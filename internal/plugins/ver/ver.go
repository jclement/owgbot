// Package ver implements /ver.
package ver

import (
	"github.com/jclement/owgbot/internal/plugin"
	"github.com/jclement/owgbot/internal/version"
)

type Plugin struct{}

func New() *Plugin { return &Plugin{} }

func (p *Plugin) Name() string { return "ver" }

func (p *Plugin) Commands() []plugin.Command {
	return []plugin.Command{{Name: "ver", Help: "bot version"}}
}

func (p *Plugin) Init(env plugin.Env) error { return nil }

func (p *Plugin) HandleCommand(ctx *plugin.Ctx, cmd, args string) error {
	ctx.Reply("owgbot " + version.Full())
	return nil
}
