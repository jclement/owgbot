// Package remind implements /remind — durable reminders delivered as DMs.
//
//	/remind +5d Buy milk    schedule
//	/remind                 list pending
//	/remind del 3           cancel
//
// Reminders are stored as JSON rows in the plugin's KV namespace and survive
// restarts. Delivery is best-effort: the mesh node may be offline, so sends
// that fail are retried on a backoff for a bounded number of attempts.
package remind

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jclement/owgbot/internal/plugin"
)

const (
	tickInterval = 15 * time.Second
	maxAttempts  = 8
	retryBase    = time.Minute // backoff: 1m, 2m, 4m, ... capped
	retryMax     = 30 * time.Minute
	maxPerUser   = 20
)

type reminder struct {
	ID       int64     `json:"id"`
	Text     string    `json:"text"`
	Due      time.Time `json:"due"`
	Attempts int       `json:"attempts"`
	NextTry  time.Time `json:"next_try"`
}

type Plugin struct {
	env  plugin.Env
	stop chan struct{}
	once sync.Once
}

func New() *Plugin { return &Plugin{stop: make(chan struct{})} }

func (p *Plugin) Name() string { return "remind" }

func (p *Plugin) Commands() []plugin.Command {
	return []plugin.Command{{
		Name: "remind", Args: "[+5d text | del N]",
		Help: "set/list/cancel reminders",
	}}
}

func (p *Plugin) Init(env plugin.Env) error {
	p.env = env
	go p.loop()
	return nil
}

func (p *Plugin) Stop() { p.once.Do(func() { close(p.stop) }) }

func (p *Plugin) HandleCommand(ctx *plugin.Ctx, cmd, args string) error {
	args = strings.TrimSpace(args)
	switch {
	case args == "":
		return p.list(ctx)
	case strings.HasPrefix(args, "del "):
		return p.del(ctx, strings.TrimSpace(strings.TrimPrefix(args, "del ")))
	case strings.HasPrefix(args, "+"):
		return p.add(ctx, args)
	default:
		ctx.Reply("usage: /remind +5d Buy milk | /remind | /remind del N")
		return nil
	}
}

func (p *Plugin) add(ctx *plugin.Ctx, args string) error {
	durStr, text, _ := strings.Cut(args[1:], " ")
	text = strings.TrimSpace(text)
	if text == "" {
		ctx.Reply("remind you of what? /remind +5d Buy milk")
		return nil
	}
	d, err := parseDuration(durStr)
	if err != nil {
		ctx.Reply("bad duration (try +30m, +2h, +5d, +1w)")
		return nil
	}
	existing, err := p.env.KV.ListUser(ctx.User, "r:")
	if err != nil {
		return err
	}
	if len(existing) >= maxPerUser {
		ctx.Reply(fmt.Sprintf("too many reminders (max %d) — /remind del N first", maxPerUser))
		return nil
	}
	due := time.Now().Add(d)
	r := reminder{ID: time.Now().UnixNano(), Text: text, Due: due, NextTry: due}
	if err := p.save(ctx.User, r); err != nil {
		return err
	}
	ctx.Reply(fmt.Sprintf("ok — %q on %s", text, due.Format("Jan 2 15:04")))
	return nil
}

func (p *Plugin) list(ctx *plugin.Ctx) error {
	rs, err := p.load(ctx.User)
	if err != nil {
		return err
	}
	if len(rs) == 0 {
		ctx.Reply("no reminders. /remind +5d Buy milk")
		return nil
	}
	var b strings.Builder
	for i, r := range rs {
		fmt.Fprintf(&b, "%d) %s - %s\n", i+1, r.Due.Format("Jan 2 15:04"), r.Text)
	}
	ctx.Reply(strings.TrimRight(b.String(), "\n"))
	return nil
}

