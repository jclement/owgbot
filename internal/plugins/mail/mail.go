// Package mail is store-and-forward messaging between mesh users:
//
//	/mail <node> <text>   queue a message for a node
//	/mail                 how many of your messages are still queued
//
// Queued mail is delivered when the recipient shows signs of life — they
// message the bot (activity) or their node advertises on the mesh.
package mail

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jclement/owgbot/internal/plugin"
)

const maxQueuedPerSender = 10

type letter struct {
	ID   int64     `json:"id"`
	From string    `json:"from"`
	Text string    `json:"text"`
	Sent time.Time `json:"sent"`
}

type Plugin struct {
	env plugin.Env
}

func New() *Plugin { return &Plugin{} }

func (p *Plugin) Name() string { return "mail" }

func (p *Plugin) Commands() []plugin.Command {
	return []plugin.Command{{
		Name: "mail", Args: "<node> <text>", Category: "mesh",
		Help: "leave a message; delivered when they're heard",
	}}
}

func (p *Plugin) Init(env plugin.Env) error {
	p.env = env
	return nil
}

// Delivery triggers: the recipient messaged the bot, or their node
// advertised nearby.
func (p *Plugin) HandleActivity(user string) { p.deliver(user) }
func (p *Plugin) HandleAdvert(user string)   { p.deliver(user) }

func (p *Plugin) HandleCommand(ctx *plugin.Ctx, cmd, args string) error {
	args = strings.TrimSpace(args)
	if args == "" {
		return p.status(ctx)
	}
	who, text, _ := strings.Cut(args, " ")
	text = strings.TrimSpace(text)
	if text == "" {
		ctx.Reply("usage: /mail <node> <text>")
		return nil
	}
	prefix, ok := p.env.ResolveNode(who)
	if !ok {
		ctx.Reply("don't know " + who + " — use their node name or 12-hex id")
		return nil
	}
	queued, err := p.queuedBy(ctx.User)
	if err != nil {
		return err
	}
	if queued >= maxQueuedPerSender {
		ctx.Reply(fmt.Sprintf("you have %d undelivered messages queued — that's the cap", queued))
		return nil
	}

	l := letter{ID: time.Now().UnixNano(), From: ctx.User, Text: text, Sent: time.Now()}
	b, _ := json.Marshal(l)
	if err := p.env.KV.Set(prefix, key(l.ID), string(b)); err != nil {
		return err
	}
	name := p.env.NodeName(prefix)
	if name == "" {
		name = prefix
	}
	ctx.Reply("queued for " + name + " — delivered when they're next heard")
	return nil
}

// deliver sends all queued mail for a user. Runs on the bot loop, so it only
// queues outbound messages — the send worker paces them.
func (p *Plugin) deliver(user string) {
	entries, err := p.env.KV.ListUser(user, "m:")
	if err != nil || len(entries) == 0 {
		return
	}
	for _, e := range entries {
		var l letter
		if err := json.Unmarshal([]byte(e.Value), &l); err != nil {
			p.env.KV.Delete(user, e.Key)
			continue
		}
		from := p.env.NodeName(l.From)
		if from == "" {
			from = l.From
		}
		p.env.SendTo(user, fmt.Sprintf("mail from %s (%s): %s", from, plugin.AgoPhrase(l.Sent), l.Text))
		p.env.KV.Delete(user, e.Key)
	}
	p.env.Log.Info("mail delivered", "to", user, "count", len(entries))
}

func (p *Plugin) status(ctx *plugin.Ctx) error {
	n, err := p.queuedBy(ctx.User)
	if err != nil {
		return err
	}
	if n == 0 {
		ctx.Reply("no mail queued. /mail <node> <text> to leave one")
		return nil
	}
	ctx.Reply(fmt.Sprintf("%d message(s) you sent still await delivery", n))
	return nil
}

func (p *Plugin) queuedBy(sender string) (int, error) {
	entries, err := p.env.KV.List("m:")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		var l letter
		if json.Unmarshal([]byte(e.Value), &l) == nil && l.From == sender {
			n++
		}
	}
	return n, nil
}

func key(id int64) string { return fmt.Sprintf("m:%020d", id) }
