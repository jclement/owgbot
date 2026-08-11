package devcli

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jclement/owgbot/internal/transport/fake"
)

// RunTUI runs the Bubble Tea dev UI: a chat pane for the mesh conversation,
// an input line, and a toggleable log pane (ctrl+l) so slog output never
// interleaves with the conversation. Blocks until the user quits.
func RunTUI(tr *fake.Transport, logs <-chan string, ver string) error {
	m := newModel(tr, logs, ver)
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

const logPaneLines = 8

var (
	headerStyle = lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.Color("230")).Background(lipgloss.Color("62")).Padding(0, 1)
	hintStyle  = lipgloss.NewStyle().Faint(true)
	youStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	botStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	sizeStyle  = lipgloss.NewStyle().Faint(true)
	logStyle   = lipgloss.NewStyle().Faint(true)
	logTitle   = lipgloss.NewStyle().Bold(true).Faint(true)
	errorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	ruleStyle  = lipgloss.NewStyle().Faint(true)
)

type botMsg fake.Sent
type logMsg string

type model struct {
	tr   *fake.Transport
	logs <-chan string
	ver  string

	vp    viewport.Model
	ti    textinput.Model
	chat  []string // rendered chat blocks
	logLn []string // log ring buffer
	show  bool     // log pane visible
	w, h  int
	ready bool
}

func newModel(tr *fake.Transport, logs <-chan string, ver string) *model {
	ti := textinput.New()
	ti.Placeholder = "message the bot… (/help)"
	ti.Prompt = youStyle.Render("you ▸ ")
	ti.Focus()
	ti.CharLimit = 300
	return &model{tr: tr, logs: logs, ver: ver, ti: ti}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, waitOutbound(m.tr), waitLog(m.logs))
}

func waitOutbound(tr *fake.Transport) tea.Cmd {
	return func() tea.Msg { return botMsg(<-tr.Outbound()) }
}

func waitLog(logs <-chan string) tea.Cmd {
	return func() tea.Msg { return logMsg(<-logs) }
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.layout()
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			return m, tea.Quit
		case "ctrl+l":
			m.show = !m.show
			m.layout()
			return m, nil
		case "enter":
			text := strings.TrimSpace(m.ti.Value())
			if text == "" {
				return m, nil
			}
			m.append(youStyle.Render("you ▸ ") + text)
			m.ti.Reset()
			m.tr.Inject(DevUser, text)
			return m, nil
		case "pgup", "pgdown":
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return m, cmd
		}
		var cmd tea.Cmd
		m.ti, cmd = m.ti.Update(msg)
		return m, cmd

	case botMsg:
		m.append(renderBot(fake.Sent(msg)))
		return m, waitOutbound(m.tr)

	case logMsg:
		line := strings.TrimRight(string(msg), "\n")
		if line != "" {
			m.logLn = append(m.logLn, line)
			if len(m.logLn) > 500 {
				m.logLn = m.logLn[len(m.logLn)-500:]
			}
		}
		return m, waitLog(m.logs)
	}
	return m, nil
}

// renderBot formats one radio chunk: prefix, indented continuation lines,
// and a faint byte count — a running reminder of what each reply costs in
// airtime.
func renderBot(s fake.Sent) string {
	prefix := botStyle.Render("bot ▸ ")
	lines := strings.Split(s.Text, "\n")
	var b strings.Builder
	b.WriteString(prefix + lines[0])
	for _, l := range lines[1:] {
		b.WriteString("\n      " + l)
	}
	style := sizeStyle
	if len(s.Text) > 130 {
		style = errorStyle // over a typical chunk budget — shouldn't happen
	}
	b.WriteString(style.Render(fmt.Sprintf("  ·%dB", len(s.Text))))
	return b.String()
}

func (m *model) append(block string) {
	m.chat = append(m.chat, block)
	if len(m.chat) > 500 {
		m.chat = m.chat[len(m.chat)-500:]
	}
	m.refreshChat()
}

func (m *model) refreshChat() {
	if !m.ready {
		return
	}
	wrap := lipgloss.NewStyle().Width(m.vp.Width)
	blocks := make([]string, len(m.chat))
	for i, c := range m.chat {
		blocks[i] = wrap.Render(c)
	}
	m.vp.SetContent(strings.Join(blocks, "\n"))
	m.vp.GotoBottom()
}

func (m *model) layout() {
	chatH := m.h - 2 // header + input
	if m.show {
		chatH -= logPaneLines + 1 // pane + its title rule
	}
	if chatH < 3 {
		chatH = 3
	}
	if !m.ready {
		m.vp = viewport.New(m.w, chatH)
		m.ready = true
	} else {
		m.vp.Width, m.vp.Height = m.w, chatH
	}
	m.ti.Width = m.w - 10
	m.refreshChat()
}

func (m *model) View() string {
	if !m.ready {
		return "starting…"
	}
	title := headerStyle.Render(" owgbot dev · " + m.ver + " ")
	hints := hintStyle.Render("  you are " + DevUser + " (admin) · ctrl+l logs · ctrl+c quit")
	header := lipgloss.NewStyle().MaxWidth(m.w).Render(title + hints)

	parts := []string{header, m.vp.View()}
	if m.show {
		parts = append(parts, m.logPane())
	}
	parts = append(parts, m.ti.View())
	return strings.Join(parts, "\n")
}

func (m *model) logPane() string {
	rule := strings.Repeat("─", max(m.w-8, 1))
	out := []string{logTitle.Render("logs ") + ruleStyle.Render(rule)}
	start := max(len(m.logLn)-logPaneLines, 0)
	for _, l := range m.logLn[start:] {
		if len(l) > m.w {
			l = l[:m.w]
		}
		out = append(out, logStyle.Render(l))
	}
	for len(out) < logPaneLines+1 {
		out = append(out, "")
	}
	return strings.Join(out, "\n")
}
