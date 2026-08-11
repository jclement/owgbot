// Package bot is the core: it pulls messages off the transport, applies rate
// limits, routes slash commands to plugins (and bare messages to the user's
// sticky plugin), and paces chunked replies onto the mesh.
package bot

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jclement/owgbot/internal/config"
	"github.com/jclement/owgbot/internal/plugin"
	"github.com/jclement/owgbot/internal/store"
	"github.com/jclement/owgbot/internal/transport"
)

// stickyNS is the core's own store namespace for per-user sticky plugins.
const stickyNS = "core"

type outMsg struct {
	to   string
	text string
}

// Bot wires transport, store, config and plugins together.
type Bot struct {
	tr      transport.Transport
	cfg     *config.Provider
	st      *store.Store
	log     *slog.Logger
	plugins []plugin.Plugin
	// commands maps command name → owning plugin.
	commands map[string]plugin.Plugin
	sticky   *store.KV
	limiter  *rateLimiter
	out      chan outMsg
	cancel   context.CancelFunc
	doneCh   chan struct{}
}

// New builds a bot. Plugins are initialized in order; disabled plugins are
// skipped entirely.
func New(tr transport.Transport, cfg *config.Provider, st *store.Store, log *slog.Logger, plugins ...plugin.Plugin) (*Bot, error) {
	b := &Bot{
		tr:       tr,
		cfg:      cfg,
		st:       st,
		log:      log,
		commands: make(map[string]plugin.Plugin),
		sticky:   st.Namespace(stickyNS),
		limiter:  newRateLimiter(),
		out:      make(chan outMsg, 256),
		doneCh:   make(chan struct{}),
	}
	for _, p := range plugins {
		if !cfg.Get().IsEnabled(p.Name()) {
			log.Info("plugin disabled", "plugin", p.Name())
			continue
		}
		env := plugin.Env{
			KV:          st.Namespace(p.Name()),
			Log:         log.With("plugin", p.Name()),
			SendTo:      b.QueueSend,
			Config:      cfg.Get,
			NodeName:    tr.NodeName,
			ResolveNode: tr.ResolveNode,
			Self:        tr.Self,
		}
		if err := p.Init(env); err != nil {
			return nil, err
		}
		b.plugins = append(b.plugins, p)
		for _, c := range p.Commands() {
			b.commands[c.Name] = p
		}
	}
	return b, nil
}

// Plugins returns the enabled plugins (for /plugins and /help).
func (b *Bot) Plugins() []plugin.Plugin { return b.plugins }

// QueueSend queues an outbound message to a user; it is chunked and paced.
func (b *Bot) QueueSend(user, text string) {
	if text == "" {
		return
	}
	for _, chunk := range Chunk(text, b.cfg.Get().MaxMsgLen) {
		select {
		case b.out <- outMsg{to: user, text: chunk}:
		default:
			b.log.Warn("outbound queue full; dropping chunk", "to", user)
		}
	}
}

// Run processes messages until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) error {
	ctx, b.cancel = context.WithCancel(ctx)
	defer b.cancel()

	go b.sendLoop(ctx)
	go b.advertLoop(ctx)
	defer close(b.doneCh)
	defer b.stopPlugins()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-b.tr.Messages():
			if !ok {
				return nil
			}
			b.handle(ctx, msg)
		case prefix := <-b.tr.Adverts():
			for _, p := range b.plugins {
				if ah, ok := p.(plugin.AdvertHandler); ok {
					ah.HandleAdvert(prefix)
				}
			}
		}
	}
}

// Flush waits (bounded) for the outbound queue to drain — used before
// restarting for a self-update so the "updating..." reply gets out.
func (b *Bot) Flush(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for len(b.out) > 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	// One extra interval for the in-flight message.
	time.Sleep(time.Duration(b.cfg.Get().SendIntervalMS) * time.Millisecond)
}

func (b *Bot) stopPlugins() {
	for _, p := range b.plugins {
		if s, ok := p.(plugin.Stopper); ok {
			s.Stop()
		}
	}
}

