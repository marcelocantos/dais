// remote is a terminal client for daisd. It connects to the shepherd
// and sends text messages, displaying streamed responses with markdown
// rendering. Reconnects automatically with exponential backoff.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/coder/websocket"
	"github.com/peterh/liner"
)

// Message types for bubbletea.
type (
	connectedMsg    string // server version
	disconnectedMsg struct{ err error }
	textMsg         string // incremental text from shepherd
	statusMsg       string // shepherd status change
	errorMsg        string // error from server
	historyMsg      []historyEntry
)

type historyEntry struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

var (
	statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	promptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
)

func main() {
	addr := flag.String("addr", "localhost:8080", "daisd address")
	flag.Parse()

	url := fmt.Sprintf("ws://%s/ws/remote", *addr)

	p := tea.NewProgram(
		newModel(url),
		tea.WithAltScreen(),
	)

	// Handle Ctrl-C outside bubbletea (backup).
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT)
	go func() {
		<-sigCh
		p.Quit()
	}()

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Save history on exit.
	saveHistory(nil)
}

// --- History management ---

var history []string

func historyPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, ".dais")
	os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "remote_history")
}

func loadHistory() {
	hp := historyPath()
	if hp == "" {
		return
	}
	l := liner.NewLiner()
	defer l.Close()
	if f, err := os.Open(hp); err == nil {
		l.ReadHistory(f)
		f.Close()
	}
	// liner doesn't expose history directly, so we store our own.
	if data, err := os.ReadFile(hp); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if line != "" {
				history = append(history, line)
			}
		}
	}
}

func saveHistory(extra []string) {
	history = append(history, extra...)
	hp := historyPath()
	if hp == "" {
		return
	}
	// Keep last 1000 lines.
	if len(history) > 1000 {
		history = history[len(history)-1000:]
	}
	os.WriteFile(hp, []byte(strings.Join(history, "\n")+"\n"), 0o644)
}

func addHistory(line string) {
	history = append(history, line)
}

// --- Model ---

type model struct {
	url      string
	conn     *websocket.Conn
	ctx      context.Context
	cancel   context.CancelFunc

	viewport viewport.Model
	input    textarea.Model
	renderer *glamour.TermRenderer

	// Shepherd state.
	markdown string // accumulated markdown for current turn
	rendered string // glamour-rendered output
	status   string // "idle", "thinking", "disconnected"
	version  string

	// Conversation log (rendered turns).
	log strings.Builder

	// Reconnect state.
	backoff time.Duration

	// History navigation.
	histIdx int // -1 = editing new input
	draft   string

	// Layout.
	width  int
	height int
	ready  bool
}

func newModel(url string) *model {
	loadHistory()

	ti := textarea.New()
	ti.Placeholder = "Type a message..."
	ti.Focus()
	ti.SetHeight(1)
	ti.ShowLineNumbers = false
	ti.KeyMap.InsertNewline.SetEnabled(false)

	ctx, cancel := context.WithCancel(context.Background())

	return &model{
		url:     url,
		ctx:     ctx,
		cancel:  cancel,
		input:   ti,
		status:  "disconnected",
		backoff: 100 * time.Millisecond,
		histIdx: -1,
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		m.connect(),
	)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.cancel()
			return m, tea.Quit

		case "enter":
			text := strings.TrimSpace(m.input.Value())
			if text == "" || m.status != "idle" {
				break
			}
			m.input.Reset()
			m.histIdx = -1
			m.draft = ""
			addHistory(text)

			m.log.WriteString(m.renderUserMsg(text) + "\n\n")
			m.markdown = ""
			m.rendered = ""
			m.updateViewport()

			return m, m.send(text)

		case "ctrl+p":
			// History: previous.
			if len(history) == 0 {
				break
			}
			if m.histIdx == -1 {
				m.draft = m.input.Value()
				m.histIdx = len(history) - 1
			} else if m.histIdx > 0 {
				m.histIdx--
			}
			m.input.SetValue(history[m.histIdx])
			return m, nil

		case "ctrl+n":
			// History: next.
			if m.histIdx == -1 {
				break
			}
			if m.histIdx < len(history)-1 {
				m.histIdx++
				m.input.SetValue(history[m.histIdx])
			} else {
				m.histIdx = -1
				m.input.SetValue(m.draft)
			}
			return m, nil
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		inputHeight := 3 // textarea + border
		statusHeight := 1

		if !m.ready {
			m.viewport = viewport.New(msg.Width, msg.Height-inputHeight-statusHeight)
			m.viewport.SetContent("")
			m.ready = true
		} else {
			m.viewport.Width = msg.Width
			m.viewport.Height = msg.Height - inputHeight - statusHeight
		}

		m.input.SetWidth(msg.Width - 2)

		// Recreate renderer with correct width.
		r, err := glamour.NewTermRenderer(
			glamour.WithStandardStyle("light"),
			glamour.WithWordWrap(msg.Width-4),
		)
		if err == nil {
			m.renderer = r
		}

		m.updateViewport()
		return m, nil

	case connectedMsg:
		m.version = string(msg)
		m.status = "idle"
		m.backoff = 100 * time.Millisecond
		m.log.Reset()
		m.log.WriteString(statusStyle.Render(fmt.Sprintf("Connected to daisd %s", m.version)) + "\n\n")
		m.updateViewport()
		return m, m.readWS()

	case historyMsg:
		for _, entry := range msg {
			switch entry.Role {
			case "user":
				m.log.WriteString(m.renderUserMsg(entry.Text) + "\n\n")
			case "shepherd":
				if m.renderer != nil {
					if r, err := m.renderer.Render(entry.Text); err == nil {
						m.log.WriteString(strings.TrimRight(r, "\n") + "\n")
					}
				} else {
					m.log.WriteString(entry.Text + "\n")
				}
			}
		}
		m.updateViewport()
		return m, m.readWS()

	case disconnectedMsg:
		m.conn = nil
		m.status = "disconnected"
		m.log.WriteString(statusStyle.Render(fmt.Sprintf("Disconnected: %v", msg.err)) + "\n")
		m.updateViewport()
		return m, m.reconnectAfter()

	case textMsg:
		m.markdown += string(msg)
		m.renderMarkdown()
		m.updateViewport()
		return m, m.readWS()

	case statusMsg:
		m.status = string(msg)
		if m.status == "idle" && m.markdown != "" {
			// Turn complete — finalize and append to log.
			m.renderMarkdown()
			m.log.WriteString(m.rendered + "\n")
			m.markdown = ""
			m.rendered = ""
		}
		m.updateViewport()
		return m, m.readWS()

	case errorMsg:
		m.log.WriteString(errorStyle.Render("Error: "+string(msg)) + "\n\n")
		m.updateViewport()
		return m, m.readWS()
	}

	// Update textarea.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)

	// Update viewport.
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

