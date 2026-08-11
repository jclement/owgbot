// Package wordle is Wordle over LoRa. One word per day, shared by the whole
// mesh; six guesses; per-node streaks.
//
//	/wordle        show today's board (and make wordle sticky)
//	crane          a bare 5-letter message while sticky is a guess
package wordle

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"github.com/jclement/owgbot/internal/plugin"
)

const maxGuesses = 6

type game struct {
	Guesses []string `json:"guesses"`
	Won     bool     `json:"won"`
}

type stats struct {
	Streak  int    `json:"streak"`
	LastWin string `json:"last_win"` // date of last win, for streak continuity
}

type Plugin struct {
	env plugin.Env
}

func New() *Plugin { return &Plugin{} }

func (p *Plugin) Name() string { return "wordle" }

func (p *Plugin) Commands() []plugin.Command {
	return []plugin.Command{{Name: "wordle", Help: "daily word game (then just send guesses)", Category: "games"}}
}

func (p *Plugin) Init(env plugin.Env) error {
	p.env = env
	return nil
}

// wordOfDay is deterministic per calendar day, same for every player.
func wordOfDay(day string) string {
	h := fnv.New32a()
	h.Write([]byte("owgbot-wordle:" + day))
	return answers[h.Sum32()%uint32(len(answers))]
}

func today() string { return time.Now().Format("2006-01-02") }

func (p *Plugin) HandleCommand(ctx *plugin.Ctx, cmd, args string) error {
	g, err := p.load(ctx.User)
	if err != nil {
		return err
	}
	ctx.Reply(p.board(ctx.User, g))
	return nil
}

func (p *Plugin) HandleSession(ctx *plugin.Ctx, text string) (bool, error) {
	guess := strings.ToLower(strings.TrimSpace(text))
	if len(guess) != 5 || !isAlpha(guess) {
		return false, nil // not a guess — fall through to the default hint
	}
	g, err := p.load(ctx.User)
	if err != nil {
		return true, err
	}
	if g.Won || len(g.Guesses) >= maxGuesses {
		ctx.Reply("today's game is done — new word at midnight")
		return true, nil
	}
	// Real-Wordle rule: unknown words don't consume a guess.
	if !isValidWord(guess) {
		ctx.Reply(strings.ToUpper(guess) + " isn't in the word list · " +
			fmt.Sprint(maxGuesses-len(g.Guesses)) + " left")
		return true, nil
	}

	g.Guesses = append(g.Guesses, guess)
	word := wordOfDay(today())
	if guess == word {
		g.Won = true
	}
	if err := p.save(ctx.User, g); err != nil {
		return true, err
	}

	switch {
	case g.Won:
		streak := p.bumpStreak(ctx.User)
		ctx.Reply(fmt.Sprintf("%s %s\n🎉 got it in %d/%d! streak %d",
			score(guess, word), strings.ToUpper(guess), len(g.Guesses), maxGuesses, streak))
	case len(g.Guesses) >= maxGuesses:
		p.resetStreak(ctx.User)
		ctx.Reply(fmt.Sprintf("%s %s\nout of guesses — it was %s",
			score(guess, word), strings.ToUpper(guess), strings.ToUpper(word)))
	default:
		ctx.Reply(fmt.Sprintf("%s %s · %d left",
			score(guess, word), strings.ToUpper(guess), maxGuesses-len(g.Guesses)))
	}
	return true, nil
}

// board renders the current day's state.
func (p *Plugin) board(user string, g *game) string {
	word := wordOfDay(today())
	var b strings.Builder
	fmt.Fprintf(&b, "wordle %s · %d/%d", today(), len(g.Guesses), maxGuesses)
	for _, guess := range g.Guesses {
		fmt.Fprintf(&b, "\n%s %s", score(guess, word), strings.ToUpper(guess))
	}
	switch {
	case g.Won:
		b.WriteString("\nsolved! new word at midnight")
	case len(g.Guesses) >= maxGuesses:
		b.WriteString("\nout of guesses — it was " + strings.ToUpper(word))
	default:
		b.WriteString("\nsend a 5-letter guess")
	}
	return b.String()
}

// score renders the 🟩🟨⬛ line using standard Wordle duplicate handling:
// greens claim letters first, then yellows left to right.
func score(guess, word string) string {
	marks := [5]rune{'⬛', '⬛', '⬛', '⬛', '⬛'}
	var remaining [26]int
	for i := 0; i < 5; i++ {
		if guess[i] == word[i] {
			marks[i] = '🟩'
		} else {
			remaining[word[i]-'a']++
		}
	}
	for i := 0; i < 5; i++ {
		if marks[i] == '🟩' {
			continue
		}
		c := guess[i] - 'a'
		if remaining[c] > 0 {
			marks[i] = '🟨'
			remaining[c]--
		}
	}
	return string(marks[:])
}

func (p *Plugin) load(user string) (*game, error) {
	g := &game{}
	raw, err := p.env.KV.Get(user, "g:"+today())
	if err == nil {
		if jerr := json.Unmarshal([]byte(raw), g); jerr != nil {
			g = &game{}
		}
	}
	return g, nil
}

func (p *Plugin) save(user string, g *game) error {
	b, _ := json.Marshal(g)
	return p.env.KV.Set(user, "g:"+today(), string(b))
}

func (p *Plugin) loadStats(user string) stats {
	var s stats
	if raw, err := p.env.KV.Get(user, "stats"); err == nil {
		json.Unmarshal([]byte(raw), &s)
	}
	return s
}

func (p *Plugin) bumpStreak(user string) int {
	s := p.loadStats(user)
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	if s.LastWin == yesterday || s.LastWin == today() {
		s.Streak++
	} else {
		s.Streak = 1
	}
	s.LastWin = today()
	b, _ := json.Marshal(s)
	p.env.KV.Set(user, "stats", string(b))
	return s.Streak
}

func (p *Plugin) resetStreak(user string) {
	s := p.loadStats(user)
	s.Streak = 0
	b, _ := json.Marshal(s)
	p.env.KV.Set(user, "stats", string(b))
}

func isAlpha(s string) bool {
	for _, c := range s {
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}
