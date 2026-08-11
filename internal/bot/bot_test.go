package bot_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jclement/owgbot/internal/bot"
	"github.com/jclement/owgbot/internal/config"
	"github.com/jclement/owgbot/internal/plugin"
	"github.com/jclement/owgbot/internal/plugins/help"
	"github.com/jclement/owgbot/internal/plugins/ver"
	"github.com/jclement/owgbot/internal/store"
	"github.com/jclement/owgbot/internal/transport/fake"
)

// echo is a session-capable test plugin: /echo says hi, bare messages echo.
type echo struct{}

func (echo) Name() string { return "echo" }
func (echo) Commands() []plugin.Command {
	return []plugin.Command{{Name: "echo", Help: "echo session"}}
}
func (echo) Init(env plugin.Env) error { return nil }
func (echo) HandleCommand(ctx *plugin.Ctx, cmd, args string) error {
	ctx.Reply("echo on")
	return nil
}
func (echo) HandleSession(ctx *plugin.Ctx, text string) (bool, error) {
	ctx.Reply("echo: " + text)
	return true, nil
}

func newTestBot(t *testing.T) (*fake.Transport, context.CancelFunc) {
	t.Helper()
	c := config.Default()
	c.DataDir = t.TempDir()
	c.SendIntervalMS = 1
	c.RateLimit = config.RateLimit{PerMinute: 600, Burst: 100}
	cfg := config.Static(c)

	st, err := store.Open(c.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	tr := fake.New()
	var b *bot.Bot
	b, err = bot.New(tr, cfg, st, testLogger(t),
		help.New(func() []plugin.Plugin { return b.Plugins() }),
		ver.New(),
		echo{},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go b.Run(ctx)
	t.Cleanup(cancel)
	return tr, cancel
}

func recv(t *testing.T, tr *fake.Transport) fake.Sent {
	t.Helper()
	select {
	case m := <-tr.Outbound():
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("no reply within 2s")
		return fake.Sent{}
	}
}

func TestVerCommand(t *testing.T) {
	tr, _ := newTestBot(t)
	tr.Inject("aabbccddeeff", "/ver")
	m := recv(t, tr)
	if m.To != "aabbccddeeff" || !strings.HasPrefix(m.Text, "owgbot ") {
		t.Fatalf("got %+v", m)
	}
}

func TestHelpMenuAndDrillIn(t *testing.T) {
	tr, _ := newTestBot(t)
	user := "aabbccddeeff"

	// /help is a one-chunk category menu, not the full list.
	tr.Inject(user, "/help")
	m := recv(t, tr)
	if !strings.Contains(m.Text, "1=tools") || strings.Contains(m.Text, "/ver") {
		t.Fatalf("menu: %q", m.Text)
	}
	// A number drills into the category.
	tr.Inject(user, "1")
	m = recv(t, tr)
	if !strings.Contains(m.Text, "/ver") || !strings.Contains(m.Text, "/help") {
		t.Fatalf("category listing: %q", m.Text)
	}
	// Answering ended the menu session: another bare message gets the hint.
	tr.Inject(user, "2")
	if m = recv(t, tr); !strings.Contains(m.Text, "/help") || strings.Contains(m.Text, "/ver") {
		t.Fatalf("menu should be one-shot: %q", m.Text)
	}
}

func TestHelpAll(t *testing.T) {
	tr, _ := newTestBot(t)
	tr.Inject("aabbccddeeff", "/help all")
	m := recv(t, tr)
	if !strings.Contains(m.Text, "/ver") || !strings.Contains(m.Text, "/echo") {
		t.Fatalf("full list: %q", m.Text)
	}
}

func TestUnknownCommand(t *testing.T) {
	tr, _ := newTestBot(t)
	tr.Inject("aabbccddeeff", "/nope")
	m := recv(t, tr)
	if !strings.Contains(m.Text, "/help") {
		t.Fatalf("got %q", m.Text)
	}
}

func TestStickySessionRouting(t *testing.T) {
	tr, _ := newTestBot(t)
	user := "aabbccddeeff"

	// No sticky plugin yet: bare message gets the hint.
	tr.Inject(user, "hello")
	if m := recv(t, tr); !strings.Contains(m.Text, "/help") {
		t.Fatalf("expected hint, got %q", m.Text)
	}

	// /echo makes echo sticky; bare messages now route to it.
	tr.Inject(user, "/echo")
	if m := recv(t, tr); m.Text != "echo on" {
		t.Fatalf("got %q", m.Text)
	}
	tr.Inject(user, "hello again")
	if m := recv(t, tr); m.Text != "echo: hello again" {
		t.Fatalf("sticky routing failed: %q", m.Text)
	}

	// /ver is not session-capable, so echo stays sticky.
	tr.Inject(user, "/ver")
	recv(t, tr)
	tr.Inject(user, "still here?")
	if m := recv(t, tr); m.Text != "echo: still here?" {
		t.Fatalf("stickiness lost after non-session command: %q", m.Text)
	}
}

func TestInboundDedup(t *testing.T) {
	tr, _ := newTestBot(t)
	user := "aabbccddeeff"
	ts := time.Now()

	// A client retry re-sends with the SAME sender timestamp: one reply.
	tr.InjectAt(user, "/ver", ts)
	tr.InjectAt(user, "/ver", ts)
	recv(t, tr)
	select {
	case m := <-tr.Outbound():
		t.Fatalf("duplicate processed: %q", m.Text)
	case <-time.After(300 * time.Millisecond):
	}

	// A genuinely new message (fresh timestamp) is processed.
	tr.InjectAt(user, "/ver", ts.Add(5*time.Second))
	recv(t, tr)
}

func TestPeriodicAdvert(t *testing.T) {
	tr, _ := newTestBot(t) // default config: advert_interval 24h, never sent
	deadline := time.Now().Add(2 * time.Second)
	for tr.AdvertsSent() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := tr.AdvertsSent(); got != 1 {
		t.Fatalf("expected exactly one startup advert, got %d", got)
	}
}

func TestRateLimitNotice(t *testing.T) {
	c := config.Default()
	c.DataDir = t.TempDir()
	c.SendIntervalMS = 1
	c.RateLimit = config.RateLimit{PerMinute: 1, Burst: 1}
	cfg := config.Static(c)

	st, err := store.Open(c.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	tr := fake.New()
	b, err := bot.New(tr, cfg, st, testLogger(t), ver.New())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go b.Run(ctx)

	// Distinct timestamps: same-second identical messages are treated as
	// client retries by the dedup layer.
	ts := time.Now()
	tr.InjectAt("aabbccddeeff", "/ver", ts)
	recv(t, tr) // consumes the single token
	tr.InjectAt("aabbccddeeff", "/ver", ts.Add(time.Second))
	if m := recv(t, tr); !strings.Contains(m.Text, "rate limited") {
		t.Fatalf("expected rate limit notice, got %q", m.Text)
	}
	// Third message: silently dropped.
	tr.InjectAt("aabbccddeeff", "/ver", ts.Add(2*time.Second))
	select {
	case m := <-tr.Outbound():
		t.Fatalf("expected silence, got %q", m.Text)
	case <-time.After(300 * time.Millisecond):
	}
}
