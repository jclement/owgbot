// Package config loads /etc/owgbot/config.yml and live-reloads it on change.
// Readers always call Provider.Get() for the current snapshot; a reload
// atomically swaps the snapshot, so in-flight handlers keep a consistent view.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/fsnotify/fsnotify"
	"gopkg.in/yaml.v3"
)

// Config is one immutable snapshot of the bot configuration.
type Config struct {
	// SerialPort is the companion radio device (change requires restart).
	SerialPort string `yaml:"serial_port"`
	Baud       int    `yaml:"baud"`
	// DataDir holds the SQLite database (change requires restart).
	DataDir string `yaml:"data_dir"`

	// MaxMsgLen is the per-message byte budget for outbound chunks.
	MaxMsgLen int `yaml:"max_msg_len"`
	// SendIntervalMS is the minimum gap between outbound messages (LoRa
	// airtime is shared — don't flood the mesh).
	SendIntervalMS int `yaml:"send_interval_ms"`

	// Admins lists node public-key prefixes (12 hex chars) allowed to run
	// admin commands.
	Admins []string `yaml:"admins"`

	RateLimit RateLimit `yaml:"rate_limit"`

	// GithubRepo ("owner/name") is where /update looks for release builds.
	GithubRepo string `yaml:"github_repo"`

	// Plugins holds per-plugin settings, keyed by plugin name.
	Plugins map[string]PluginConfig `yaml:"plugins"`
}

// RateLimit configures the per-user token bucket.
type RateLimit struct {
	PerMinute int `yaml:"per_minute"`
	Burst     int `yaml:"burst"`
}

// PluginConfig is one plugin's settings.
type PluginConfig struct {
	Enabled  *bool             `yaml:"enabled"` // nil = enabled
	Settings map[string]string `yaml:"settings"`
}

// IsEnabled reports whether the plugin named name is enabled.
func (c *Config) IsEnabled(name string) bool {
	pc, ok := c.Plugins[name]
	return !ok || pc.Enabled == nil || *pc.Enabled
}

// Setting returns a plugin setting or def if unset.
func (c *Config) Setting(plugin, key, def string) string {
	if v, ok := c.Plugins[plugin].Settings[key]; ok {
		return v
	}
	return def
}

// IsAdmin reports whether the user (pubkey prefix) is an admin.
func (c *Config) IsAdmin(user string) bool {
	for _, a := range c.Admins {
		if a == user {
			return true
		}
	}
	return false
}

// Default returns the built-in defaults (used by dev mode and as the base
// every loaded file is merged over).
func Default() Config {
	return Config{
		SerialPort:     "/dev/ttyUSB0",
		Baud:           115200,
		DataDir:        "/var/lib/owgbot",
		MaxMsgLen:      140,
		SendIntervalMS: 2000,
		RateLimit:      RateLimit{PerMinute: 6, Burst: 3},
		GithubRepo:     "jclement/owgbot",
	}
}

// Provider hands out the current Config snapshot and watches for changes.
type Provider struct {
	cur      atomic.Pointer[Config]
	log      *slog.Logger
	override func(*Config)
}

// Static wraps a fixed Config (dev mode, tests).
func Static(c Config) *Provider {
	p := &Provider{log: slog.Default()}
	p.cur.Store(&c)
	return p
}

// Load reads path and starts watching it for changes. override (may be nil)
// is applied after every parse — including live reloads — so CLI flags and
// environment variables keep precedence over the file for the process's
// lifetime.
func Load(path string, log *slog.Logger, override func(*Config)) (*Provider, error) {
	p := &Provider{log: log.With("component", "config"), override: override}
	c, err := p.parseFile(path)
	if err != nil {
		return nil, err
	}
	p.cur.Store(&c)
	if err := p.watch(path); err != nil {
		p.log.Warn("config watch unavailable; live reload disabled", "err", err)
	}
	return p, nil
}

// Get returns the current snapshot. Never nil.
func (p *Provider) Get() *Config { return p.cur.Load() }

func (p *Provider) parseFile(path string) (Config, error) {
	c := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		return c, fmt.Errorf("config: %w", err)
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if p.override != nil {
		p.override(&c)
	}
	if c.MaxMsgLen < 20 || c.MaxMsgLen > 160 {
		return c, fmt.Errorf("config: max_msg_len must be 20..160 (got %d)", c.MaxMsgLen)
	}
	return c, nil
}

// watch reloads the config when the file changes. Editors and provisioning
// tools typically replace the file (rename), so watch the directory.
func (p *Provider) watch(path string) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := w.Add(dir); err != nil {
		w.Close()
		return err
	}
	base := filepath.Base(path)
	go func() {
		for {
			select {
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if filepath.Base(ev.Name) != base {
					continue
				}
				if !ev.Has(fsnotify.Write) && !ev.Has(fsnotify.Create) && !ev.Has(fsnotify.Rename) {
					continue
				}
				c, err := p.parseFile(path)
				if err != nil {
					p.log.Error("config reload failed; keeping previous config", "err", err)
					continue
				}
				p.cur.Store(&c)
				p.log.Info("config reloaded")
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				p.log.Warn("config watcher error", "err", err)
			}
		}
	}()
	return nil
}
