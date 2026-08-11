package bbs

import "testing"

// Every tagline must fit one radio chunk with room to spare.
func TestTaglinesFitOneChunk(t *testing.T) {
	for i, tl := range taglines {
		if len(tl) > 120 {
			t.Errorf("tagline %d too long (%d bytes): %q", i, len(tl), tl)
		}
		if len(tl) == 0 {
			t.Errorf("tagline %d empty", i)
		}
	}
}

func TestTaglinePoolSize(t *testing.T) {
	if len(taglines) < 190 {
		t.Fatalf("pool has %d taglines; the sign said a couple hundred", len(taglines))
	}
}

func TestTaglinesUnique(t *testing.T) {
	seen := make(map[string]bool, len(taglines))
	for _, tl := range taglines {
		if seen[tl] {
			t.Errorf("duplicate tagline: %q", tl)
		}
		seen[tl] = true
	}
}
