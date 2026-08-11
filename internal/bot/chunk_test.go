package bot

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChunkShort(t *testing.T) {
	got := Chunk("hello", 130)
	if len(got) != 1 || got[0] != "hello" {
		t.Fatalf("got %q", got)
	}
}

func TestChunkSplitsAndMarks(t *testing.T) {
	text := strings.Repeat("word ", 100) // 500 bytes
	chunks := Chunk(text, 130)
	if len(chunks) < 4 {
		t.Fatalf("expected ≥4 chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > 130 {
			t.Errorf("chunk %d over budget: %d bytes", i, len(c))
		}
		if !strings.Contains(c, "/") {
			t.Errorf("chunk %d missing marker: %q", i, c)
		}
	}
	if !strings.HasSuffix(chunks[0], " 1/"+itoa(len(chunks))) {
		t.Errorf("first chunk marker wrong: %q", chunks[0])
	}
}

func TestChunkPrefersLineBreaks(t *testing.T) {
	text := "line one is here\nline two is here\nline three is quite long as well ok"
	chunks := Chunk(text, 40)
	if strings.Contains(chunks[0], "line two") && !strings.HasSuffix(strings.TrimSuffix(chunks[0], " 1/"+itoa(len(chunks))), "here") {
		t.Errorf("did not break at newline: %q", chunks[0])
	}
}

func TestChunkNeverSplitsRunes(t *testing.T) {
	text := strings.Repeat("héllo wörld ", 30)
	for _, c := range Chunk(text, 50) {
		if !utf8.ValidString(c) {
			t.Fatalf("invalid utf8 chunk: %q", c)
		}
	}
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}
