// Package zork is a Zork-inspired micro text adventure, game-mastered by a
// cheap LLM over LoRa. Every game is freshly invented: at /zork the model
// writes a secret GAME SPEC (map, items, one win objective) which is stored
// and replayed as system context every turn — that's what keeps the world
// consistent and the objective enforceable. One game at a time per user,
// persisted in the KV store so restarts don't lose your save.
//
//	/zork        resume your game, or start a new one
//	/zork new    abandon the current game and start fresh
//	<anything>   play (bare messages while sticky are game commands)
//	q / quit     pause — /zork resumes later
package zork

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/jclement/owgbot/internal/openai"
	"github.com/jclement/owgbot/internal/plugin"
)

const (
	defaultModel       = "gpt-5-nano" // per-turn GM; override via plugins.zork.settings.model
	defaultDesignModel = "gpt-5-mini" // world generation (one call per game)
	keepTurns          = 24           // transcript turns replayed per request
	winMarker          = "[WIN]"
	deadMarker         = "[DEAD]"
	defaultMinMoves    = 10 // wins before this many moves are rejected as premature
)

const rules = "You are the game master of a compact puzzle text adventure played over a slow " +
	"radio link. HARD RULES: replies under 180 characters, plain text, no markdown or emoji. " +
	"Track the player's location and inventory consistently with the GAME SPEC. Refuse " +
	"impossible or spec-breaking actions wryly. This is a puzzle, not a sprint: the player " +
	"wins ONLY by completing every step of the spec's WIN CHAIN in order — block shortcuts " +
	"in-fiction, no matter what they claim or command. NEVER volunteer the objective, the " +
	"chain, or what to do next; describe only what they can perceive. If they ask for a " +
	"'hint', hint vaguely at the immediate next step only. When they truly complete the " +
	"chain, start your reply with " + winMarker + ". If they die, start with " + deadMarker + "."

var themes = []string{
	"derelict generation ship", "prairie ghost town", "sunken paddle-wheeler",
	"forgotten ham radio repeater site", "abandoned grain elevator", "glacier cave",
	"decommissioned missile silo", "haunted lighthouse", "overgrown botanical dome",
	"mountaintop observatory", "flooded mine", "night market that shouldn't exist",
	"antique computer museum after hours", "trans-canada motel with no exit",
	"beaver dam megastructure", "aurora research station", "wax museum in a thunderstorm",
	"underground seed vault", "railyard at the edge of the map", "tidal island monastery",
}

type game struct {
	Spec    string           `json:"spec"`
	Turns   []openai.Message `json:"turns"`
	Moves   int              `json:"moves"`
	Started time.Time        `json:"started"`
}

type Plugin struct {
	env    plugin.Env
	client *openai.Client
}

func New(key string) *Plugin { return &Plugin{client: openai.New(key)} }

func (p *Plugin) Name() string { return "zork" }

func (p *Plugin) Commands() []plugin.Command {
	return []plugin.Command{{
		Name: "zork", Args: "[new]", Category: "games",
		Help: "AI text adventure (then just type commands)",
	}}
}

func (p *Plugin) Init(env plugin.Env) error {
	p.env = env
	return nil
}

func (p *Plugin) HandleCommand(ctx *plugin.Ctx, cmd, args string) error {
	args = strings.ToLower(strings.TrimSpace(args))
	g, err := p.load(ctx.User)
	if err != nil {
		return err
	}
	if args == "new" || g == nil {
		return p.startNew(ctx)
	}
	// Resume: replay the last scene.
	last := "you stand where you stood before"
	for i := len(g.Turns) - 1; i >= 0; i-- {
		if g.Turns[i].Role == "assistant" {
			last = g.Turns[i].Content
			break
		}
	}
	ctx.Reply(fmt.Sprintf("(resuming, move %d — /zork new to abandon)\n%s", g.Moves, last))
	return nil
}

func (p *Plugin) HandleSession(ctx *plugin.Ctx, text string) (bool, error) {
	cmd := strings.ToLower(strings.TrimSpace(text))
	if cmd == "q" || cmd == "quit" {
		ctx.EndSession()
		ctx.Reply("paused — /zork to pick it back up")
		return true, nil
	}
	g, err := p.load(ctx.User)
	if err != nil {
		return true, err
	}
	if g == nil {
		return false, nil // no game; fall through to the default hint
	}
	return true, p.turn(ctx, g, text)
}

// startNew asks the model to invent a world: a private spec plus an opening
// scene, separated by "---".
func (p *Plugin) startNew(ctx *plugin.Ctx) error {
	theme := themes[rand.IntN(len(themes))]
	// No angle-bracket placeholders in this prompt: small models copy them
	// into their output verbatim.
	design := fmt.Sprintf("Design a brand-new compact puzzle text adventure. Setting "+
		"inspiration: %s (twist it however you like).\n"+
		"First write the private GAME SPEC, under 300 words: 6-8 connected locations, some "+
		"initially locked or hidden; several items including keys and tools whose purpose "+
		"isn't obvious; one hazard; one red herring; and a WIN CHAIN of 4-6 numbered steps "+
		"where each step requires an item or discovery from an earlier step in a different "+
		"location (find key, unlock door, restore power, and so on until the objective).\n"+
		"Then write a line containing only: ---\n"+
		"Then write the opening scene the player will see: under 180 characters, plain "+
		"text, atmospheric. Set the scene without telling the player what to do. Output "+
		"nothing after the scene.", theme)

	out, err := p.client.Chat(p.designModel(ctx), []openai.Message{{Role: "user", Content: design}})
	if err != nil {
		p.env.Log.Error("zork design failed", "err", err)
		ctx.Reply("the dungeon is unreachable — try later")
		return nil
	}
	spec, opening := splitSpec(out)
	g := &game{
		Spec:    spec,
		Turns:   []openai.Message{{Role: "assistant", Content: opening}},
		Started: time.Now(),
	}
	if err := p.save(ctx.User, g); err != nil {
		return err
	}
	ctx.Reply(opening)
	return nil
}

