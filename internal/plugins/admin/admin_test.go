package admin

import (
	"encoding/hex"
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
	p := New(nil, nil, nil, nil)
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

// Published MeshCore vector: #mesh → first16(SHA256("#mesh")).
func TestHashtagKeyDerivation(t *testing.T) {
	want := "5b664cde0b08b220612113db980650f3"
	if got := hex.EncodeToString(hashtagKey("#mesh")); got != want {
		t.Fatalf("hashtagKey(#mesh) = %s, want %s", got, want)
	}
}

func TestWatchHashtagAutoJoin(t *testing.T) {
	provisioned = nil
	w := &stubWatcher{}
	names := map[int]string{0: "Public"}
	p := New(nil, nil, nil, func() ChannelWatcher { return w })
	p.env = plugin.Env{
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		ChannelName: func(slot int) string { return names[slot] },
	}
	var out []string
	ctx := &plugin.Ctx{User: "adminadmin01", Admin: true,
		Reply: func(s string) { out = append(out, s) }}

	// Unknown hashtag channel: derive key, take first free slot, watch.
	p.watch(ctx, "#YYC")
	if len(provisioned) != 1 {
		t.Fatalf("expected auto-provision, got %v", provisioned)
	}
	pr := provisioned[0]
	if pr.slot != 1 || pr.name != "#yyc" {
		t.Fatalf("slot/name: %+v", pr)
	}
	if hex.EncodeToString(pr.secret) != hex.EncodeToString(hashtagKey("#yyc")) {
		t.Fatal("derived key mismatch")
	}
	if len(w.list) != 1 || w.list[0] != 1 {
		t.Fatalf("should be watching slot 1: %v", w.list)
	}
	if !strings.Contains(out[0], "joined #yyc") {
		t.Fatalf("reply: %q", out[0])
	}

	// A non-hashtag miss still gets the help text, no provisioning.
	provisioned = nil
	out = nil
	p.watch(ctx, "5")
	p.watch(ctx, "nope")
	if len(provisioned) != 0 {
		t.Fatalf("bare name must not auto-provision: %v", provisioned)
	}
}

func TestParseChannelKey(t *testing.T) {
	// 16 bytes in the accepted encodings.
	for _, in := range []string{
		"00112233445566778899aabbccddeeff", // hex
		"ABEiM0RVZneImaq7zN3u/w==",         // base64 padded
		"ABEiM0RVZneImaq7zN3u/w",           // base64 unpadded
		"ABEiM0RVZneImaq7zN3u_w",           // base64 url-safe
	} {
		b, err := parseChannelKey(in)
		if err != nil || len(b) != 16 {
			t.Errorf("%q: %v (%d bytes)", in, err, len(b))
		}
	}
	for _, in := range []string{"tooshort", "zz112233445566778899aabbccddeeff", ""} {
		if _, err := parseChannelKey(in); err == nil {
			t.Errorf("%q should fail", in)
		}
	}
}

type stubWatcher struct{ list []int }

func (s *stubWatcher) WatchedChannels() []int { return s.list }
func (s *stubWatcher) Watch(ch int) error {
	s.Unwatch(ch)
	s.list = append(s.list, ch)
	return nil
}
func (s *stubWatcher) Unwatch(ch int) error {
	var next []int
	for _, c := range s.list {
		if c != ch {
			next = append(next, c)
		}
	}
	s.list = next
	return nil
}

type provision struct {
	slot   int
	name   string
	secret []byte
}

func (s *stubWatcher) ProvisionChannel(ch int, name string, secret []byte) error {
	provisioned = append(provisioned, provision{ch, name, secret})
	return nil
}

var provisioned []provision

func TestWatchProvision(t *testing.T) {
	provisioned = nil
	w := &stubWatcher{}
	names := map[int]string{0: "Public"}
	p := New(nil, nil, nil, func() ChannelWatcher { return w })
	p.env = plugin.Env{
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		ChannelName: func(slot int) string { return names[slot] },
	}
	var out []string
	ctx := &plugin.Ctx{User: "adminadmin01", Admin: true,
		Reply: func(s string) { out = append(out, s) }}

	p.watch(ctx, "2 yyc 00112233445566778899aabbccddeeff")
	if len(provisioned) != 1 || provisioned[0].slot != 2 || provisioned[0].name != "yyc" || len(provisioned[0].secret) != 16 {
		t.Fatalf("provision: %+v", provisioned)
	}
	if len(w.list) != 1 || w.list[0] != 2 {
		t.Fatalf("should watch after provisioning: %v", w.list)
	}

	// Bad key is rejected before touching the radio.
	provisioned = nil
	out = nil
	p.watch(ctx, "3 nope notakey")
	if len(provisioned) != 0 || !strings.Contains(out[0], "bad key") {
		t.Fatalf("bad key handling: %v %v", provisioned, out)
	}
}

func TestWatchCommand(t *testing.T) {
	w := &stubWatcher{}
	p := New(nil, nil, nil, func() ChannelWatcher { return w })
	p.env = plugin.Env{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		ChannelName: func(slot int) string {
			return map[int]string{0: "Public", 2: "yyc-chat"}[slot]
		},
	}
	var out []string
	ctx := &plugin.Ctx{User: "adminadmin01", Admin: true,
		Reply: func(s string) { out = append(out, s) }}

	// List: nothing watched, slots visible.
	p.watch(ctx, "")
	if !strings.Contains(out[0], "watching: nothing") || !strings.Contains(out[0], "2 #yyc-chat") {
		t.Fatalf("list: %q", out[0])
	}
	// Add by name (with #), case-insensitive.
	p.watch(ctx, "#Yyc-Chat")
	if len(w.list) != 1 || w.list[0] != 2 {
		t.Fatalf("watch by name: %v", w.list)
	}
	// Add by slot number.
	p.watch(ctx, "0")
	if len(w.list) != 2 {
		t.Fatalf("watch by slot: %v", w.list)
	}
	// Remove with leading dash.
	p.watch(ctx, "-#yyc-chat")
	if len(w.list) != 1 || w.list[0] != 0 {
		t.Fatalf("unwatch: %v", w.list)
	}
	// Unknown bare name (no hashtag): help text, no auto-join.
	out = nil
	p.watch(ctx, "nope")
	if !strings.Contains(out[0], "no channel") {
		t.Fatalf("unknown: %q", out[0])
	}
	// Removing an unknown hashtag must not auto-join either.
	out = nil
	p.watch(ctx, "-#ghost")
	if !strings.Contains(out[0], "no channel") {
		t.Fatalf("unknown remove: %q", out[0])
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

	p := New(nil, nil, nil, nil)
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
