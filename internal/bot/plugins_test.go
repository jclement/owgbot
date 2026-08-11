package bot_test

// Integration tests for the mesh-social plugins, driven through the bot core
// over the fake transport.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jclement/owgbot/internal/bot"
	"github.com/jclement/owgbot/internal/config"
	"github.com/jclement/owgbot/internal/plugins/mail"
	"github.com/jclement/owgbot/internal/plugins/ping"
	"github.com/jclement/owgbot/internal/plugins/seen"
	"github.com/jclement/owgbot/internal/plugins/wall"
	"github.com/jclement/owgbot/internal/plugins/wordle"
	"github.com/jclement/owgbot/internal/store"
	"github.com/jclement/owgbot/internal/transport/fake"
)

const (
	alice = "aaaaaaaaaaaa"
	bob   = "bbbbbbbbbbbb"
)

func newSocialBot(t *testing.T) *fake.Transport {
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
	tr.SetName(bob, "bobnode")
	var b *bot.Bot
	b, err = bot.New(tr, cfg, st, testLogger(t),
		ping.New(), seen.New(), mail.New(), wall.New(), wordle.New(),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go b.Run(ctx)
	return tr
}

func TestPingReportsSNR(t *testing.T) {
	tr := newSocialBot(t)
	tr.Inject(alice, "/ping")
	m := recv(t, tr)
	if !strings.Contains(m.Text, "pong") || !strings.Contains(m.Text, "-2.5dB") {
		t.Fatalf("got %q", m.Text)
	}
	// Sticky: bare messages keep echoing SNR.
	tr.Inject(alice, "walking north")
	if m := recv(t, tr); !strings.Contains(m.Text, "-2.5dB") {
		t.Fatalf("session echo missing: %q", m.Text)
	}
	// "off" ends the session; bare messages fall back to the hint.
	tr.Inject(alice, "off")
	if m := recv(t, tr); !strings.Contains(m.Text, "quiet") {
		t.Fatalf("off reply: %q", m.Text)
	}
	tr.Inject(alice, "walking south")
	if m := recv(t, tr); !strings.Contains(m.Text, "/help") {
		t.Fatalf("session should be over, got: %q", m.Text)
	}
}

func TestMailDeliveredOnActivity(t *testing.T) {
	tr := newSocialBot(t)

	tr.Inject(alice, "/mail bobnode meet at the tower")
	if m := recv(t, tr); !strings.Contains(m.Text, "queued for bobnode") {
		t.Fatalf("queue reply: %q", m.Text)
	}

	// Bob shows up: his own reply plus the delivered mail.
	tr.Inject(bob, "/ping")
	var got []string
	for i := 0; i < 2; i++ {
		m := recv(t, tr)
		if m.To != bob {
			t.Fatalf("message to %s, want bob", m.To)
		}
		got = append(got, m.Text)
	}
	joined := strings.Join(got, "|")
	if !strings.Contains(joined, "meet at the tower") || !strings.Contains(joined, "mail from") {
		t.Fatalf("mail not delivered: %q", joined)
	}

	// Delivered mail is gone: next activity brings only the pong.
	tr.Inject(bob, "still here")
	recv(t, tr)
	select {
	case m := <-tr.Outbound():
		t.Fatalf("unexpected second delivery: %q", m.Text)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestMailDeliveredOnAdvert(t *testing.T) {
	tr := newSocialBot(t)
	tr.Inject(alice, "/mail "+bob+" psst")
	recv(t, tr)

	tr.InjectAdvert(bob)
	m := recv(t, tr)
	if m.To != bob || !strings.Contains(m.Text, "psst") {
		t.Fatalf("advert delivery failed: %+v", m)
	}
}

func TestWallWriteAndRead(t *testing.T) {
	tr := newSocialBot(t)
	tr.Inject(alice, "/wall owg was here")
	recv(t, tr)
	tr.Inject(bob, "/wall")
	m := recv(t, tr)
	if !strings.Contains(m.Text, "owg was here") {
		t.Fatalf("wall read: %q", m.Text)
	}
}

func TestSeenTracksActivity(t *testing.T) {
	tr := newSocialBot(t)
	tr.Inject(bob, "/ping") // bob is now "seen"
	recv(t, tr)
	tr.Inject(alice, "/seen bobnode")
	m := recv(t, tr)
	if !strings.Contains(m.Text, "bobnode") || !strings.Contains(m.Text, "heard just now") {
		t.Fatalf("seen: %q", m.Text)
	}
	tr.Inject(alice, "/nodes")
	m = recv(t, tr)
	if !strings.Contains(m.Text, "bobnode") {
		t.Fatalf("nodes: %q", m.Text)
	}
}

func TestWordleGame(t *testing.T) {
	tr := newSocialBot(t)
	tr.Inject(alice, "/wordle")
	m := recv(t, tr)
	if !strings.Contains(m.Text, "wordle") || !strings.Contains(m.Text, "send a 5-letter guess") {
		t.Fatalf("board: %q", m.Text)
	}
	tr.Inject(alice, "crane")
	m = recv(t, tr)
	if !strings.Contains(m.Text, "🟩") && !strings.Contains(m.Text, "🟨") && !strings.Contains(m.Text, "⬛") {
		t.Fatalf("guess result: %q", m.Text)
	}
	// Non-5-letter bare message falls through to the default hint.
	tr.Inject(alice, "what")
	m = recv(t, tr)
	if !strings.Contains(m.Text, "/help") {
		t.Fatalf("fallthrough: %q", m.Text)
	}
}
