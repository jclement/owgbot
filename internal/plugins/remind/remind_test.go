package remind

import (
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	cases := map[string]time.Duration{
		"5d":    5 * 24 * time.Hour,
		"1w":    7 * 24 * time.Hour,
		"2h30m": 2*time.Hour + 30*time.Minute,
		"90m":   90 * time.Minute,
		"45s":   45 * time.Second,
		"1w2d":  9 * 24 * time.Hour,
	}
	for in, want := range cases {
		got, err := parseDuration(in)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%s: got %v want %v", in, got, want)
		}
	}
}

func TestParseDurationRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "d", "5", "5x", "h30m", "-5d", "5d "} {
		if _, err := parseDuration(in); err == nil {
			t.Errorf("%q should fail", in)
		}
	}
}
