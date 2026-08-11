// Package admin implements admin-only commands: /plugins and /update.
// Admins are node pubkey prefixes listed in config; for everyone else these
// commands don't exist (the dispatcher answers "unknown command").
package admin

import (
	"fmt"
	"strings"

	"github.com/jclement/owgbot/internal/plugin"
	"github.com/jclement/owgbot/internal/selfupdate"
	"github.com/jclement/owgbot/internal/version"
)

// Plugin needs hooks back into the app: the plugin roster for /plugins, and
// a restart trigger for /update (flush outbound then exit; systemd restarts).
type Plugin struct {
	env     plugin.Env
	plugins func() []plugin.Plugin
	restart func()
}

func New(plugins func() []plugin.Plugin, restart func()) *Plugin {
	return &Plugin{plugins: plugins, restart: restart}
}

func (p *Plugin) Name() string { return "admin" }

func (p *Plugin) Commands() []plugin.Command {
	return []plugin.Command{
		{Name: "plugins", Help: "list plugins", Admin: true},
		{Name: "update", Help: "self-update from github", Admin: true},
	}
}

func (p *Plugin) Init(env plugin.Env) error {
	p.env = env
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
	p.env.Log.Info("update applied; restarting", "tag", rel.Tag)
	p.restart()
	return nil
}
