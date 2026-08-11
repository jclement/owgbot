// Package ai is an LLM over LoRa: /ai asks OpenAI a question, and while the
// plugin is sticky, bare messages continue the conversation. The system
// prompt begs for brevity — every 130 bytes is another transmission.
//
// The plugin is only registered when an API key is present (config
// plugins.ai.settings.api_key, or the OPENAI_API_KEY environment variable).
package ai

import (
	"fmt"
	"strings"
	"sync"

	"github.com/jclement/owgbot/internal/openai"
	"github.com/jclement/owgbot/internal/plugin"
)

const (
	defaultModel = "gpt-5-mini"
	maxHistory   = 8 // turns kept per user (in memory only)
	systemPrompt = "You are owgbot, a bot on a LoRa mesh radio network. Replies travel over " +
		"the air at great cost: answer in under 120 characters when possible, 300 absolute max. " +
		"Plain text only, no markdown, no emoji. Be direct and a little wry."
)

type msg = openai.Message

type Plugin struct {
	env    plugin.Env
	client *openai.Client

	mu   sync.Mutex
	hist map[string][]msg
}

// New builds the plugin with the given API key (caller decides presence).
func New(key string) *Plugin {
	return &Plugin{
		client: openai.New(key),
		hist:   make(map[string][]msg),
	}
}

func (p *Plugin) Name() string { return "ai" }

func (p *Plugin) Commands() []plugin.Command {
	return []plugin.Command{{
		Name: "ai", Args: "<question>",
		Help: "ask the AI (then just keep chatting)",
	}}
}

func (p *Plugin) Init(env plugin.Env) error {
	p.env = env
	return nil
}

func (p *Plugin) HandleCommand(ctx *plugin.Ctx, cmd, args string) error {
	q := strings.TrimSpace(args)
	switch q {
	case "":
		ctx.Reply("usage: /ai <question> — follow-ups need no /ai. /ai clear resets")
		return nil
	case "clear", "reset":
		p.mu.Lock()
		delete(p.hist, ctx.User)
		p.mu.Unlock()
		ctx.Reply("fresh context")
		return nil
	}
	return p.chat(ctx, q)
}

func (p *Plugin) HandleSession(ctx *plugin.Ctx, text string) (bool, error) {
	return true, p.chat(ctx, text)
}

func (p *Plugin) chat(ctx *plugin.Ctx, text string) error {
	p.mu.Lock()
	history := append([]msg(nil), p.hist[ctx.User]...)
	p.mu.Unlock()

	messages := make([]msg, 0, len(history)+2)
	messages = append(messages, msg{Role: "system", Content: systemPrompt + "\n" + peerContext(ctx, p.env)})
	messages = append(messages, history...)
	messages = append(messages, msg{Role: "user", Content: text})

	model := p.env.Config().Setting(p.Name(), "model", defaultModel)
	reply, err := p.client.Chat(model, messages)
	if err != nil {
		p.env.Log.Error("openai request failed", "err", err)
		ctx.Reply("ai: the cloud is unreachable (or unhappy) — try later")
		return nil
	}

	p.mu.Lock()
	h := append(p.hist[ctx.User], msg{Role: "user", Content: text}, msg{Role: "assistant", Content: reply})
	if len(h) > maxHistory {
		h = h[len(h)-maxHistory:]
	}
	p.hist[ctx.User] = h
	p.mu.Unlock()

	ctx.Reply(reply)
	return nil
}

// peerContext describes who's talking and how well we hear them, so the
// model can be personable ("hi Bob") and radio-aware ("your link is rough").
func peerContext(ctx *plugin.Ctx, env plugin.Env) string {
	who := env.NodeName(ctx.User)
	if who == "" {
		who = "node " + ctx.User[:4]
	}
	link := "signal unknown"
	if ctx.SNR != 0 {
		link = fmt.Sprintf("received at SNR %.1f dB", ctx.SNR)
	}
	switch {
	case ctx.Hops == 0:
		link += ", direct link"
	case ctx.Hops > 0:
		link += fmt.Sprintf(", relayed over %d hop(s) (SNR is the last hop's)", ctx.Hops)
	}
	return fmt.Sprintf("Current peer: %s, %s.", who, link)
}
