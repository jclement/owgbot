package gem

import (
	"strings"
	"testing"
)

func TestRenderNumbersLinks(t *testing.T) {
	page := "# Welcome\n\nSome text here.\n=> /posts/one.gmi First post\n=> gemini://other.site/ Other site\n=> https://example.com Web link\n"
	text, links := render("gemini://owg.fyi/", page)

	if !strings.Contains(text, "Welcome") {
		t.Error("heading text lost")
	}
	if !strings.Contains(text, "[1] First post") || !strings.Contains(text, "[2] Other site") {
		t.Errorf("links not numbered:\n%s", text)
	}
	if len(links) != 3 {
		t.Fatalf("want 3 links, got %d", len(links))
	}
	if links[0] != "gemini://owg.fyi/posts/one.gmi" {
		t.Errorf("relative link not resolved: %s", links[0])
	}
	if links[2] != "https://example.com" {
		t.Errorf("absolute link mangled: %s", links[2])
	}
}

func TestRenderPreformatted(t *testing.T) {
	page := "before\n```\ncode line\n```\nafter"
	text, _ := render("gemini://x/", page)
	if !strings.Contains(text, "code line") {
		t.Error("preformatted content lost")
	}
	if strings.Contains(text, "```") {
		t.Error("fence markers should be stripped")
	}
}

func TestPaginate(t *testing.T) {
	text := strings.Repeat("0123456789\n", 100)
	pages := paginate(strings.TrimRight(text, "\n"), 100)
	if len(pages) < 10 {
		t.Fatalf("expected many pages, got %d", len(pages))
	}
	for i, p := range pages {
		if len(p) > 100 {
			t.Errorf("page %d over budget: %d", i, len(p))
		}
	}
	var total int
	for _, p := range pages {
		total += strings.Count(p, "0123456789")
	}
	if total != 100 {
		t.Errorf("lost lines: %d/100", total)
	}
}

func TestNormalizeURL(t *testing.T) {
	u, err := normalizeURL("owg.fyi")
	if err != nil || u.String() != "gemini://owg.fyi/" {
		t.Fatalf("got %v, %v", u, err)
	}
	if _, err := normalizeURL("https://owg.fyi"); err == nil {
		t.Fatal("non-gemini scheme should fail")
	}
}
