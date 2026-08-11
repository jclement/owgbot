// Package sms is a per-user SMS gateway over the user's own voip.ms account:
//
//	/sms init <did> <api-user> <api-pass>   connect your voip.ms account
//	/sms <number> <message>                 send an SMS (from YOUR number)
//	/sms check                              poll for new texts now
//	/sms off                                disconnect and forget credentials
//
// The bot holds no shared number and pays for nothing: every message is sent
// on the user's own account, so abuse economics stay with the user. Inbound
// texts are polled in the background, queued, and delivered when the user is
// next heard (message or advert) — same store-and-forward as /mail.
//
// Honest caveats, told to the user at init: credentials are stored plainly
// in the bot's database, SMS is plaintext at the provider regardless of mesh
// encryption, and delivery latency makes this wrong for 2FA codes.
package sms

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jclement/owgbot/internal/plugin"
	"github.com/jclement/owgbot/internal/store"
)

const (
	pollEvery   = 2 * time.Minute
	maxPerDay   = 25  // outbound cap per user — contains a stolen node
	maxSMSBytes = 160 // one SMS segment; keep the mesh:SMS mapping 1:1
)

type queued struct {
	From string    `json:"from"`
	Text string    `json:"text"`
	Time time.Time `json:"time"`
}

type Plugin struct {
	env   plugin.Env
	api   *voipms
	ipURL string
	stop  chan struct{}
	once  sync.Once

	ipMu     sync.Mutex
	cachedIP string
	ipAt     time.Time
}

// New builds the plugin. baseURL overrides the voip.ms endpoint (tests).
func New(baseURL string) *Plugin {
	return &Plugin{
		api:   newVoipms(baseURL, &http.Client{Timeout: 20 * time.Second}),
		ipURL: "https://api4.ipify.org", // A-record only: always answers with the IPv4
		stop:  make(chan struct{}),
	}
}

// publicIP reports the bot's public IPv4 — the address voip.ms sees and the
// one the user must whitelist. Cached for an hour; "" when undeterminable.
func (p *Plugin) publicIP() string {
	p.ipMu.Lock()
	defer p.ipMu.Unlock()
	if p.cachedIP != "" && time.Since(p.ipAt) < time.Hour {
		return p.cachedIP
	}
	resp, err := p.api.client.Get(p.ipURL)
	if err != nil {
		return p.cachedIP
	}
	defer resp.Body.Close()
	buf := make([]byte, 64)
	n, _ := resp.Body.Read(buf)
	ip := strings.TrimSpace(string(buf[:n]))
	if ip != "" {
		p.cachedIP, p.ipAt = ip, time.Now()
	}
	return p.cachedIP
}

// ipHint renders whitelist guidance when the error is the IP allowlist.
func (p *Plugin) ipHint(errText string) string {
	if !strings.Contains(errText, "ip_not_enabled") {
		return ""
	}
	if ip := p.publicIP(); ip != "" {
		return " — whitelist my IP " + ip + " at voip.ms/m/api.php"
	}
	return " — my IP needs whitelisting at voip.ms/m/api.php"
}

func (p *Plugin) Name() string { return "sms" }

func (p *Plugin) Commands() []plugin.Command {
	return []plugin.Command{{
		Name: "sms", Args: "[init … | <num> <msg> | check | off]",
		Help: "SMS via your own voip.ms account",
	}}
}

func (p *Plugin) Init(env plugin.Env) error {
	p.env = env
	go p.pollLoop()
	return nil
}

func (p *Plugin) Stop() { p.once.Do(func() { close(p.stop) }) }

// Queued texts ride the same delivery triggers as /mail.
func (p *Plugin) HandleActivity(user string) { p.deliver(user) }
func (p *Plugin) HandleAdvert(user string)   { p.deliver(user) }

func (p *Plugin) HandleCommand(ctx *plugin.Ctx, cmd, args string) error {
	fields := strings.Fields(args)
	switch {
	case len(fields) == 0:
		return p.status(ctx)
	case fields[0] == "init":
		return p.init(ctx, fields[1:])
	case fields[0] == "check":
		return p.check(ctx)
	case fields[0] == "off":
		p.env.KV.Delete(ctx.User, "cfg")
		for _, e := range p.mustList(ctx.User, "q:") {
			p.env.KV.Delete(ctx.User, e.Key)
		}
		ctx.Reply("sms disconnected, credentials forgotten")
		return nil
	case len(fields) >= 2:
		return p.send(ctx, fields[0], strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(args, fields[0]), " ")))
	default:
		ctx.Reply("usage: /sms init <did> <user> <pass> · /sms <num> <msg> · /sms check · /sms off")
		return nil
	}
}

