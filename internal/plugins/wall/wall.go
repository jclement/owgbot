// Package wall is the graffiti wall — the guestbook every BBS deserved:
//
//	/wall <text>   scrawl something (one line, kept short)
//	/wall          read the latest scrawls
package wall

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jclement/owgbot/internal/plugin"
)

const (
	maxEntryLen = 100
	showEntries = 5
	keepEntries = 10
)

type entry struct {
	From string    `json:"from"`
	Name string    `json:"name,omitempty"`
	Text string    `json:"text"`
	Time time.Time `json:"time"`
}

type Plugin struct {
	env plugin.Env
}

func New() *Plugin { return &Plugin{} }

func (p *Plugin) Name() string { return "wall" }

func (p *Plugin) Commands() []plugin.Command {
	return []plugin.Command{{
		Name: "wall", Args: "[text]", Category: "mesh",
		Help: "read or scrawl on the graffiti wall",
	}}
}

func (p *Plugin) Init(env plugin.Env) error {
	p.env = env
	return nil
}

func (p *Plugin) HandleCommand(ctx *plugin.Ctx, cmd, args string) error {
	args = strings.TrimSpace(args)
	if args == "" {
		return p.read(ctx)
	}
	return p.write(ctx, args)
}

func (p *Plugin) write(ctx *plugin.Ctx, text string) error {
	if len(text) > maxEntryLen {
		ctx.Reply(fmt.Sprintf("keep it under %d chars — it's a wall, not a manifesto", maxEntryLen))
		return nil
	}
	e := entry{From: ctx.User, Name: p.env.NodeName(ctx.User), Text: text, Time: time.Now()}
	b, _ := json.Marshal(e)
	// Global entries live under user "" so the wall is shared.
	if err := p.env.KV.Set("", fmt.Sprintf("w:%020d", time.Now().UnixNano()), string(b)); err != nil {
		return err
	}
	p.prune()
	ctx.Reply("scrawled. /wall to admire it")
	return nil
}

func (p *Plugin) read(ctx *plugin.Ctx) error {
	entries, err := p.env.KV.ListUser("", "w:")
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		ctx.Reply("the wall is bare. /wall <text> to fix that")
		return nil
	}
	start := len(entries) - showEntries
	if start < 0 {
		start = 0
	}
	var b strings.Builder
	for i := len(entries) - 1; i >= start; i-- { // newest first
		var e entry
		if json.Unmarshal([]byte(entries[i].Value), &e) != nil {
			continue
		}
		who := e.Name
		if who == "" {
			who = e.From[:4]
		}
		fmt.Fprintf(&b, "%s · %s: %s\n", plugin.Ago(e.Time), who, e.Text)
	}
	ctx.Reply(strings.TrimRight(b.String(), "\n"))
	return nil
}

// prune keeps the wall from growing without bound.
func (p *Plugin) prune() {
	entries, err := p.env.KV.ListUser("", "w:")
	if err != nil || len(entries) <= keepEntries {
		return
	}
	for _, e := range entries[:len(entries)-keepEntries] {
		p.env.KV.Delete("", e.Key)
	}
}
