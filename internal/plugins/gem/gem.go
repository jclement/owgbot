// Package gem implements /gem — a gemini browser over LoRa.
//
//	/gem owg.fyi     load a page (chunked; links numbered)
//	1                follow link [1]
//	n                next page of the current document
//	b                back to the previous page
//
// Sessions are in-memory per user: page text is paginated into reply-sized
// blocks so a long document never floods the mesh unasked.
package gem

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/jclement/owgbot/internal/plugin"
)

// pageBudget is roughly how many bytes of document each reply carries
// (the bot core further chunks a reply into radio-sized messages).
const defaultPageBudget = 350

type session struct {
	url     string
	pages   []string
	pageIdx int
	links   []string
	history []string // URLs, most recent last (current not included)
}

type Plugin struct {
	env plugin.Env

	mu       sync.Mutex
	sessions map[string]*session
}

func New() *Plugin { return &Plugin{sessions: make(map[string]*session)} }

func (p *Plugin) Name() string { return "gem" }

func (p *Plugin) Commands() []plugin.Command {
	return []plugin.Command{{
		Name: "gem", Args: "<url>",
		Help: "browse gemini (1=link n=next b=back)",
	}}
}

func (p *Plugin) Init(env plugin.Env) error {
	p.env = env
	return nil
}

func (p *Plugin) HandleCommand(ctx *plugin.Ctx, cmd, args string) error {
	if strings.TrimSpace(args) == "" {
		ctx.Reply("usage: /gem <url> — then 1..9 to follow links, n=next, b=back")
		return nil
	}
	return p.browse(ctx, args, true)
}

func (p *Plugin) HandleSession(ctx *plugin.Ctx, text string) (bool, error) {
	s := p.get(ctx.User)
	if s == nil {
		return false, nil
	}
	text = strings.TrimSpace(strings.ToLower(text))
	switch text {
	case "q", "quit":
		ctx.EndSession()
		ctx.Reply("left the browser")
		return true, nil
	case "n":
		p.mu.Lock()
		if s.pageIdx+1 < len(s.pages) {
			s.pageIdx++
		}
		reply := p.renderPageLocked(s)
		p.mu.Unlock()
		ctx.Reply(reply)
		return true, nil
	case "b":
		p.mu.Lock()
		if len(s.history) == 0 {
			p.mu.Unlock()
			ctx.Reply("no history")
			return true, nil
		}
		prev := s.history[len(s.history)-1]
		s.history = s.history[:len(s.history)-1]
		p.mu.Unlock()
		return true, p.browse(ctx, prev, false)
	}
	if n, err := strconv.Atoi(text); err == nil {
		p.mu.Lock()
		var target string
		if n >= 1 && n <= len(s.links) {
			target = s.links[n-1]
		}
		p.mu.Unlock()
		if target == "" {
			ctx.Reply(fmt.Sprintf("no link %d on this page", n))
			return true, nil
		}
		if !strings.HasPrefix(target, "gemini://") {
			ctx.Reply("non-gemini link: " + target)
			return true, nil
		}
		return true, p.browse(ctx, target, true)
	}
	return false, nil
}

// browse fetches a URL and replies with its first page. push records the
// current URL in history (false when navigating back).
func (p *Plugin) browse(ctx *plugin.Ctx, rawURL string, push bool) error {
	finalURL, body, err := fetch(rawURL)
	if err != nil {
		ctx.Reply("gem: " + err.Error())
		return nil
	}
	text, links := render(finalURL, body)
	if text == "" {
		text = "(empty page)"
	}
	budget := defaultPageBudget
	if v := ctx.Config.Setting(p.Name(), "page_bytes", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 100 {
			budget = n
		}
	}
	pages := paginate(text, budget)

	p.mu.Lock()
	s := p.sessions[ctx.User]
	if s == nil {
		s = &session{}
		p.sessions[ctx.User] = s
	}
	if push && s.url != "" && s.url != finalURL {
		s.history = append(s.history, s.url)
		if len(s.history) > 20 {
			s.history = s.history[1:]
		}
	}
	s.url = finalURL
	s.pages = pages
	s.pageIdx = 0
	s.links = links
	reply := p.renderPageLocked(s)
	p.mu.Unlock()

	ctx.Reply(reply)
	return nil
}

// renderPageLocked formats the current page plus a nav hint. Caller holds mu.
func (p *Plugin) renderPageLocked(s *session) string {
	page := s.pages[s.pageIdx]
	if len(s.pages) > 1 {
		hint := fmt.Sprintf("pg %d/%d", s.pageIdx+1, len(s.pages))
		if s.pageIdx+1 < len(s.pages) {
			hint += " n=more"
		}
		return page + "\n(" + hint + ")"
	}
	return page
}

func (p *Plugin) get(user string) *session {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessions[user]
}

// paginate splits rendered text into page-sized blocks on line boundaries.
func paginate(text string, budget int) []string {
	lines := strings.Split(text, "\n")
	var pages []string
	var cur strings.Builder
	for _, line := range lines {
		// A single line longer than the budget gets hard-wrapped.
		for len(line) > budget {
			if cur.Len() > 0 {
				pages = append(pages, strings.TrimRight(cur.String(), "\n"))
				cur.Reset()
			}
			pages = append(pages, line[:budget])
			line = line[budget:]
		}
		if cur.Len()+len(line)+1 > budget && cur.Len() > 0 {
			pages = append(pages, strings.TrimRight(cur.String(), "\n"))
			cur.Reset()
		}
		cur.WriteString(line)
		cur.WriteString("\n")
	}
	if strings.TrimSpace(cur.String()) != "" {
		pages = append(pages, strings.TrimRight(cur.String(), "\n"))
	}
	if len(pages) == 0 {
		pages = []string{""}
	}
	return pages
}
