// Package ai is an LLM over LoRa: /ai asks OpenAI a question, and while the
// plugin is sticky, bare messages continue the conversation. The system
// prompt begs for brevity — every 130 bytes is another transmission.
//
// The plugin is only registered when an API key is present (config
// plugins.ai.settings.api_key, or the OPENAI_API_KEY environment variable).
package ai

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/jclement/owgbot/internal/openai"
	"github.com/jclement/owgbot/internal/plugin"
)

const (
	defaultModel  = "gpt-5-mini"
	maxHistory    = 8 // turns kept per user (in memory only)
	maxToolRounds = 3 // tool-call round trips per user message
	systemPrompt  = "You are owgbot, a bot on a LoRa mesh radio network. Replies travel over " +
		"the air at great cost: answer in under 120 characters when possible, 300 absolute max. " +
		"Plain text only, no markdown, no emoji. Be direct and a little wry. " +
		"You can run bot commands with the run_command tool — ONLY these: " +
		"/w <place> (weather), /seen <node>, /nodes, /remind +5d <text>, /remind, " +
		"/mail <node> <text>, /wall, /wall <text>, /ping, /ver. Use them when they answer " +
		"the user's question; never claim abilities beyond them, and never invent commands."
)

// allowedCommands is what the model may execute via run_command. No admin
// commands, no /ai recursion, nothing that hijacks game or browser sessions.
var allowedCommands = map[string]bool{
	"w": true, "seen": true, "nodes": true, "remind": true,
	"mail": true, "wall": true, "ping": true, "ver": true,
}

var commandTool = []openai.Tool{{
	Type: "function",
	Function: openai.ToolFunction{
		Name:        "run_command",
		Description: "Run an owgbot slash command as the current user and get its reply text.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "The full command, e.g. \"/w calgary\" or \"/seen bob\"",
				},
			},
			"required": []string{"command"},
		},
	},
}}

type msg = openai.Message

// RunCommand dispatches a slash command in the context of the triggering
// message (user, SNR, hops) and returns the reply text (wired to
// bot.RunCommand).
type RunCommand func(ctx *plugin.Ctx, command string) string

type Plugin struct {
	env    plugin.Env
	client *openai.Client
	runCmd RunCommand

	mu   sync.Mutex
	hist map[string][]msg
}

// New builds the plugin with the given API key (caller decides presence).
func New(key string, runCmd RunCommand) *Plugin {
	return &Plugin{
		client: openai.New(key),
		runCmd: runCmd,
		hist:   make(map[string][]msg),
	}
}

func (p *Plugin) Name() string { return "ai" }

func (p *Plugin) Commands() []plugin.Command {
	return []plugin.Command{{
		Name: "ai", Args: "[question]",
		Help: "chat with the AI (bare /ai says hello)",
	}}
}

func (p *Plugin) Init(env plugin.Env) error {
	p.env = env
	return nil
}

// greetPrompt is the synthetic instruction for a bare /ai: open the chat
// rather than lecture about usage. It is never stored in history.
const greetPrompt = "(the user just opened a chat with /ai and hasn't said anything yet — " +
	"greet them briefly, by name if you know it, and invite a question; under 100 characters)"

func (p *Plugin) HandleCommand(ctx *plugin.Ctx, cmd, args string) error {
	q := strings.TrimSpace(args)
	switch q {
	case "":
		// Bare /ai starts the conversation; the session is now sticky, so
		// whatever they say next is the chat.
		return p.chat(ctx, greetPrompt, false)
	case "clear", "reset":
		p.mu.Lock()
		delete(p.hist, ctx.User)
		p.mu.Unlock()
		ctx.Reply("fresh context")
		return nil
	}
	return p.chat(ctx, q, true)
}

func (p *Plugin) HandleSession(ctx *plugin.Ctx, text string) (bool, error) {
	return true, p.chat(ctx, text, true)
}

