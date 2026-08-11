// Package admin implements admin-only commands: /plugins and /update.
// Admins are node pubkey prefixes listed in config; for everyone else these
// commands don't exist (the dispatcher answers "unknown command").
package admin

import (
	"context"
	"fmt"
	"strings"

	"github.com/jclement/owgbot/internal/plugin"
	"github.com/jclement/owgbot/internal/selfupdate"
	"github.com/jclement/owgbot/internal/version"
)

// Plugin needs hooks back into the app: the plugin roster for /plugins, a
// restart trigger for /update (flush outbound then exit; systemd restarts),
// and an advert trigger for /advert.
type Plugin struct {
	env     plugin.Env
	plugins func() []plugin.Plugin
	restart func()
	advert  func(ctx context.Context) error
}

func New(plugins func() []plugin.Plugin, restart func(), advert func(ctx context.Context) error) *Plugin {
	return &Plugin{plugins: plugins, restart: restart, advert: advert}
}

func (p *Plugin) Name() string { return "admin" }

func (p *Plugin) Commands() []plugin.Command {
	return []plugin.Command{
		{Name: "plugins", Help: "list plugins", Admin: true},
		{Name: "update", Help: "self-update from github", Admin: true},
		{Name: "advert", Help: "broadcast a self-advert now", Admin: true},
	}
}

func (p *Plugin) Init(env plugin.Env) error {
	p.env = env
	// If an /update restarted us, tell the admin who ran it that we're back.
	if user, err := env.KV.Get("", "notify"); err == nil {
		env.SendTo(user, "update complete — running "+version.Full())
		if derr := env.KV.Delete("", "notify"); derr != nil {
			env.Log.Warn("clearing update notify failed", "err", derr)
		}
	}
	return nil
}

func (p *Plugin) HandleCommand(ctx *plugin.Ctx, cmd, args string) error {
	switch cmd {
	case "plugins":
		var names []string
		for _, pl := range p.plugins() {
			names = append(names, pl.Name())
		}
		ctx.Reply("plugins: " + strings.Join(names, ", "))
		return nil
	case "update":
		return p.update(ctx)
	case "advert":
		if err := p.advert(ctx.Context); err != nil {
			ctx.Reply("advert failed: " + err.Error())
			return nil
		}
		ctx.Reply("advert sent")
		return nil
	}
	return nil
}

func (p *Plugin) update(ctx *plugin.Ctx) error {
	repo := ctx.Config.GithubRepo
	rel, err := selfupdate.Check(repo)
	if err != nil {
		ctx.Reply("update: " + err.Error())
		return nil
	}
	if version.Tag != "" && version.Tag == rel.Tag {
		ctx.Reply("up to date (" + version.Full() + ")")
		return nil
	}
	cur := version.Tag
	if cur == "" {
		cur = "dev " + version.String()
	}
	ctx.Reply(fmt.Sprintf("updating %s -> %s, back in a minute", cur, rel.Tag))
	if err := rel.Apply(); err != nil {
		ctx.Reply("update failed: " + err.Error())
		return nil
	}
	// Leave a note so the next boot DMs this admin that we're back.
	if err := p.env.KV.Set("", "notify", ctx.User); err != nil {
		p.env.Log.Warn("saving update notify failed", "err", err)
	}
	p.env.Log.Info("update applied; restarting", "tag", rel.Tag)
	p.restart()
	return nil
}
