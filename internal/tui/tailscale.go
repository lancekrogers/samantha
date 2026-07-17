package tui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
	ansi "github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/samantha/internal/audio"
)

const (
	tailscaleStopTimeout = 3 * time.Second
	tailscaleLogLimit    = 200
)

type tailscaleCommandFactory func(context.Context) (*exec.Cmd, error)

type tailscaleStartedMsg struct {
	server *tailscaleServer
	err    error
}

type tailscaleOutputMsg struct {
	server *tailscaleServer
	line   string
}

type tailscaleExitedMsg struct {
	server  *tailscaleServer
	err     error
	stopped bool
}

type tailscaleCopiedMsg struct {
	label string
	err   error
}

// tailscaleServer owns one `samantha serve --tailscale` child. Reusing the
// CLI entrypoint keeps TLS, pairing, auth, and remote-audio behavior on the
// same code path instead of duplicating security-sensitive setup in the TUI.
type tailscaleServer struct {
	mu       sync.Mutex
	cmd      *exec.Cmd
	events   chan tea.Msg
	done     chan struct{}
	stopping atomic.Bool
	stopOnce sync.Once
}

func newTailscaleServer() *tailscaleServer {
	return &tailscaleServer{
		events: make(chan tea.Msg, 512),
		done:   make(chan struct{}),
	}
}

func defaultTailscaleCommand(ctx context.Context) (*exec.Cmd, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate Samantha executable: %w", err)
	}
	args := []string{"serve", "--tailscale"}
	if dir := audio.DebugAudioDir(); dir != "" {
		args = append(args, "--debug-audio="+dir)
	}
	return exec.CommandContext(ctx, executable, args...), nil
}

func (s *tailscaleServer) start(ctx context.Context, factory tailscaleCommandFactory) error {
	cmd, err := factory(ctx)
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("capture Tailscale server output: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("capture Tailscale server diagnostics: %w", err)
	}

	s.mu.Lock()
	s.cmd = cmd
	s.mu.Unlock()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start Samantha Tailscale server: %w", err)
	}
	if s.stopping.Load() {
		// The user can leave the screen while exec.Start is still in flight.
		// Honor that earlier stop request now that a process exists.
		_ = cmd.Process.Signal(os.Interrupt)
	}

	var readers sync.WaitGroup
	readers.Add(2)
	go s.scan(&readers, stdout)
	go s.scan(&readers, stderr)
	go func() {
		err := cmd.Wait()
		readers.Wait()
		close(s.done)
		s.emit(tailscaleExitedMsg{server: s, err: err, stopped: s.stopping.Load()})
	}()
	return nil
}

func (s *tailscaleServer) scan(wg *sync.WaitGroup, reader io.Reader) {
	defer wg.Done()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	scanner.Split(scanTerminalLines)
	for scanner.Scan() {
		s.emit(tailscaleOutputMsg{server: s, line: scanner.Text()})
	}
	if err := scanner.Err(); err != nil && !s.stopping.Load() {
		s.emit(tailscaleOutputMsg{server: s, line: "read server output: " + err.Error()})
	}
}

// scanTerminalLines treats carriage-return status updates as individual
// records as well as normal newline-delimited output. The plain CLI UI uses
// both forms, and waiting for a later newline would otherwise concatenate an
// entire stream of live status updates into one large scanner token.
func scanTerminalLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i, b := range data {
		if b != '\r' && b != '\n' {
			continue
		}
		advance = i + 1
		if b == '\r' && advance < len(data) && data[advance] == '\n' {
			advance++
		}
		return advance, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func (s *tailscaleServer) emit(msg tea.Msg) {
	// Server output is diagnostic. Never let an inactive screen or a burst of
	// conversation logs block the child process. The structured URL, code, and
	// exit messages fit comfortably inside this bounded queue.
	select {
	case s.events <- msg:
	default:
	}
}

func (s *tailscaleServer) nextEvent() tea.Cmd {
	return func() tea.Msg { return <-s.events }
}

func (s *tailscaleServer) stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		s.stopping.Store(true)
		s.mu.Lock()
		cmd := s.cmd
		s.mu.Unlock()
		if cmd == nil || cmd.Process == nil {
			return
		}

		// The serve command handles an interrupt by canceling its context and
		// running pipeline/session cleanup. Escalate only if graceful shutdown
		// does not finish promptly.
		_ = cmd.Process.Signal(os.Interrupt)
		go func() {
			select {
			case <-s.done:
			case <-time.After(tailscaleStopTimeout):
				_ = cmd.Process.Kill()
			}
		}()
	})
}

