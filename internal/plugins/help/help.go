// Package help implements /help as a BBS-style menu: one terse chunk listing
// categories, then a number reply drills into one. The full firehose stays
// available as /help all. Every command remains directly invocable — the
// menu is discovery only, priced for LoRa.
package help

import (
	"fmt"
	"strings"

	"github.com/jclement/owgbot/internal/plugin"
)

// categories in menu order. Commands with an empty Category land in "tools";
// admin commands (visible to admins only) are listed under "admin".
var categories = []struct{ key, label string }{
	{"games", "games"},
	{"mesh", "mesh"},
	{"tools", "tools"},
	{"admin", "admin"},
}

// Plugin lists every visible command. It gets the plugin roster lazily so it
// can be constructed before the bot core exists.
type Plugin struct {
	plugins func() []plugin.Plugin
}

func New(plugins func() []plugin.Plugin) *Plugin {
	return &Plugin{plugins: plugins}
}

func (p *Plugin) Name() string { return "help" }

func (p *Plugin) Commands() []plugin.Command {
	return []plugin.Command{{Name: "help", Args: "[all]", Help: "this menu"}}
}

func (p *Plugin) Init(env plugin.Env) error { return nil }

func (p *Plugin) HandleCommand(ctx *plugin.Ctx, cmd, args string) error {
	switch args = strings.ToLower(strings.TrimSpace(args)); {
	case args == "":
		ctx.Reply(p.menu(ctx))
	case args == "all":
		ctx.EndSession()
		ctx.Reply(p.listAll(ctx))
	default:
		if !p.replyCategory(ctx, args) {
			ctx.Reply(p.menu(ctx))
		}
	}
	return nil
}

// HandleSession makes the number replies work right after /help. Answering
// ends the menu session so a game or browser session isn't held hostage.
func (p *Plugin) HandleSession(ctx *plugin.Ctx, text string) (bool, error) {
	if p.replyCategory(ctx, strings.ToLower(strings.TrimSpace(text))) {
		ctx.EndSession()
		return true, nil
	}
	ctx.EndSession()
	return false, nil // not a menu choice — release the session and fall through
}

// menu renders the one-chunk category index.
func (p *Plugin) menu(ctx *plugin.Ctx) string {
	byCat := p.visible(ctx)
	var parts []string
	n := 0
	for _, c := range categories {
		if len(byCat[c.key]) == 0 {
			continue
		}
		n++
		parts = append(parts, fmt.Sprintf("%d=%s", n, c.label))
	}
	return "owgbot: " + strings.Join(parts, " ") + " — send a number, or /help all"
}

// replyCategory answers a menu choice by number ("2") or name ("mesh").
func (p *Plugin) replyCategory(ctx *plugin.Ctx, choice string) bool {
	byCat := p.visible(ctx)
	n := 0
	for _, c := range categories {
		cmds := byCat[c.key]
		if len(cmds) == 0 {
			continue
		}
		n++
		if choice == fmt.Sprint(n) || choice == c.key {
			ctx.Reply(strings.TrimRight(render(cmds), "\n"))
			return true
		}
	}
	return false
}

func (p *Plugin) listAll(ctx *plugin.Ctx) string {
	byCat := p.visible(ctx)
	var b strings.Builder
	for _, c := range categories {
		b.WriteString(render(byCat[c.key]))
	}
	return strings.TrimRight(b.String(), "\n")
}

// visible buckets the roster's commands by category for this viewer.
func (p *Plugin) visible(ctx *plugin.Ctx) map[string][]plugin.Command {
	byCat := make(map[string][]plugin.Command)
	for _, pl := range p.plugins() {
		for _, c := range pl.Commands() {
			if c.Hidden || (c.Admin && !ctx.Admin) {
				continue
			}
			key := c.Category
			if c.Admin {
				key = "admin"
			} else if key == "" {
				key = "tools"
			}
			byCat[key] = append(byCat[key], c)
		}
	}
	return byCat
}

func render(cmds []plugin.Command) string {
	var b strings.Builder
	for _, c := range cmds {
		b.WriteString("/")
		b.WriteString(c.Name)
		if c.Args != "" {
			b.WriteString(" ")
			b.WriteString(c.Args)
		}
		if c.Help != "" {
			b.WriteString(" - ")
			b.WriteString(c.Help)
		}
		b.WriteString("\n")
	}
	return b.String()
}
