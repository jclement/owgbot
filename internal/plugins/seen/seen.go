// Package seen is the mesh radar: it records every advert and every message
// the bot hears, and answers /seen <node> and /nodes from that log.
package seen

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jclement/owgbot/internal/plugin"
)

const maxListed = 15

type record struct {
	Time time.Time `json:"t"`
	Name string    `json:"n,omitempty"`
}

type Plugin struct {
	env plugin.Env
}

func New() *Plugin { return &Plugin{} }

func (p *Plugin) Name() string { return "seen" }

func (p *Plugin) Commands() []plugin.Command {
	return []plugin.Command{
		{Name: "seen", Args: "<node>", Help: "when a node was last heard", Category: "mesh"},
		{Name: "nodes", Help: "recently heard nodes", Category: "mesh"},
	}
}

func (p *Plugin) Init(env plugin.Env) error {
	p.env = env
	return nil
}

// HandleAdvert and HandleActivity both mean "this node is alive right now".
func (p *Plugin) HandleAdvert(user string)   { p.mark(user) }
func (p *Plugin) HandleActivity(user string) { p.mark(user) }

func (p *Plugin) mark(user string) {
	r := record{Time: time.Now(), Name: p.env.NodeName(user)}
	b, _ := json.Marshal(r)
	if err := p.env.KV.Set(user, "last", string(b)); err != nil {
		p.env.Log.Warn("recording sighting failed", "err", err)
	}
}

func (p *Plugin) HandleCommand(ctx *plugin.Ctx, cmd, args string) error {
	switch cmd {
	case "seen":
		return p.seen(ctx, strings.TrimSpace(args))
	case "nodes":
		return p.nodes(ctx)
	}
	return nil
}

func (p *Plugin) seen(ctx *plugin.Ctx, who string) error {
	if who == "" {
		ctx.Reply("usage: /seen <name or 12-hex node id>")
		return nil
	}
	prefix, ok := p.env.ResolveNode(who)
	if !ok {
		ctx.Reply("don't know " + who + " — try the 12-hex node id")
		return nil
	}
	raw, err := p.env.KV.Get(prefix, "last")
	if err != nil {
		ctx.Reply(label(prefix, p.env.NodeName(prefix)) + ": never heard")
		return nil
	}
	var r record
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return err
	}
	ctx.Reply(fmt.Sprintf("%s: heard %s", label(prefix, r.Name), plugin.AgoPhrase(r.Time)))
	return nil
}

func (p *Plugin) nodes(ctx *plugin.Ctx) error {
	entries, err := p.env.KV.List("last")
	if err != nil {
		return err
	}
	type row struct {
		prefix string
		rec    record
	}
	var rows []row
	for _, e := range entries {
		var r record
		if json.Unmarshal([]byte(e.Value), &r) == nil {
			rows = append(rows, row{e.User, r})
		}
	}
	if len(rows) == 0 {
		ctx.Reply("no nodes heard yet")
		return nil
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].rec.Time.After(rows[j].rec.Time) })
	if len(rows) > maxListed {
		rows = rows[:maxListed]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d node(s):\n", len(rows))
	for _, r := range rows {
		fmt.Fprintf(&b, "%s · %s\n", label(r.prefix, r.rec.Name), plugin.Ago(r.rec.Time))
	}
	ctx.Reply(strings.TrimRight(b.String(), "\n"))
	return nil
}

// label shows "name (abcd)" when a name is known, else the full prefix.
func label(prefix, name string) string {
	if name != "" {
		return fmt.Sprintf("%s (%s)", name, prefix[:4])
	}
	return prefix
}
