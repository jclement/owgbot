package devcli

// ChanWriter adapts an io.Writer (for slog) onto a channel the TUI drains.
// Writes never block: if the TUI is behind, lines are dropped — these are
// dev logs, not records.
type ChanWriter chan string

func (w ChanWriter) Write(p []byte) (int, error) {
	select {
	case w <- string(p):
	default:
	}
	return len(p), nil
}
