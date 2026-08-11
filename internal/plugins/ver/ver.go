// Package ver implements /ver: bot version plus the companion radio's
// firmware version and model.
package ver

import (
	"fmt"

	"github.com/jclement/owgbot/internal/plugin"
	"github.com/jclement/owgbot/internal/version"
)

type Plugin struct {
	env plugin.Env
}

func New() *Plugin { return &Plugin{} }

func (p *Plugin) Name() string { return "ver" }

func (p *Plugin) Commands() []plugin.Command {
	return []plugin.Command{{Name: "ver", Help: "bot + radio versions"}}
}

func (p *Plugin) Init(env plugin.Env) error {
	p.env = env
	return nil
}

func (p *Plugin) HandleCommand(ctx *plugin.Ctx, cmd, args string) error {
	reply := "owgbot " + version.Full()
	if self := p.env.Self(); self.Model != "" {
		reply += "\nradio: " + self.Model
		if self.FwVer > 0 {
			reply += fmt.Sprintf(" fw%d", self.FwVer)
		}
	}
	ctx.Reply(reply)
	return nil
}