// advertLoop broadcasts a periodic self-advert so the mesh knows the bot
// exists. The last-sent time is persisted, so restart loops can't spam.
func (b *Bot) advertLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		b.maybeAdvert(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (b *Bot) maybeAdvert(ctx context.Context) {
	interval := b.cfg.Get().AdvertEvery()
	if interval == 0 {
		return
	}
	var last time.Time
	if v, err := b.sticky.Get("", "last_advert"); err == nil {
		if ts, perr := strconv.ParseInt(v, 10, 64); perr == nil {
			last = time.Unix(ts, 0)
		}
	}
	if time.Since(last) < interval {
		return
	}
	if err := b.SendAdvertNow(ctx); err != nil {
		b.log.Warn("periodic advert failed", "err", err)
	}
}

// SendAdvertNow broadcasts a flood self-advert and resets the periodic timer.
func (b *Bot) SendAdvertNow(ctx context.Context) error {
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := b.tr.SendAdvert(sctx, true); err != nil {
		return err
	}
	b.log.Info("self-advert sent")
	return b.sticky.Set("", "last_advert", strconv.FormatInt(time.Now().Unix(), 10))
}

// sendLoop paces outbound messages so the bot never floods LoRa airtime.
func (b *Bot) sendLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-b.out:
			sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := b.tr.Send(sctx, m.to, m.text)
			cancel()
			if err != nil {
				b.log.Warn("send failed", "to", m.to, "err", err)
			} else {
				b.log.Info("sent", "to", m.to, "bytes", len(m.text))
			}
			interval := time.Duration(b.cfg.Get().SendIntervalMS) * time.Millisecond
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
		}
	}
}

func (b *Bot) handle(ctx context.Context, msg transport.Message) {
	cfg := b.cfg.Get()
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}
	admin := cfg.IsAdmin(msg.From)
	if !admin {
		ok, firstReject := b.limiter.allow(msg.From, cfg.RateLimit.PerMinute, cfg.RateLimit.Burst)
		if !ok {
			if firstReject {
				b.QueueSend(msg.From, "slow down — rate limited, try again in a minute")
			}
			b.log.Debug("rate limited", "from", msg.From)
			return
		}
	}

	pctx := &plugin.Ctx{
		Context: ctx,
		User:    msg.From,
		Admin:   admin,
		SNR:     msg.SNR,
		Hops:    msg.Hops,
		Reply:   func(t string) { b.QueueSend(msg.From, t) },
		Config:  cfg,
	}

	b.log.Info("message", "from", msg.From, "text", text)

	// Any message is proof of life — let interested plugins react (deliver
	// queued mail, refresh last-seen, ...).
	for _, p := range b.plugins {
		if ah, ok := p.(plugin.ActivityHandler); ok {
			ah.HandleActivity(msg.From)
		}
	}

	if strings.HasPrefix(text, "/") {
		b.handleCommand(pctx, text)
		return
	}
	b.handleBare(pctx, text)
}

func (b *Bot) handleCommand(pctx *plugin.Ctx, text string) {
	name, args, _ := strings.Cut(strings.TrimPrefix(text, "/"), " ")
	name = strings.ToLower(name)
	args = strings.TrimSpace(args)

	p, ok := b.commands[name]
	if ok {
		cmd := findCommand(p, name)
		if cmd.Admin && !pctx.Admin {
			ok = false // admins-only commands don't exist for anyone else
		}
	}
	if !ok {
		pctx.Reply("unknown command — try /help")
		return
	}
	if err := p.HandleCommand(pctx, name, args); err != nil {
		b.log.Error("command failed", "cmd", name, "from", pctx.User, "err", err)
		pctx.Reply("error: " + err.Error())
		return
	}
	if pctx.SessionEnded() {
		if err := b.sticky.Delete(pctx.User, "sticky"); err != nil {
			b.log.Warn("clearing sticky plugin failed", "err", err)
		}
		return
	}
	// Session-capable plugins become sticky so bare replies route to them.
	if _, isSession := p.(plugin.SessionHandler); isSession {
		if err := b.sticky.Set(pctx.User, "sticky", p.Name()); err != nil {
			b.log.Warn("saving sticky plugin failed", "err", err)
		}
	}
}

func (b *Bot) handleBare(pctx *plugin.Ctx, text string) {
	name, err := b.sticky.Get(pctx.User, "sticky")
	if err == nil {
		for _, p := range b.plugins {
			sh, ok := p.(plugin.SessionHandler)
			if !ok || p.Name() != name {
				continue
			}
			handled, err := sh.HandleSession(pctx, text)
			if err != nil {
				b.log.Error("session handler failed", "plugin", name, "err", err)
				pctx.Reply("error: " + err.Error())
				return
			}
			if pctx.SessionEnded() {
				if derr := b.sticky.Delete(pctx.User, "sticky"); derr != nil {
					b.log.Warn("clearing sticky plugin failed", "err", derr)
				}
			}
			if handled {
				return
			}
			break
		}
	}
	pctx.Reply("hi! I'm owgbot — try /help")
}

func findCommand(p plugin.Plugin, name string) plugin.Command {
	for _, c := range p.Commands() {
		if c.Name == name {
			return c
		}
	}
	return plugin.Command{}
}
