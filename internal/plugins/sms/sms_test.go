package sms

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jclement/owgbot/internal/config"
	"github.com/jclement/owgbot/internal/plugin"
	"github.com/jclement/owgbot/internal/store"
)

// fakeVoipms serves getSMS/sendSMS with mutable state.
type fakeVoipms struct {
	mu       sync.Mutex
	inbox    []string // JSON items
	sent     []string // "dst|message"
	failSend bool
}

func (f *fakeVoipms) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Query().Get("method") {
	case "getSMS":
		if len(f.inbox) == 0 {
			fmt.Fprint(w, `{"status":"no_sms"}`)
			return
		}
		fmt.Fprintf(w, `{"status":"success","sms":[%s]}`, strings.Join(f.inbox, ","))
	case "sendSMS":
		if f.failSend {
			fmt.Fprint(w, `{"status":"invalid_credentials"}`)
			return
		}
		f.sent = append(f.sent, r.URL.Query().Get("dst")+"|"+r.URL.Query().Get("message"))
		fmt.Fprint(w, `{"status":"success","sms":99}`)
	default:
		fmt.Fprint(w, "203.0.113.7") // the ipify stand-in
	}
}

func smsItem(id int, from, text string) string {
	return fmt.Sprintf(`{"id":"%d","date":"2026-08-11 12:00:00","contact":"%s","message":"%s"}`, id, from, text)
}

func newTestSMS(t *testing.T) (*Plugin, *fakeVoipms, *[]string) {
	t.Helper()
	fake := &fakeVoipms{}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(srv.Close)

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	var dms []string
	p := New(srv.URL)
	p.ipURL = srv.URL // no method param → fake answers with an IP
	p.env = plugin.Env{
		KV:     st.Namespace("sms"),
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		SendTo: func(user, text string) { dms = append(dms, user+": "+text) },
	}
	return p, fake, &dms
}

func newCtx(user string, out *[]string) *plugin.Ctx {
	c := config.Default()
	return &plugin.Ctx{Context: context.Background(), User: user,
		Reply: func(s string) { *out = append(*out, s) }, Config: &c}
}

const user = "aabbccddeeff"

func setup(t *testing.T, p *Plugin, fake *fakeVoipms) {
	t.Helper()
	var out []string
	// Existing history must set the watermark, not get delivered.
	fake.inbox = []string{smsItem(100, "4035550000", "old news")}
	if err := p.HandleCommand(newCtx(user, &out), "sms", "init 403-555-1234 me@example.com sekret"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out[0], "connected to 4035551234") {
		t.Fatalf("init: %q", out[0])
	}
}

func TestInitSendAndCap(t *testing.T) {
	p, fake, _ := newTestSMS(t)
	setup(t, p, fake)

	var out []string
	if err := p.HandleCommand(newCtx(user, &out), "sms", "+1-403-555-9999 meet at the tower"); err != nil {
		t.Fatal(err)
	}
	if len(fake.sent) != 1 || fake.sent[0] != "4035559999|meet at the tower" {
		t.Fatalf("sent: %v", fake.sent)
	}
	if !strings.Contains(out[0], "1/25 today") {
		t.Fatalf("reply: %q", out[0])
	}

	// Cap enforcement.
	p.env.KV.Set(user, "sent:"+timeNowDay(), "25")
	out = nil
	p.HandleCommand(newCtx(user, &out), "sms", "4035559999 one more")
	if !strings.Contains(out[0], "cap reached") || len(fake.sent) != 1 {
		t.Fatalf("cap: %q, sent %v", out[0], fake.sent)
	}
}

func TestInboundQueueAndDeliverOnActivity(t *testing.T) {
	p, fake, dms := newTestSMS(t)
	setup(t, p, fake)

	// New message arrives (id above watermark), plus the old one.
	fake.mu.Lock()
	fake.inbox = append(fake.inbox, smsItem(101, "5875551111", "you around?"))
	fake.mu.Unlock()

	c, _ := p.creds(user)
	n, err := p.pollUser(user, c)
	if err != nil || n != 1 {
		t.Fatalf("poll: %d, %v", n, err)
	}
	if len(*dms) != 0 {
		t.Fatalf("nothing delivered until user is heard: %v", *dms)
	}

	// User shows life: queued SMS delivered once.
	p.HandleActivity(user)
	if len(*dms) != 1 || !strings.Contains((*dms)[0], "5875551111") || !strings.Contains((*dms)[0], "you around?") {
		t.Fatalf("delivery: %v", *dms)
	}
	p.HandleActivity(user)
	if len(*dms) != 1 {
		t.Fatalf("no duplicate delivery: %v", *dms)
	}

	// Re-poll: watermark prevents requeueing the same message.
	if n, _ := p.pollUser(user, c); n != 0 {
		t.Fatalf("watermark failed: %d new", n)
	}
}

