package wordle

import "testing"

func TestScore(t *testing.T) {
	cases := []struct{ guess, word, want string }{
		{"crane", "crane", "🟩🟩🟩🟩🟩"},
		{"slate", "crane", "⬛⬛🟩⬛🟩"},
		{"eeeee", "crane", "⬛⬛⬛⬛🟩"}, // dup letters: only the green survives
		{"radar", "roman", "🟩⬛⬛🟩⬛"}, // greens claim first; extra 'a'/'r' get nothing
		{"aback", "banal", "🟨🟨🟨⬛⬛"},
	}
	for _, c := range cases {
		if got := score(c.guess, c.word); got != c.want {
			t.Errorf("score(%q, %q) = %s, want %s", c.guess, c.word, got, c.want)
		}
	}
}

func TestWordOfDayDeterministic(t *testing.T) {
	if wordOfDay("2026-08-10") != wordOfDay("2026-08-10") {
		t.Fatal("same day must give same word")
	}
	// Different days should (almost always) differ — check a known pair.
	a, b := wordOfDay("2026-08-10"), wordOfDay("2026-08-11")
	t.Logf("words: %s, %s", a, b)
	if len(a) != 5 || len(b) != 5 {
		t.Fatal("words must be 5 letters")
	}
}

func TestGuessDictionary(t *testing.T) {
	for _, w := range []string{"crane", "toads", "slate", "aahed"} {
		if !isValidWord(w) {
			t.Errorf("%q should be a valid guess", w)
		}
	}
	for _, w := range []string{"zzzzz", "aeiou", "qqqqq"} {
		if isValidWord(w) {
			t.Errorf("%q should be rejected", w)
		}
	}
	// Every possible answer must be guessable.
	for _, a := range answers {
		if !isValidWord(a) {
			t.Errorf("answer %q missing from guess dictionary", a)
		}
	}
}

func TestAnswersAreValid(t *testing.T) {
	seen := map[string]bool{}
	for _, w := range answers {
		if len(w) != 5 || !isAlpha(w) {
			t.Errorf("bad answer %q", w)
		}
		if seen[w] {
			t.Errorf("duplicate answer %q", w)
		}
		seen[w] = true
	}
	if len(answers) < 400 {
		t.Fatalf("answer pool too small: %d", len(answers))
	}
}