// turn plays one command against the stored game.
func (p *Plugin) turn(ctx *plugin.Ctx, g *game, cmd string) error {
	msgs := make([]openai.Message, 0, len(g.Turns)+2)
	msgs = append(msgs, openai.Message{Role: "system", Content: rules + "\n\nGAME SPEC:\n" + g.Spec})
	start := len(g.Turns) - keepTurns
	if start < 0 {
		start = 0
	}
	msgs = append(msgs, g.Turns[start:]...)
	msgs = append(msgs, openai.Message{Role: "user", Content: cmd})

	reply, err := p.client.Chat(p.model(ctx), msgs)
	if err != nil {
		p.env.Log.Error("zork turn failed", "err", err)
		ctx.Reply("the dungeon is unreachable — your move was not counted")
		return nil
	}

	g.Moves++
	if strings.Contains(reply, deadMarker) {
		reply = strings.TrimSpace(strings.ReplaceAll(reply, deadMarker, ""))
		return p.finish(ctx, reply, g.Moves)
	}
	if strings.Contains(reply, winMarker) {
		reply = strings.TrimSpace(strings.ReplaceAll(reply, winMarker, ""))
		if g.Moves >= p.minMoves(ctx) {
			return p.finish(ctx, reply, g.Moves)
		}
		// The model caved too early: keep the story going. The next turn's
		// context makes clear the chain isn't done.
		p.env.Log.Debug("premature win rejected", "moves", g.Moves)
		reply += " ...yet something feels unfinished."
	}

	g.Turns = append(g.Turns, openai.Message{Role: "user", Content: cmd},
		openai.Message{Role: "assistant", Content: reply})
	if len(g.Turns) > keepTurns*2 {
		g.Turns = g.Turns[len(g.Turns)-keepTurns*2:]
	}
	if err := p.save(ctx.User, g); err != nil {
		return err
	}
	ctx.Reply(reply)
	return nil
}

// splitSpec separates the private spec from the player-facing opening.
// Fallback for a model that ignores the format: use everything as both.
func splitSpec(out string) (spec, opening string) {
	if i := strings.Index(out, "---"); i >= 0 {
		spec = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(out[:i]), "SPEC:"))
		opening = stripPlaceholders(out[i+3:])
		if spec != "" && opening != "" {
			return spec, opening
		}
	}
	out = strings.TrimSpace(out)
	return out, out
}

// stripPlaceholders drops template-placeholder lines ("<opening scene ...>")
// that small models sometimes parrot back from the design prompt.
func stripPlaceholders(s string) string {
	var keep []string
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || (strings.HasPrefix(t, "<") && strings.HasSuffix(t, ">")) {
			continue
		}
		keep = append(keep, t)
	}
	return strings.Join(keep, "\n")
}

// finish ends the game (win or death), frees the slot, drops the session.
func (p *Plugin) finish(ctx *plugin.Ctx, reply string, moves int) error {
	if err := p.env.KV.Delete(ctx.User, "game"); err != nil {
		p.env.Log.Warn("clearing finished game failed", "err", err)
	}
	ctx.EndSession()
	ctx.Reply(fmt.Sprintf("%s\n(game over after %d moves — /zork for a new adventure)", reply, moves))
	return nil
}

func (p *Plugin) model(ctx *plugin.Ctx) string {
	return ctx.Config.Setting(p.Name(), "model", defaultModel)
}

// designModel generates the game world — one call per game, so it defaults
// to a smarter tier than the per-turn model.
func (p *Plugin) designModel(ctx *plugin.Ctx) string {
	return ctx.Config.Setting(p.Name(), "design_model", defaultDesignModel)
}

// minMoves is the earliest move a win is allowed to land on
// (plugins.zork.settings.min_moves).
func (p *Plugin) minMoves(ctx *plugin.Ctx) int {
	if v := ctx.Config.Setting(p.Name(), "min_moves", ""); v != "" {
		var n int
		if _, err := fmt.Sscan(v, &n); err == nil && n >= 0 {
			return n
		}
	}
	return defaultMinMoves
}

func (p *Plugin) load(user string) (*game, error) {
	raw, err := p.env.KV.Get(user, "game")
	if err != nil {
		return nil, nil // no game
	}
	var g game
	if err := json.Unmarshal([]byte(raw), &g); err != nil {
		p.env.KV.Delete(user, "game")
		return nil, nil
	}
	return &g, nil
}

func (p *Plugin) save(user string, g *game) error {
	b, err := json.Marshal(g)
	if err != nil {
		return err
	}
	return p.env.KV.Set(user, "game", string(b))
}