func (s *tailscaleServer) stopAndWait(timeout time.Duration) {
	if s == nil {
		return
	}
	s.stop()
	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	select {
	case <-s.done:
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
	}
}

type tailscaleModel struct {
	ctx        context.Context
	factory    tailscaleCommandFactory
	server     *tailscaleServer
	width      int
	height     int
	status     string
	detail     string
	url        string
	pairing    string
	logs       []string
	starting   bool
	running    bool
	restarting bool
	leaving    bool
}

func newTailscale(ctx context.Context, factory tailscaleCommandFactory) tailscaleModel {
	if ctx == nil {
		ctx = context.Background()
	}
	if factory == nil {
		factory = defaultTailscaleCommand
	}
	return tailscaleModel{ctx: ctx, factory: factory, status: "Starting Tailscale server..."}
}

func (m *tailscaleModel) start() tea.Cmd {
	m.server = newTailscaleServer()
	m.status = "Starting Tailscale server..."
	m.detail = "Checking MagicDNS and HTTPS certificate"
	m.url = ""
	m.pairing = ""
	m.logs = nil
	m.starting = true
	m.running = false
	m.restarting = false
	m.leaving = false
	server := m.server
	return func() tea.Msg {
		err := server.start(m.ctx, m.factory)
		return tailscaleStartedMsg{server: server, err: err}
	}
}

func (m tailscaleModel) Update(msg tea.Msg) (tailscaleModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case tailscaleStartedMsg:
		if msg.server != m.server {
			return m, nil
		}
		m.starting = false
		if msg.err != nil {
			if m.leaving {
				return m, func() tea.Msg { return switchScreenMsg(screenLauncher) }
			}
			m.status = "Could not start Tailscale server"
			m.detail = msg.err.Error()
			return m, nil
		}
		m.running = true
		m.status = "Starting Tailscale server..."
		m.detail = "Preparing models, certificate, and pairing code"
		return m, m.server.nextEvent()

	case tailscaleOutputMsg:
		if msg.server != m.server {
			return m, nil
		}
		m.consumeLine(msg.line)
		return m, m.server.nextEvent()

	case tailscaleExitedMsg:
		if msg.server != m.server {
			return m, nil
		}
		m.running = false
		m.starting = false
		if m.leaving {
			return m, func() tea.Msg { return switchScreenMsg(screenLauncher) }
		}
		if m.restarting {
			return m, m.start()
		}
		if msg.stopped || msg.err == nil || errors.Is(msg.err, context.Canceled) {
			m.status = "Tailscale server stopped"
			m.detail = "Press r to start it again"
		} else if m.status == "Port 7262 is already in use" {
			// Keep the actionable error extracted from serve's diagnostics.
		} else {
			m.status = "Tailscale server stopped unexpectedly"
			m.detail = msg.err.Error()
		}
		return m, nil

	case tailscaleCopiedMsg:
		if msg.err != nil {
			m.detail = "Copy failed: " + msg.err.Error()
		} else {
			m.detail = msg.label + " copied"
		}

	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "b":
			if m.starting || m.running {
				m.leaving = true
				m.status = "Stopping Tailscale server..."
				m.detail = "Saving the remote session and releasing the port"
				m.stop()
				return m, nil
			}
			return m, func() tea.Msg { return switchScreenMsg(screenLauncher) }
		case "q":
			m.stop()
			return m, func() tea.Msg { return quitMsg{} }
		case "r":
			if m.starting {
				m.detail = "The server is already starting"
				return m, nil
			}
			if m.running && m.server != nil {
				m.restarting = true
				m.status = "Restarting Tailscale server..."
				m.detail = "Stopping the current server cleanly"
				m.server.stop()
				return m, nil
			}
			return m, m.start()
		case "c":
			if m.url != "" {
				return m, copyTailscaleValue("URL", m.url)
			}
		case "p":
			if m.pairing != "" {
				return m, copyTailscaleValue("Pairing code", m.pairing)
			}
		}
	}
	return m, nil
}