func TestSendFailureSurfaces(t *testing.T) {
	p, fake, _ := newTestSMS(t)
	setup(t, p, fake)
	fake.failSend = true
	var out []string
	p.HandleCommand(newCtx(user, &out), "sms", "4035559999 hello")
	if !strings.Contains(out[0], "invalid_credentials") {
		t.Fatalf("error surfacing: %q", out[0])
	}
}

func TestAuthFailureNotifiesOnceWithIP(t *testing.T) {
	p, fake, dms := newTestSMS(t)
	setup(t, p, fake)

	err := fmt.Errorf("voip.ms: ip_not_enabled")
	p.notifyAuthFailure(user, err)
	if len(*dms) != 1 || !strings.Contains((*dms)[0], "203.0.113.7") || !strings.Contains((*dms)[0], "whitelist") {
		t.Fatalf("warning: %v", *dms)
	}
	// Throttled: no second DM within 24h.
	p.notifyAuthFailure(user, err)
	if len(*dms) != 1 {
		t.Fatalf("should warn once: %v", *dms)
	}
	// Transient errors never DM.
	p.env.KV.Delete(user, "auth_warned")
	p.notifyAuthFailure(user, fmt.Errorf("voip.ms unreachable: timeout"))
	if len(*dms) != 1 {
		t.Fatalf("transient error must not warn: %v", *dms)
	}
}

func TestRecentUserGetsPushDelivery(t *testing.T) {
	p, fake, dms := newTestSMS(t)
	setup(t, p, fake)

	// The user was just heard (e.g. they ran /sms init a minute ago).
	p.markHeard(user)

	fake.mu.Lock()
	fake.inbox = append(fake.inbox, smsItem(102, "5875552222", "push me"))
	fake.mu.Unlock()

	c, _ := p.creds(user)
	n, err := p.pollUser(user, c)
	if err != nil || n != 1 {
		t.Fatalf("poll: %d, %v", n, err)
	}
	if p.heardRecently(user) {
		p.deliver(user)
	}
	if len(*dms) != 1 || !strings.Contains((*dms)[0], "push me") {
		t.Fatalf("recent user should get immediate delivery: %v", *dms)
	}
}

func TestPollIntervalTiers(t *testing.T) {
	p, fake, _ := newTestSMS(t)
	setup(t, p, fake)

	// Idle by default.
	if got := p.pollInterval(user); got != pollEveryIdle {
		t.Fatalf("idle: %v", got)
	}
	// Heard on the mesh: active tier.
	p.markHeard(user)
	if got := p.pollInterval(user); got != pollEvery {
		t.Fatalf("active: %v", got)
	}
	// Sending an SMS: conversation tier.
	var out []string
	p.HandleCommand(newCtx(user, &out), "sms", "4035559999 you up?")
	if got := p.pollInterval(user); got != pollConv {
		t.Fatalf("conversation: %v", got)
	}
	// A reply arriving mid-conversation extends the window.
	fake.mu.Lock()
	fake.inbox = append(fake.inbox, smsItem(150, "4035559999", "yep"))
	fake.mu.Unlock()
	c, _ := p.creds(user)
	if n, err := p.pollUser(user, c); err != nil || n != 1 {
		t.Fatalf("poll: %d %v", n, err)
	}
	if got := p.pollInterval(user); got != pollConv {
		t.Fatalf("reply should keep conversation hot: %v", got)
	}
}

func TestOffForgetsEverything(t *testing.T) {
	p, fake, _ := newTestSMS(t)
	setup(t, p, fake)
	var out []string
	p.HandleCommand(newCtx(user, &out), "sms", "off")
	if _, ok := p.creds(user); ok {
		t.Fatal("creds should be gone")
	}
}