func (p *Plugin) del(ctx *plugin.Ctx, nStr string) error {
	n, err := strconv.Atoi(nStr)
	if err != nil {
		ctx.Reply("usage: /remind del N (see /remind)")
		return nil
	}
	rs, err := p.load(ctx.User)
	if err != nil {
		return err
	}
	if n < 1 || n > len(rs) {
		ctx.Reply(fmt.Sprintf("no reminder %d (have %d)", n, len(rs)))
		return nil
	}
	r := rs[n-1]
	if err := p.env.KV.Delete(ctx.User, key(r.ID)); err != nil {
		return err
	}
	ctx.Reply(fmt.Sprintf("deleted: %s", r.Text))
	return nil
}

// loop delivers due reminders.
func (p *Plugin) loop() {
	t := time.NewTicker(tickInterval)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
			p.deliverDue()
		}
	}
}

func (p *Plugin) deliverDue() {
	entries, err := p.env.KV.List("r:")
	if err != nil {
		p.env.Log.Error("listing reminders failed", "err", err)
		return
	}
	now := time.Now()
	for _, e := range entries {
		var r reminder
		if err := json.Unmarshal([]byte(e.Value), &r); err != nil {
			p.env.Log.Warn("dropping corrupt reminder", "key", e.Key, "err", err)
			p.env.KV.Delete(e.User, e.Key)
			continue
		}
		if now.Before(r.NextTry) {
			continue
		}
		// SendTo queues into the paced outbound path; treat queueing as
		// delivery but keep a bounded retry schedule in case the process
		// dies before it drains, or the node is unreachable long-term.
		p.env.SendTo(e.User, "reminder: "+r.Text)
		r.Attempts++
		if r.Attempts >= maxAttempts {
			p.env.KV.Delete(e.User, e.Key)
			continue
		}
		backoff := retryBase << (r.Attempts - 1)
		if backoff > retryMax {
			backoff = retryMax
		}
		r.NextTry = now.Add(backoff)
		if err := p.save(e.User, r); err != nil {
			p.env.Log.Error("updating reminder failed", "err", err)
		}
	}
}

func (p *Plugin) save(user string, r reminder) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return p.env.KV.Set(user, key(r.ID), string(b))
}

func (p *Plugin) load(user string) ([]reminder, error) {
	entries, err := p.env.KV.ListUser(user, "r:")
	if err != nil {
		return nil, err
	}
	rs := make([]reminder, 0, len(entries))
	for _, e := range entries {
		var r reminder
		if err := json.Unmarshal([]byte(e.Value), &r); err != nil {
			continue
		}
		rs = append(rs, r)
	}
	// KV order is by key (ID = creation nanos), i.e. creation order; sort by
	// due time for display.
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && rs[j].Due.Before(rs[j-1].Due); j-- {
			rs[j], rs[j-1] = rs[j-1], rs[j]
		}
	}
	return rs, nil
}

func key(id int64) string { return fmt.Sprintf("r:%020d", id) }

// parseDuration parses "5d", "2h30m", "1w", "90m" (the leading + is stripped
// by the caller). Days and weeks are added on top of Go's h/m/s units.
func parseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	var total time.Duration
	num := ""
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			num += string(c)
		case c == 'w' || c == 'd' || c == 'h' || c == 'm' || c == 's':
			if num == "" {
				return 0, fmt.Errorf("bad duration %q", s)
			}
			n, err := strconv.Atoi(num)
			if err != nil {
				return 0, err
			}
			switch c {
			case 'w':
				total += time.Duration(n) * 7 * 24 * time.Hour
			case 'd':
				total += time.Duration(n) * 24 * time.Hour
			case 'h':
				total += time.Duration(n) * time.Hour
			case 'm':
				total += time.Duration(n) * time.Minute
			case 's':
				total += time.Duration(n) * time.Second
			}
			num = ""
		default:
			return 0, fmt.Errorf("bad duration %q", s)
		}
	}
	if num != "" || total <= 0 {
		return 0, fmt.Errorf("bad duration %q", s)
	}
	return total, nil
}