func (m *tailscaleModel) consumeLine(line string) {
	clean := strings.TrimSpace(ansi.Strip(line))
	if clean == "" {
		return
	}
	// Prefer the client-facing URL labels serve prints (legacy "Open on phone"
	// kept for older binaries / log replay).
	for _, label := range []string{"Open on client:", "Open on phone:"} {
		if value, ok := tailscaleField(clean, label); ok {
			m.url = value
			m.status = "Ready on your tailnet"
			m.detail = "Open the URL on any tailnet device, pair, then Start"
			break
		}
	}
	if value, ok := tailscaleField(clean, "Pairing code:"); ok {
		m.pairing = strings.Fields(value)[0]
	}
	if _, ok := tailscaleField(clean, "Listening:"); ok && m.status != "Ready on your tailnet" {
		m.status = "Tailscale server is listening"
	}
	if strings.Contains(strings.ToLower(clean), "address already in use") {
		m.status = "Port 7262 is already in use"
		m.detail = "Stop the existing `samantha serve` process, then press r"
	}
	// Soft cert fallback: serve keeps running on self-signed TLS.
	if strings.Contains(clean, "unavailable — using self-signed TLS") ||
		strings.Contains(clean, "unavailable - using self-signed TLS") {
		if m.status != "Ready on your tailnet" {
			m.status = "Starting with self-signed TLS"
		}
		m.detail = "Tailscale HTTPS cert unavailable; remote clients still work over the tailnet"
	}
	if value, ok := tailscaleField(clean, "Reason:"); ok && m.url == "" {
		m.detail = value
	}

	// Never mirror the long-lived bearer token into the TUI. The short-lived,
	// single-use pairing code is the intended device onboarding path.
	if _, ok := tailscaleField(clean, "Token:"); ok {
		clean = "Token: stored securely (use the pairing code)"
	}
	m.logs = append(m.logs, clean)
	if len(m.logs) > tailscaleLogLimit {
		m.logs = append([]string(nil), m.logs[len(m.logs)-tailscaleLogLimit:]...)
	}
}

func tailscaleField(line, label string) (string, bool) {
	idx := strings.Index(line, label)
	if idx < 0 {
		return "", false
	}
	value := strings.TrimSpace(line[idx+len(label):])
	return value, value != ""
}

func copyTailscaleValue(label, value string) tea.Cmd {
	return func() tea.Msg {
		return tailscaleCopiedMsg{label: label, err: clipboard.WriteAll(value)}
	}
}

func (m *tailscaleModel) stop() {
	if m != nil && m.server != nil {
		m.server.stop()
	}
}

func (m *tailscaleModel) stopAndWait(timeout time.Duration) {
	if m != nil && m.server != nil {
		m.server.stopAndWait(timeout)
	}
}

func (m tailscaleModel) View() string {
	width := m.width
	if width <= 0 {
		width = 80
	}
	height := m.height
	if height <= 0 {
		height = 24
	}
	compact := height < 14
	line := func(value string) string {
		return ansi.Truncate(value, max(width-2, 1), "…")
	}

	var b strings.Builder
	title := titleStyle.Render("  Remote over Tailscale")
	if compact {
		title = headerStyle.Render("  Remote over Tailscale")
	}
	b.WriteString(line(title))
	b.WriteString("\n")
	b.WriteString(line("  " + statusStyle.Render(m.status)))
	b.WriteString("\n")
	if m.detail != "" && !compact {
		b.WriteString(line("  " + dimStyle.Render(m.detail)))
		b.WriteString("\n")
	}

	if m.url != "" {
		b.WriteString("\n")
		b.WriteString(line("  Open on client: " + selectedStyle.Render(m.url)))
		b.WriteString("\n")
	}
	if m.pairing != "" {
		b.WriteString(line("  Pairing code: " + selectedStyle.Render(m.pairing)))
		b.WriteString("\n")
	}
	if m.url != "" && !compact {
		b.WriteString(line("  " + dimStyle.Render("Any tailnet device: Pair → Start → hold Talk")))
		b.WriteString("\n")
	}

	if !compact && len(m.logs) > 0 {
		fixedRows := 11
		visible := max(min(height-fixedRows, 8), 1)
		start := max(len(m.logs)-visible, 0)
		b.WriteString("\n")
		b.WriteString(line("  " + dimStyle.Render("Server activity")))
		b.WriteString("\n")
		for _, logLine := range m.logs[start:] {
			b.WriteString(line("  " + dimStyle.Render(logLine)))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	footer := "  c copy URL • p copy code • r restart • esc stop & back • q quit"
	if m.starting || m.leaving {
		footer = "  esc stop & back • q quit"
	} else if !m.running {
		footer = "  r start • esc back • q quit"
	}
	b.WriteString(line(dimStyle.Render(footer)))
	b.WriteString("\n")
	return b.String()
}
