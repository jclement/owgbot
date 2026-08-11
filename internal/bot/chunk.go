package bot

import "fmt"

// Chunk splits text into mesh-sized messages of at most maxLen bytes,
// breaking on whitespace where possible. Multi-chunk output gets a trailing
// " i/n" marker so recipients can tell a continuation from a new reply.
func Chunk(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}
	// Reserve room for the worst-case marker (" 99/99").
	const markerLen = 6
	budget := maxLen - markerLen
	if budget < 10 {
		budget = maxLen // degenerate config; skip markers rather than thrash
	}

	var chunks []string
	rest := text
	for len(rest) > 0 {
		if len(rest) <= budget {
			chunks = append(chunks, rest)
			break
		}
		cut := budget
		// Prefer breaking at the last newline, else last space, in-window.
		found := false
		for i := budget; i > budget/2; i-- {
			if rest[i-1] == '\n' {
				cut, found = i, true
				break
			}
		}
		if !found {
			for i := budget; i > budget/2; i-- {
				if rest[i-1] == ' ' {
					cut = i
					break
				}
			}
		}
		// Never split a UTF-8 rune.
		for cut > 0 && rest[cut]&0xC0 == 0x80 {
			cut--
		}
		if cut == 0 {
			cut = budget
		}
		chunks = append(chunks, trimRight(rest[:cut]))
		rest = trimLeft(rest[cut:])
	}

	if len(chunks) > 1 {
		for i := range chunks {
			chunks[i] = fmt.Sprintf("%s %d/%d", chunks[i], i+1, len(chunks))
		}
	}
	return chunks
}

func trimRight(s string) string {
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\n') {
		s = s[:len(s)-1]
	}
	return s
}

func trimLeft(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\n') {
		s = s[1:]
	}
	return s
}