func (p *Plugin) init(ctx *plugin.Ctx, fields []string) error {
	// Accept an optional leading provider word for future-proofing.
	if len(fields) == 4 && strings.EqualFold(fields[0], "voipms") {
		fields = fields[1:]
	}
	if len(fields) != 3 {
		ctx.Reply("usage: /sms init <did> <api-user> <api-pass> (voip.ms: enable API + IP whitelist first)")
		return nil
	}
	did, err := normalizeNumber(fields[0])
	if err != nil {
		ctx.Reply(err.Error())
		return nil
	}
	c := creds{Provider: "voipms", DID: did, User: fields[1], Pass: fields[2]}

	// Validate by fetching, and set the watermark to the newest existing
	// message so history doesn't dump onto the mesh.
	msgs, err := p.api.fetch(c)
	if err != nil {
		if hint := p.ipHint(err.Error()); hint != "" {
			ctx.Reply("voip.ms rejected that: " + err.Error() + hint)
			return nil
		}
		ctx.Reply("voip.ms rejected that: " + err.Error() + " (API enabled? password right?)")
		return nil
	}
	var max int64
	for _, m := range msgs {
		if m.ID > max {
			max = m.ID
		}
	}
	b, _ := json.Marshal(c)
	if err := p.env.KV.Set(ctx.User, "cfg", string(b)); err != nil {
		return err
	}
	p.env.KV.Set(ctx.User, "last_id", strconv.FormatInt(max, 10))
	ctx.Reply(fmt.Sprintf("connected to %s. I'll DM new texts when you're heard. "+
		"Note: your API key sits plainly in my database, and SMS is never private — don't route secrets or 2FA here.", did))
	return nil
}

func (p *Plugin) send(ctx *plugin.Ctx, to, message string) error {
	c, ok := p.creds(ctx.User)
	if !ok {
		ctx.Reply("not set up — /sms init <did> <api-user> <api-pass>")
		return nil
	}
	dst, err := normalizeNumber(to)
	if err != nil {
		ctx.Reply(err.Error())
		return nil
	}
	if message == "" {
		ctx.Reply("send what? /sms <number> <message>")
		return nil
	}
	if len(message) > maxSMSBytes {
		ctx.Reply(fmt.Sprintf("too long for one SMS (%d/%d chars)", len(message), maxSMSBytes))
		return nil
	}
	day := timeNowDay()
	sent := p.countToday(ctx.User, day)
	if sent >= maxPerDay {
		ctx.Reply(fmt.Sprintf("daily SMS cap reached (%d) — resets at midnight", maxPerDay))
		return nil
	}
	if err := p.api.send(c, dst, message); err != nil {
		ctx.Reply("send failed: " + err.Error() + p.ipHint(err.Error()))
		return nil
	}
	p.env.KV.Set(ctx.User, "sent:"+day, strconv.Itoa(sent+1))
	ctx.Reply(fmt.Sprintf("sent to %s (%d/%d today)", dst, sent+1, maxPerDay))
	return nil
}

func (p *Plugin) check(ctx *plugin.Ctx) error {
	c, ok := p.creds(ctx.User)
	if !ok {
		ctx.Reply("not set up — /sms init <did> <api-user> <api-pass>")
		return nil
	}
	n, err := p.pollUser(ctx.User, c)
	if err != nil {
		ctx.Reply("poll failed: " + err.Error())
		return nil
	}
	// The user is obviously reachable right now: flush the queue.
	p.deliver(ctx.User)
	if n == 0 {
		ctx.Reply("no new texts")
	}
	return nil
}

func (p *Plugin) status(ctx *plugin.Ctx) error {
	c, ok := p.creds(ctx.User)
	if !ok {
		reply := "SMS via your own voip.ms account. /sms init <did> <api-user> <api-pass>. " +
			"Setup at voip.ms/m/api.php: enable API, set an API password"
		if ip := p.publicIP(); ip != "" {
			reply += ", whitelist my IP " + ip
		}
		ctx.Reply(reply)
		return nil
	}
	ctx.Reply(fmt.Sprintf("connected as %s · %d/%d sent today · /sms <num> <msg>, check, off",
		c.DID, p.countToday(ctx.User, timeNowDay()), maxPerDay))
	return nil
}

