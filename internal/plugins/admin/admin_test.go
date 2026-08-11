package admin

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jclement/owgbot/internal/config"
	"github.com/jclement/owgbot/internal/plugin"
	"github.com/jclement/owgbot/internal/selfupdate"
	"github.com/jclement/owgbot/internal/store"
)

func TestUpdateCheckNotifiesOncePerTag(t *testing.T) {
	orig := checkRelease
	checkRelease = func(repo string) (*selfupdate.Release, error) {
		return &selfupdate.Release{Tag: "v9.9.9"}, nil
	}
	t.Cleanup(func() { checkRelease = orig })

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	c := config.Default()
	c.Admins = []string{"adminadmin01", "adminadmin02"}
	c.UpdateCheck = "1ns" // always due

	var sent []string
	p := New(nil, nil, nil)
	p.env = plugin.Env{
		KV:     st.Namespace("admin"),
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		SendTo: func(user, text string) { sent = append(sent, user+": "+text) },
		Config: func() *config.Config { return &c },
	}

	p.maybeCheckUpdates()
	if len(sent) != 2 {
		t.Fatalf("both admins should be notified, got %v", sent)
	}
	if !strings.Contains(sent[0], "v9.9.9") || !strings.Contains(sent[0], "/update") {
		t.Fatalf("notification text: %q", sent[0])
	}

	// Same tag again: silence.
	p.maybeCheckUpdates()
	if len(sent) != 2 {
		t.Fatalf("no duplicate notifications wanted, got %v", sent)
	}

	// A newer tag notifies again.
	checkRelease = func(repo string) (*selfupdate.Release, error) {
		return &selfupdate.Release{Tag: "v10.0.0"}, nil
	}
	p.maybeCheckUpdates()
	if len(sent) != 4 || !strings.Contains(sent[2], "v10.0.0") {
		t.Fatalf("new tag should re-notify, got %v", sent)
	}
}

func TestUpdateCheckDisabled(t *testing.T) {
	orig := checkRelease
	called := false
	checkRelease = func(repo string) (*selfupdate.Release, error) {
		called = true
		return &selfupdate.Release{Tag: "v9.9.9"}, nil
	}
	t.Cleanup(func() { checkRelease = orig })

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	c := config.Default()
	c.Admins = []string{"adminadmin01"}
	c.UpdateCheck = "0"

	p := New(nil, nil, nil)
	p.env = plugin.Env{
		KV:     st.Namespace("admin"),
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		SendTo: func(user, text string) { t.Fatal("nothing should be sent") },
		Config: func() *config.Config { return &c },
	}
	p.maybeCheckUpdates()
	if called {
		t.Fatal("disabled check must not hit the API")
	}
}
