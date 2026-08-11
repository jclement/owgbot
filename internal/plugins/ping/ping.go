// Package ping implements /ping — a range-testing tool disguised as a toy.
// Replies with the SNR the radio heard you at. As a session plugin it stays
// sticky, so while you walk the neighborhood every bare message gets an SNR
// report back.
package ping

import (
	"fmt"
	"strings"

	"github.com/jclement/owgbot/internal/plugin"
)

type Plugin struct{}

func New() *Plugin { return &Plugin{} }

func (p *Plugin) Name() string { return "ping" }

func (p *Plugin) Commands() []plugin.Command {
	return []plugin.Command{{Name: "ping", Args: "[off]", Help: "pong + your SNR (repeats until 'off')", Category: "mesh"}}
}

func (p *Plugin) Init(env plugin.Env) error { return nil }

func (p *Plugin) HandleCommand(ctx *plugin.Ctx, cmd, args string) error {
	if isOff(args) {
		ctx.EndSession()
		ctx.Reply("ok, going quiet")
		return nil
	}
	ctx.Reply("pong · " + report(ctx) + " — keep sending, I'll keep reporting ('off' to stop)")
	return nil
}

func (p *Plugin) HandleSession(ctx *plugin.Ctx, text string) (bool, error) {
	if isOff(text) {
		ctx.EndSession()
		ctx.Reply("ok, going quiet")
		return true, nil
	}
	ctx.Reply("pong · " + report(ctx))
	return true, nil
}

// report renders SNR + route. SNR is measured on the final hop into the
// bot, so unless the message was heard direct it's the last relay's link,
// not the sender's. Routed messages don't carry a hop count at all.
func report(ctx *plugin.Ctx) string {
	switch {
	case ctx.Hops > 0:
		return fmt.Sprintf("flood via %d hop(s), last hop %s", ctx.Hops, snr(ctx.SNR))
	case ctx.Hops < 0:
		return snr(ctx.SNR) + " · routed (hops unknown, SNR is last hop's)"
	default:
		return snr(ctx.SNR) + " direct"
	}
}

func isOff(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off", "stop", "quit", "x":
		return true
	}
	return false
}

func snr(v float64) string {
	if v == 0 {
		return "SNR n/a"
	}
	return fmt.Sprintf("SNR %.1fdB", v)
}
