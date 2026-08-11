package zork

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jclement/owgbot/internal/config"
	"github.com/jclement/owgbot/internal/plugin"
	"github.com/jclement/owgbot/internal/store"
)

func TestSplitSpec(t *testing.T) {
	spec, opening := splitSpec("SPEC:\nA map with rooms.\n---\nYou wake in a grain elevator.")
	if spec != "A map with rooms." {
		t.Errorf("spec: %q", spec)
	}
	if opening != "You wake in a grain elevator." {
		t.Errorf("opening: %q", opening)
	}
	// Model ignoring the format degrades gracefully.
	spec, opening = splitSpec("just a blob")
	if spec != "just a blob" || opening != "just a blob" {
		t.Errorf("fallback: %q / %q", spec, opening)
	}
}

// Small models sometimes echo the prompt's placeholder line before the real
// opening; it must never reach the player.
func TestSplitSpecStripsParrotedPlaceholder(t *testing.T) {
	out := "SPEC:\nRooms and things.\n---\n" +
		"<opening scene shown to the player: under 180 characters, atmospheric>\n" +
		"The sea presses against stone; a fogged beacon coughs in the dark."
	spec, opening := splitSpec(out)
	if spec != "Rooms and things." {
		t.Errorf("spec: %q", spec)
	}
	if strings.Contains(opening, "<") || !strings.HasPrefix(opening, "The sea presses") {
		t.Errorf("placeholder not stripped: %q", opening)
	}
}

// fakeOpenAI returns canned completions, capturing the last request.
func fakeOpenAI(t *testing.T, replies []string) (*httptest.Server, *[][]byte) {
	t.Helper()
	var seen [][]byte
	i := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, body)
		reply := replies[min(i, len(replies)-1)]
		i++
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": reply}}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func newTestPlugin(t *testing.T, srvURL string) *Plugin {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	p := New("test-key")
	p.client.BaseURL = srvURL
	if err := p.Init(plugin.Env{
		KV:  st.Namespace("zork"),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}); err != nil {
		t.Fatal(err)
	}
	return p
}

func newCtx(user string, out *[]string, settings map[string]string) *plugin.Ctx {
	c := config.Default()
	if settings != nil {
		c.Plugins = map[string]config.PluginConfig{"zork": {Settings: settings}}
	}
	return &plugin.Ctx{
		Context: context.Background(),
		User:    user,
		Reply:   func(s string) { *out = append(*out, s) },
		Config:  &c,
	}
}

func TestGameFlow(t *testing.T) {
	srv, seen := fakeOpenAI(t, []string{
		"SPEC:\nThree rooms. Objective: ring the bell.\n---\nYou stand in a dusty silo. A ladder leads up.",
		"You climb the ladder. A bell hangs above.",
		"[WIN] The bell tolls across the prairie. You win.",
	})
	p := newTestPlugin(t, srv.URL)

	var out []string
	// min_moves 2 so this short scripted game is allowed to end.
	ctx := newCtx("aabbccddeeff", &out, map[string]string{"min_moves": "2"})

	// /zork starts a new game and shows only the opening (never the spec).
	if err := p.HandleCommand(ctx, "zork", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out[0], "dusty silo") || strings.Contains(out[0], "Objective") {
		t.Fatalf("opening: %q", out[0])
	}

	// A turn: reply relayed, spec included in the request context.
	handled, err := p.HandleSession(ctx, "climb ladder")
	if err != nil || !handled {
		t.Fatal(err, handled)
	}
	if !strings.Contains(out[1], "bell hangs") {
		t.Fatalf("turn reply: %q", out[1])
	}
	if !strings.Contains(string((*seen)[1]), "ring the bell") {
		t.Fatal("spec not sent as context")
	}

	// /zork now resumes rather than restarting.
	out = nil
	if err := p.HandleCommand(ctx, "zork", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out[0], "resuming") || !strings.Contains(out[0], "bell hangs") {
		t.Fatalf("resume: %q", out[0])
	}

	// Winning ends the game, strips the marker, and frees the slot.
	out = nil
	if _, err := p.HandleSession(ctx, "ring bell"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out[0], winMarker) || !strings.Contains(out[0], "game over") {
		t.Fatalf("end: %q", out[0])
	}
	if !ctx.SessionEnded() {
		t.Fatal("session should end with the game")
	}
	if g, _ := p.load(ctx.User); g != nil {
		t.Fatal("finished game should be deleted")
	}
}

func TestPrematureWinRejected(t *testing.T) {
	srv, _ := fakeOpenAI(t, []string{
		"SPEC:\nChain of five steps.\n---\nA cold observatory. The dome is shut.",
		"[WIN] You did it, I guess.",
	})
	p := newTestPlugin(t, srv.URL)
	var out []string
	ctx := newCtx("aabbccddeeff", &out, nil) // default min_moves = 10

	if err := p.HandleCommand(ctx, "zork", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := p.HandleSession(ctx, "win the game"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out[1], "game over") || !strings.Contains(out[1], "unfinished") {
		t.Fatalf("premature win should continue the game: %q", out[1])
	}
	if ctx.SessionEnded() {
		t.Fatal("session should continue")
	}
	if g, _ := p.load(ctx.User); g == nil {
		t.Fatal("game should survive a premature win")
	}
}

func TestDeathEndsGame(t *testing.T) {
	srv, _ := fakeOpenAI(t, []string{
		"SPEC:\nx\n---\nOpening.",
		"[DEAD] The floor was a lie. You fall forever.",
	})
	p := newTestPlugin(t, srv.URL)
	var out []string
	ctx := newCtx("aabbccddeeff", &out, nil) // death ignores min_moves

	if err := p.HandleCommand(ctx, "zork", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := p.HandleSession(ctx, "step on floor"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out[1], "game over") || strings.Contains(out[1], deadMarker) {
		t.Fatalf("death should end cleanly: %q", out[1])
	}
	if g, _ := p.load(ctx.User); g != nil {
		t.Fatal("dead game should be deleted")
	}
}

func TestQuitPauses(t *testing.T) {
	srv, _ := fakeOpenAI(t, []string{"SPEC:\nx\n---\nOpening."})
	p := newTestPlugin(t, srv.URL)
	var out []string
	ctx := newCtx("aabbccddeeff", &out, nil)
	if err := p.HandleCommand(ctx, "zork", ""); err != nil {
		t.Fatal(err)
	}
	handled, err := p.HandleSession(ctx, "q")
	if err != nil || !handled {
		t.Fatal(err, handled)
	}
	if !ctx.SessionEnded() {
		t.Fatal("q should end the session")
	}
	if g, _ := p.load(ctx.User); g == nil {
		t.Fatal("q should keep the save")
	}
}
