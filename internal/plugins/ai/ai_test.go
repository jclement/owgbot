package ai

import (
	"context"
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
	p := newTestPlugin(t, srv.URL, func(ctx context.Context, user string, admin bool, cmd string) string {
		ran = append(ran, user+" "+cmd)
		return "2 node(s): bob, eve"
	})

	var out []string
	if err := p.HandleCommand(newCtx(&out), "ai", "who's around?"); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 || ran[0] != "aabbccddeeff /nodes" {
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
	p := newTestPlugin(t, srv.URL, func(ctx context.Context, user string, admin bool, cmd string) string {
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

func TestPlainReplyNoTools(t *testing.T) {
	srv := fakeOpenAI(t, []string{`{"role":"assistant","content":"42."}`})
	p := newTestPlugin(t, srv.URL, func(ctx context.Context, user string, admin bool, cmd string) string {
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
