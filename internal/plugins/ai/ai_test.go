package ai

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jclement/owgbot/internal/config"
	"github.com/jclement/owgbot/internal/plugin"
)

// fakeOpenAI serves canned chat responses (raw choice message JSON).
func fakeOpenAI(t *testing.T, messages []string) *httptest.Server {
	t.Helper()
	i := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := messages[min(i, len(messages)-1)]
		i++
		w.Write([]byte(`{"choices":[{"message":` + m + `}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestPlugin(t *testing.T, srvURL string, runCmd RunCommand) *Plugin {
	t.Helper()
	p := New("test-key", runCmd)
	p.client.BaseURL = srvURL
	if err := p.Init(plugin.Env{
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		NodeName: func(string) string { return "" },
		Config: func() *config.Config {
			c := config.Default()
			return &c
		},
	}); err != nil {
		t.Fatal(err)
	}
	return p
}

func newCtx(out *[]string) *plugin.Ctx {
	c := config.Default()
	return &plugin.Ctx{
		Context: context.Background(),
		User:    "aabbccddeeff",
		SNR:     -7.5,
		Reply:   func(s string) { *out = append(*out, s) },
		Config:  &c,
	}
}

func TestToolCallRoundTrip(t *testing.T) {
	srv := fakeOpenAI(t, []string{
		`{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function",
		  "function":{"name":"run_command","arguments":"{\"command\":\"/nodes\"}"}}]}`,
		`{"role":"assistant","content":"2 nodes nearby: bob and eve."}`,
	})
	var ran []string
	p := newTestPlugin(t, srv.URL, func(pctx *plugin.Ctx, cmd string) string {
		ran = append(ran, fmt.Sprintf("%s %s snr=%.1f", pctx.User, cmd, pctx.SNR))
		return "2 node(s): bob, eve"
	})

	var out []string
	if err := p.HandleCommand(newCtx(&out), "ai", "who's around?"); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 || ran[0] != "aabbccddeeff /nodes snr=-7.5" {
		t.Fatalf("tool executed %v", ran)
	}
	if len(out) != 1 || !strings.Contains(out[0], "bob and eve") {
		t.Fatalf("reply: %v", out)
	}
}

func TestDisallowedCommandBlocked(t *testing.T) {
	srv := fakeOpenAI(t, []string{
		`{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function",
		  "function":{"name":"run_command","arguments":"{\"command\":\"/update\"}"}}]}`,
		`{"role":"assistant","content":"that command is off limits."}`,
	})
	called := false
	p := newTestPlugin(t, srv.URL, func(pctx *plugin.Ctx, cmd string) string {
		called = true
		return "should never run"
	})

	var out []string
	if err := p.HandleCommand(newCtx(&out), "ai", "update yourself"); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("disallowed command must not reach the dispatcher")
	}
	if len(out) != 1 {
		t.Fatalf("reply: %v", out)
	}
}

func TestBareAiGreets(t *testing.T) {
	srv := fakeOpenAI(t, []string{`{"role":"assistant","content":"hey there. what do you need?"}`})
	p := newTestPlugin(t, srv.URL, nil)
	var out []string
	if err := p.HandleCommand(newCtx(&out), "ai", ""); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || !strings.Contains(out[0], "hey there") {
		t.Fatalf("greeting: %v", out)
	}
	// The synthetic greeting prompt must not pollute history.
	p.mu.Lock()
	defer p.mu.Unlock()
	h := p.hist["aabbccddeeff"]
	if len(h) != 1 || h[0].Role != "assistant" {
		t.Fatalf("history should hold only the assistant greeting: %+v", h)
	}
}

func TestPlainReplyNoTools(t *testing.T) {
	srv := fakeOpenAI(t, []string{`{"role":"assistant","content":"42."}`})
	p := newTestPlugin(t, srv.URL, func(pctx *plugin.Ctx, cmd string) string {
		t.Fatal("no tool should run")
		return ""
	})
	var out []string
	if err := p.HandleCommand(newCtx(&out), "ai", "meaning of life?"); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0] != "42." {
		t.Fatalf("reply: %v", out)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