// pollLoop fetches new inbound texts for every configured user.
func (p *Plugin) pollLoop() {
	t := time.NewTicker(pollEvery)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
		}
		entries, err := p.env.KV.List("cfg")
		if err != nil {
			continue
		}
		for _, e := range entries {
			var c creds
			if json.Unmarshal([]byte(e.Value), &c) != nil {
				continue
			}
			if _, err := p.pollUser(e.User, c); err != nil {
				p.env.Log.Debug("sms poll failed", "user", e.User, "err", err)
				p.notifyAuthFailure(e.User, err)
			} else {
				// Working again: re-arm the warning.
				p.env.KV.Delete(e.User, "auth_warned")
			}
		}
	}
}

// notifyAuthFailure proactively DMs a user when their voip.ms credentials
// stop working in the background — the classic case being a changed home IP
// falling off the whitelist. At most one warning per 24h.
func (p *Plugin) notifyAuthFailure(user string, err error) {
	text := err.Error()
	if !strings.Contains(text, "ip_not_enabled") && !strings.Contains(text, "invalid_credentials") {
		return // transient/network errors aren't worth a DM
	}
	if v, kerr := p.env.KV.Get(user, "auth_warned"); kerr == nil {
		if ts, perr := strconv.ParseInt(v, 10, 64); perr == nil && time.Since(time.Unix(ts, 0)) < 24*time.Hour {
			return
		}
	}
	msg := "heads up: voip.ms is rejecting me (" + text + ")"
	if hint := p.ipHint(text); hint != "" {
		msg += hint
	} else {
		msg += " — check your API settings at voip.ms/m/api.php"
	}
	p.env.SendTo(user, msg)
	p.env.KV.Set(user, "auth_warned", strconv.FormatInt(time.Now().Unix(), 10))
	p.env.Log.Info("sms auth failure notified", "user", user)
}

// pollUser fetches messages newer than the user's watermark and queues them.
func (p *Plugin) pollUser(user string, c creds) (int, error) {
	msgs, err := p.api.fetch(c)
	if err != nil {
		return 0, err
	}
	var last int64
	if v, err := p.env.KV.Get(user, "last_id"); err == nil {
		last, _ = strconv.ParseInt(v, 10, 64)
	}
	newMax, n := last, 0
	for _, m := range msgs {
		if m.ID <= last {
			continue
		}
		q := queued{From: m.From, Text: m.Text, Time: time.Now()}
		b, _ := json.Marshal(q)
		p.env.KV.Set(user, fmt.Sprintf("q:%020d", m.ID), string(b))
		if m.ID > newMax {
			newMax = m.ID
		}
		n++
	}
	if newMax != last {
		p.env.KV.Set(user, "last_id", strconv.FormatInt(newMax, 10))
	}
	return n, nil
}

// deliver DMs all queued texts to a now-reachable user.
func (p *Plugin) deliver(user string) {
	entries := p.mustList(user, "q:")
	for _, e := range entries {
		var q queued
		if json.Unmarshal([]byte(e.Value), &q) != nil {
			p.env.KV.Delete(user, e.Key)
			continue
		}
		p.env.SendTo(user, fmt.Sprintf("SMS from %s (%s): %s", q.From, plugin.AgoPhrase(q.Time), q.Text))
		p.env.KV.Delete(user, e.Key)
	}
	if len(entries) > 0 {
		p.env.Log.Info("sms delivered", "to", user, "count", len(entries))
	}
}

func (p *Plugin) creds(user string) (creds, bool) {
	var c creds
	v, err := p.env.KV.Get(user, "cfg")
	if err != nil || json.Unmarshal([]byte(v), &c) != nil {
		return c, false
	}
	return c, true
}

func timeNowDay() string { return time.Now().Format("2006-01-02") }

func (p *Plugin) countToday(user, day string) int {
	n := 0
	if v, err := p.env.KV.Get(user, "sent:"+day); err == nil {
		n, _ = strconv.Atoi(v)
	}
	return n
}

func (p *Plugin) mustList(user, prefix string) []store.Entry {
	entries, err := p.env.KV.ListUser(user, prefix)
	if err != nil {
		return nil
	}
	return entries
}