func (m *model) View() string {
	if !m.ready {
		return "Connecting..."
	}

	status := m.statusLine()
	return m.viewport.View() + "\n" + status + "\n" + m.input.View()
}

func (m *model) statusLine() string {
	var s string
	switch m.status {
	case "idle":
		s = "Ready"
	case "thinking":
		s = "Thinking..."
	case "disconnected":
		s = "Disconnected — reconnecting..."
	default:
		s = m.status
	}
	return statusStyle.Render(s)
}

func (m *model) renderUserMsg(text string) string {
	rendered := text
	if m.renderer != nil {
		if r, err := m.renderer.Render("***" + text + "***"); err == nil {
			rendered = strings.TrimRight(r, "\n")
		}
	}
	lines := strings.Split(rendered, "\n")
	for i, l := range lines {
		if i == 0 {
			lines[i] = "💬 " + l
		} else {
			lines[i] = "   " + l
		}
	}
	return strings.Join(lines, "\n")
}

func (m *model) renderMarkdown() {
	if m.renderer == nil || m.markdown == "" {
		return
	}
	rendered, err := m.renderer.Render(m.markdown)
	if err == nil {
		m.rendered = strings.TrimRight(rendered, "\n")
	}
}

func (m *model) updateViewport() {
	if !m.ready {
		return
	}
	content := m.log.String()
	if m.rendered != "" {
		content += m.rendered
	}
	m.viewport.SetContent(content)
	m.viewport.GotoBottom()
}

// --- WebSocket commands ---

func (m *model) connect() tea.Cmd {
	return func() tea.Msg {
		conn, _, err := websocket.Dial(m.ctx, m.url, nil)
		if err != nil {
			return disconnectedMsg{err: err}
		}
		conn.SetReadLimit(1 << 20)

		// Read init message.
		_, data, err := conn.Read(m.ctx)
		if err != nil {
			conn.CloseNow()
			return disconnectedMsg{err: err}
		}

		var init struct {
			Version string `json:"version"`
		}
		json.Unmarshal(data, &init)

		m.conn = conn
		return connectedMsg(init.Version)
	}
}

func (m *model) reconnectAfter() tea.Cmd {
	delay := m.backoff
	m.backoff = min(m.backoff*2, 5*time.Second)
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return m.connect()()
	})
}

func (m *model) readWS() tea.Cmd {
	return func() tea.Msg {
		if m.conn == nil {
			return disconnectedMsg{err: fmt.Errorf("no connection")}
		}

		_, data, err := m.conn.Read(m.ctx)
		if err != nil {
			return disconnectedMsg{err: err}
		}

		var msg struct {
			Type    string `json:"type"`
			Content string `json:"content,omitempty"`
			State   string `json:"state,omitempty"`
			Message string `json:"message,omitempty"`
		}
		json.Unmarshal(data, &msg)

		switch msg.Type {
		case "text":
			return textMsg(msg.Content)
		case "status":
			return statusMsg(msg.State)
		case "error":
			return errorMsg(msg.Message)
		case "history":
			var full struct {
				Entries []historyEntry `json:"entries"`
			}
			json.Unmarshal(data, &full)
			return historyMsg(full.Entries)
		}

		// Unknown message type — keep reading.
		return m.readWS()()
	}
}

func (m *model) send(text string) tea.Cmd {
	return func() tea.Msg {
		if m.conn == nil {
			return disconnectedMsg{err: fmt.Errorf("not connected")}
		}

		data, _ := json.Marshal(map[string]string{
			"type": "message",
			"text": text,
		})
		if err := m.conn.Write(m.ctx, websocket.MessageText, data); err != nil {
			return disconnectedMsg{err: err}
		}
		return nil
	}
}
