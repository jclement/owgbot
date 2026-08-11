// Package admin implements admin-only commands: /plugins and /update.
// Admins are node pubkey prefixes listed in config; for everyone else these
// commands don't exist (the dispatcher answers "unknown command").
package admin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
// ChannelWatcher manages the channel watch list (implemented by the bot).
type ChannelWatcher interface {
	WatchedChannels() []int
	Watch(ch int) error
	Unwatch(ch int) error
	// ProvisionChannel writes a channel (name + 16-byte secret) into a
	// radio slot so the bot can hear it at all.
	ProvisionChannel(ch int, name string, secret []byte) error
}

type Plugin struct {
	env     plugin.Env
	plugins func() []plugin.Plugin
	restart func()
	advert  func(ctx context.Context) error
	watcher func() ChannelWatcher
	stop    chan struct{}
	once    sync.Once
}

func New(plugins func() []plugin.Plugin, restart func(), advert func(ctx context.Context) error, watcher func() ChannelWatcher) *Plugin {
	return &Plugin{plugins: plugins, restart: restart, advert: advert, watcher: watcher, stop: make(chan struct{})}
}

func (p *Plugin) Name() string { return "admin" }

func (p *Plugin) Commands() []plugin.Command {
	return []plugin.Command{
		{Name: "plugins", Help: "list plugins", Admin: true},
		{Name: "update", Help: "self-update from github", Admin: true},
		{Name: "advert", Help: "broadcast a self-advert now", Admin: true},
		{Name: "watch", Args: "[#ch | -#ch | slot name key]", Help: "list/watch/unwatch/add channels", Admin: true},
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
	case "watch":
		return p.watch(ctx, args)
	}
	return nil
}

// watch manages the channel watch list: bare = list, "#name"/"N" = watch,
// "-#name"/"-N" = unwatch, "slot name key" = provision a channel onto the
// radio (key = 16 bytes as base64 or 32-char hex, from the app's channel
// share) and start watching it.
func (p *Plugin) watch(ctx *plugin.Ctx, args string) error {
	w := p.watcher()
	args = strings.TrimSpace(args)
	if args == "" {
		ctx.Reply(p.watchList(w))
		return nil
	}
	if fields := strings.Fields(args); len(fields) == 3 {
		return p.provision(ctx, w, fields)
	}
	remove := strings.HasPrefix(args, "-")
	hashtag := strings.HasPrefix(strings.TrimPrefix(args, "-"), "#")
	spec := strings.TrimPrefix(strings.TrimPrefix(args, "-"), "#")
	slot, ok := p.resolveChannel(spec)
	if !ok && !remove && hashtag {
		// Hashtag channels are keyless-joinable: the secret is derived
		// from the name, so we can provision it ourselves.
		return p.joinHashtag(ctx, w, spec)
	}
	if !ok {
		ctx.Reply("the radio has no channel " + args + " — /watch lists slots, /watch #name joins a hashtag channel, /watch <slot> <name> <key> adds a private one")
		return nil
	}
	var err error
	if remove {
		err = w.Unwatch(slot)
	} else {
		err = w.Watch(slot)
	}
	if err != nil {
		return err
	}
	ctx.Reply(p.watchList(w))
	return nil
}

func (p *Plugin) watchList(w ChannelWatcher) string {
	watched := make(map[int]bool)
	for _, ch := range w.WatchedChannels() {
		watched[ch] = true
	}
	var on, off []string
	for slot := 0; slot < 8; slot++ {
		name := p.env.ChannelName(slot)
		if name == "" && !watched[slot] {
			continue // unconfigured slot
		}
		label := fmt.Sprintf("%d", slot)
		if name != "" {
			label = fmt.Sprintf("%d #%s", slot, strings.TrimPrefix(name, "#"))
		}
		if watched[slot] {
			on = append(on, label)
		} else {
			off = append(off, label)
		}
	}
	s := "watching: "
	if len(on) == 0 {
		s += "nothing"
	} else {
		s += strings.Join(on, ", ")
	}
	if len(off) > 0 {
		s += "\nother slots: " + strings.Join(off, ", ")
	}
	return s
}

// joinHashtag provisions a public hashtag channel: the 16-byte secret is
// the first half of SHA256 of the "#name" string (MeshCore convention), so
// knowing the name IS knowing the key.
func (p *Plugin) joinHashtag(ctx *plugin.Ctx, w ChannelWatcher, name string) error {
	full := "#" + strings.ToLower(name)
	slot := -1
	for s := 0; s < 8; s++ {
		if p.env.ChannelName(s) == "" {
			slot = s
			break
		}
	}
	if slot < 0 {
		ctx.Reply("all 8 channel slots are in use — nothing free for " + full)
		return nil
	}
	if err := w.ProvisionChannel(slot, full, hashtagKey(full)); err != nil {
		ctx.Reply("radio refused the channel: " + err.Error())
		return nil
	}
	if err := w.Watch(slot); err != nil {
		return err
	}
	p.env.Log.Info("hashtag channel joined", "slot", slot, "name", full)
	ctx.Reply("joined " + full + " (slot " + strconv.Itoa(slot) + ")\n" + p.watchList(w))
	return nil
}

// hashtagKey derives a hashtag channel's secret: first 16 bytes of
// SHA256 of the full "#name" string.
func hashtagKey(fullName string) []byte {
	sum := sha256.Sum256([]byte(fullName))
	return sum[:16]
}

// provision writes a channel into a radio slot and starts watching it.
func (p *Plugin) provision(ctx *plugin.Ctx, w ChannelWatcher, fields []string) error {
	slot, err := strconv.Atoi(fields[0])
	if err != nil || slot < 0 || slot > 7 {
		ctx.Reply("slot must be 0-7: /watch <slot> <name> <key>")
		return nil
	}
	name := strings.TrimPrefix(fields[1], "#")
	secret, err := parseChannelKey(fields[2])
	if err != nil {
		ctx.Reply("bad key — want 16 bytes as base64 or 32 hex chars (from the app's channel share)")
		return nil
	}
	if err := w.ProvisionChannel(slot, name, secret); err != nil {
		ctx.Reply("radio refused the channel: " + err.Error())
		return nil
	}
	if err := w.Watch(slot); err != nil {
		return err
	}
	p.env.Log.Info("channel provisioned", "slot", slot, "name", name)
	ctx.Reply(p.watchList(w))
	return nil
}

// parseChannelKey accepts a 16-byte channel secret as base64 (std or URL,
// padded or not) or 32 hex chars.
func parseChannelKey(s string) ([]byte, error) {
	if b, err := hex.DecodeString(s); err == nil && len(b) == 16 {
		return b, nil
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == 16 {
			return b, nil
		}
	}
	return nil, fmt.Errorf("not a 16-byte key")
}

// resolveChannel turns a slot number or channel name into a slot index.
// Names match with or without their leading '#'.
func (p *Plugin) resolveChannel(spec string) (int, bool) {
	if n, err := strconv.Atoi(spec); err == nil {
		if n >= 0 && n <= 7 {
			return n, true
		}
		return 0, false
	}
	want := strings.TrimPrefix(spec, "#")
	for slot := 0; slot < 8; slot++ {
		name := strings.TrimPrefix(p.env.ChannelName(slot), "#")
		if name != "" && strings.EqualFold(name, want) {
			return slot, true
		}
	}
	return 0, false
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