// chat runs one conversational turn. transcript=false marks a synthetic
// prompt (the /ai greeting): only the assistant's reply enters history.
func (p *Plugin) chat(ctx *plugin.Ctx, text string, transcript bool) error {
	p.mu.Lock()
	history := append([]msg(nil), p.hist[ctx.User]...)
	p.mu.Unlock()

	messages := make([]msg, 0, len(history)+2)
	messages = append(messages, msg{Role: "system", Content: systemPrompt + "\n" + peerContext(ctx, p.env)})
	messages = append(messages, history...)
	messages = append(messages, msg{Role: "user", Content: text})

	model := p.env.Config().Setting(p.Name(), "model", defaultModel)
	reply, err := p.converse(ctx, model, messages)
	if err != nil {
		p.env.Log.Error("openai request failed", "err", err)
		ctx.Reply("ai: the cloud is unreachable (or unhappy) — try later")
		return nil
	}

	p.mu.Lock()
	h := p.hist[ctx.User]
	if transcript {
		h = append(h, msg{Role: "user", Content: text})
	}
	h = append(h, msg{Role: "assistant", Content: reply})
	if len(h) > maxHistory {
		h = h[len(h)-maxHistory:]
	}
	p.hist[ctx.User] = h
	p.mu.Unlock()

	ctx.Reply(reply)
	return nil
}

// converse runs the completion loop, executing run_command tool calls
// (allowlisted, as the requesting user) until the model produces text.
func (p *Plugin) converse(ctx *plugin.Ctx, model string, messages []msg) (string, error) {
	for round := 0; round < maxToolRounds; round++ {
		m, err := p.client.ChatTools(model, messages, commandTool)
		if err != nil {
			return "", err
		}
		if len(m.ToolCalls) == 0 {
			reply := strings.TrimSpace(m.Content)
			if reply == "" {
				return "", fmt.Errorf("openai: empty content")
			}
			return reply, nil
		}
		messages = append(messages, m)
		for i, tc := range m.ToolCalls {
			result := "too many tool calls"
			if i < 3 {
				result = p.execTool(ctx, tc)
			}
			messages = append(messages, msg{Role: "tool", ToolCallID: tc.ID, Content: result})
		}
	}
	return "", fmt.Errorf("openai: tool loop did not converge")
}

// execTool runs one run_command call against the bot's dispatcher.
func (p *Plugin) execTool(ctx *plugin.Ctx, tc openai.ToolCall) string {
	if tc.Function.Name != "run_command" {
		return "unknown tool"
	}
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		return "bad arguments"
	}
	cmd := strings.TrimSpace(args.Command)
	name, _, _ := strings.Cut(strings.TrimPrefix(cmd, "/"), " ")
	if !allowedCommands[strings.ToLower(name)] {
		return "command not allowed"
	}
	p.env.Log.Info("ai tool call", "user", ctx.User, "command", cmd)
	result := p.runCmd(ctx, cmd)
	if len(result) > 500 {
		result = result[:500]
	}
	return result
}

// peerContext describes who's talking and how well we hear them, so the
// model can be personable ("hi Bob") and radio-aware ("your link is rough").
func peerContext(ctx *plugin.Ctx, env plugin.Env) string {
	who := env.NodeName(ctx.User)
	if who == "" {
		who = "node " + ctx.User[:4]
	}
	link := "signal unknown"
	if ctx.SNR != 0 {
		link = fmt.Sprintf("received at SNR %.1f dB", ctx.SNR)
	}
	switch {
	case ctx.Hops == 0:
		link += ", direct link"
	case ctx.Hops > 0:
		link += fmt.Sprintf(", flooded over %d hop(s) (SNR is the last hop's)", ctx.Hops)
	default:
		link += ", routed path (hop count unknown; SNR is the last hop's)"
	}
	return fmt.Sprintf("Current peer: %s, %s.", who, link)
}
