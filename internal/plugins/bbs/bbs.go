// Package bbs is the nostalgia corner: /tl serves a random BBS-style
// tagline from the pool (mesh puns, mostly), and /8008 is exactly the
// command your inner twelve-year-old thinks it is.
package bbs

import (
	"math/rand/v2"
	"strconv"

	"github.com/jclement/owgbot/internal/plugin"
)

type Plugin struct {
	env plugin.Env
}

func New() *Plugin { return &Plugin{} }

func (p *Plugin) Name() string { return "bbs" }

func (p *Plugin) Commands() []plugin.Command {
	return []plugin.Command{
		{Name: "tl", Help: "random tagline", Category: "games"},
		// Hidden on purpose: some things you have to discover the way we
		// did in 1993 — by typing it into a calculator first.
		{Name: "8008", Hidden: true},
	}
}

func (p *Plugin) Init(env plugin.Env) error {
	p.env = env
	return nil
}

func (p *Plugin) HandleCommand(ctx *plugin.Ctx, cmd, args string) error {
	switch cmd {
	case "tl":
		ctx.Reply(p.pickTagline(ctx.User))
	case "8008":
		ctx.Reply("( . )( . )\nas rendered at 300 baud")
	}
	return nil
}

// pickTagline picks a random tagline, avoiding serving a user the same one
// twice in a row (the last index is remembered per user).
func (p *Plugin) pickTagline(user string) string {
	i := rand.IntN(len(taglines))
	if last, err := p.env.KV.Get(user, "last"); err == nil {
		if l, err := strconv.Atoi(last); err == nil && l == i {
			i = (i + 1) % len(taglines)
		}
	}
	if err := p.env.KV.Set(user, "last", strconv.Itoa(i)); err != nil {
		p.env.Log.Warn("saving tagline state failed", "err", err)
	}
	return taglines[i]
}
