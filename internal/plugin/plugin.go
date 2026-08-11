// Package plugin defines the contract every owgbot plugin implements.
//
// A plugin is one self-contained Go package that registers commands and
// (optionally) handles session follow-ups. Adding a feature to the bot means
// writing one plugin and listing it in main.go — nothing else changes.
package plugin

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jclement/owgbot/internal/config"
	"github.com/jclement/owgbot/internal/store"
	"github.com/jclement/owgbot/internal/transport"
)

// Command describes one slash command a plugin owns.
type Command struct {
	// Name is the command without the slash, e.g. "w" for /w.
	Name string
	// Args is a short usage hint for /help, e.g. "[location]".
	Args string
	// Help is a one-line description, kept terse — it travels over LoRa.
	Help string
	// Admin restricts the command to configured admin node keys. Admin
	// commands are hidden from /help and answer "unknown command" to
	// everyone else.
	Admin bool
	// Hidden keeps a command out of /help while leaving it usable by
	// anyone — for easter eggs best discovered the hard way.
	Hidden bool
	// Category groups the command in the /help menu: "games", "mesh",
	// or "tools" (the default when empty).
	Category string
}

// Ctx is the per-message context handed to plugin handlers.
type Ctx struct {
	// Context carries cancellation from the bot's lifecycle.
	Context context.Context
	// User is the sender's node pubkey prefix (12 hex chars).
	User string
	// Admin reports whether the sender is a configured admin.
	Admin bool
	// SNR is the received signal-to-noise ratio in dB (0 when the radio
	// didn't report one). Note it describes the final hop into the bot —
	// meaningful for your own link only when Hops == 0.
	SNR float64
	// Hops is how many repeater hops the message travelled (0 = direct).
	Hops int
	// Reply queues a reply to the sender. Long text is chunked and paced
	// automatically; keep replies terse anyway — every chunk is airtime.
	Reply func(text string)
	// Config is the current config snapshot.
	Config *config.Config

	endSession bool
}

// EndSession releases this plugin's sticky claim on the user: bare messages
// go back to the default hint until another session-style command runs.
func (c *Ctx) EndSession() { c.endSession = true }

// SessionEnded reports whether the handler called EndSession (dispatcher use).
func (c *Ctx) SessionEnded() bool { return c.endSession }

// Env is what a plugin gets at startup.
type Env struct {
	// KV is the plugin's private namespaced store.
	KV *store.KV
	// Log is a logger tagged with the plugin name.
	Log *slog.Logger
	// SendTo queues an unsolicited message to a node (reminders etc.),
	// subject to the same chunking and pacing as replies.
	SendTo func(user, text string)
	// Config returns the current config snapshot.
	Config func() *config.Config
	// NodeName returns the advertised name for a node prefix, or "" if the
	// radio's contact list doesn't know it.
	NodeName func(user string) string
	// ResolveNode turns a node name or 12-hex prefix into a prefix.
	ResolveNode func(nameOrPrefix string) (string, bool)
	// Self reports the radio node the bot is running as.
	Self func() transport.SelfInfo
}

// Plugin is one bot feature.
type Plugin interface {
	// Name is the stable identifier (store namespace, config key, /plugins).
	Name() string
	// Commands lists the slash commands this plugin serves.
	Commands() []Command
	// Init is called once at startup, before any messages flow.
	Init(env Env) error
	// HandleCommand runs a slash command. cmd is the bare command name;
	// args is the untrimmed remainder (may be "").
	HandleCommand(ctx *Ctx, cmd string, args string) error
}

// SessionHandler is implemented by plugins with follow-up interactions
// (e.g. gem's numbered links). After such a plugin handles a command, it
// becomes "sticky" for that user: bare (non-slash) messages route to
// HandleSession until another sticky plugin takes over.
type SessionHandler interface {
	Plugin
	// HandleSession receives a bare message from a user this plugin is
	// sticky for. Return handled=false to fall through to the default
	// "unknown command" hint.
	HandleSession(ctx *Ctx, text string) (handled bool, err error)
}

// AdvertHandler is implemented by plugins that want to know when a node
// advertises itself on the mesh (it's nearby and awake).
type AdvertHandler interface {
	Plugin
	HandleAdvert(user string)
}

// ActivityHandler is implemented by plugins that want to know when a user
// sends the bot any message (e.g. to deliver queued mail).
type ActivityHandler interface {
	Plugin
	HandleActivity(user string)
}

// Stopper is implemented by plugins with background work to shut down.
type Stopper interface {
	Stop()
}

// AgoPhrase renders "just now", "14m ago", "3h ago".
func AgoPhrase(t time.Time) string {
	if a := Ago(t); a != "now" {
		return a + " ago"
	}
	return "just now"
}

// Ago renders a duration since t tersely: "14m", "3h", "2d".
func Ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
