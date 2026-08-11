// Package admin implements admin-only commands: /plugins and /update.
// Admins are node pubkey prefixes listed in config; for everyone else these
// commands don't exist (the dispatcher answers "unknown command").
package admin

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

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
	stop    chan struct{}
	once    sync.Once
}

func New(plugins func() []plugin.Plugin, restart func(), advert func(ctx context.Context) error) *Plugin {
	return &Plugin{plugins: plugins, restart: restart, advert: advert, stop: make(chan struct{})}
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
	go p.updateLoop()
	return nil
}

func (p *Plugin) Stop() { p.once.Do(func() { close(p.stop) }) }

// checkRelease is swapped out in tests.
var checkRelease = selfupdate.Check

const updateTick = 10 * time.Minute

// updateLoop periodically looks for a newer release and tells the admins —
// once per new tag. Installing remains an explicit /update.
func (p *Plugin) updateLoop() {
	t := time.NewTicker(updateTick)
	defer t.Stop()
	for {
		p.maybeCheckUpdates()
		select {
		case <-p.stop:
			return
		case <-t.C:
		}
	}
}

func (p *Plugin) maybeCheckUpdates() {
	cfg := p.env.Config()
	interval := cfg.UpdateCheckEvery()
	if interval == 0 || len(cfg.Admins) == 0 {
		return
	}
	if v, err := p.env.KV.Get("", "last_check"); err == nil {
		if ts, perr := strconv.ParseInt(v, 10, 64); perr == nil && time.Since(time.Unix(ts, 0)) < interval {
			return
		}
	}
	// Record the attempt up front so API errors don't turn into hammering.
	p.env.KV.Set("", "last_check", strconv.FormatInt(time.Now().Unix(), 10))

	rel, err := checkRelease(cfg.GithubRepo)
	if err != nil {
		p.env.Log.Debug("update check failed", "err", err)
		return
	}
	if rel.Tag == "" || rel.Tag == version.Tag {
		return
	}
	if notified, err := p.env.KV.Get("", "notified_tag"); err == nil && notified == rel.Tag {
		return
	}
	cur := version.Tag
	if cur == "" {
		cur = "dev " + version.String()
	}
	p.env.Log.Info("newer release available", "tag", rel.Tag)
	for _, a := range cfg.Admins {
		p.env.SendTo(a, fmt.Sprintf("%s is available (running %s) — /update to install", rel.Tag, cur))
	}
	p.env.KV.Set("", "notified_tag", rel.Tag)
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
