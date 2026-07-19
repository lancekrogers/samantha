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
	remoteStopTimeout = 3 * time.Second
	remoteLogLimit    = 200
	// clientSetupURL is where users enable free Tailscale HTTPS Certificates
	// so strict browsers (notably mobile Safari) can use the microphone.
	clientSetupURL = "https://login.tailscale.com/admin/dns"
)

// remoteNetwork is how clients reach this machine.
type remoteNetwork int

const (
	remoteNetworkTailscale remoteNetwork = iota
	remoteNetworkLAN
)

func (n remoteNetwork) label() string {
	switch n {
	case remoteNetworkLAN:
		return "Same Wi‑Fi"
	default:
		return "Tailscale"
	}
}

func (n remoteNetwork) detail() string {
	switch n {
	case remoteNetworkLAN:
		return "Devices on the same local network"
	default:
		return "Any device on your Tailscale network"
	}
}

type remoteCommandFactory func(context.Context) (*exec.Cmd, error)

type remoteStartedMsg struct {
	server *remoteServer
	err    error
}

type remoteOutputMsg struct {
	server *remoteServer
	line   string
}

type remoteExitedMsg struct {
	server  *remoteServer
	err     error
	stopped bool
}

type remoteCopiedMsg struct {
	label string
	err   error
}

// remoteServer owns one `samantha serve` child (LAN or --tailscale).
// Reusing the CLI entrypoint keeps TLS, pairing, auth, and remote-audio
// behavior on the same code path instead of duplicating security-sensitive
// setup in the TUI.
type remoteServer struct {
	mu       sync.Mutex
	cmd      *exec.Cmd
	events   chan tea.Msg
	done     chan struct{}
	stopping atomic.Bool
	stopOnce sync.Once
}

func newRemoteServer() *remoteServer {
	return &remoteServer{
		events: make(chan tea.Msg, 512),
		done:   make(chan struct{}),
	}
}

func defaultRemoteCommand(network remoteNetwork) remoteCommandFactory {
	return func(ctx context.Context) (*exec.Cmd, error) {
		executable, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("locate Samantha executable: %w", err)
		}
		// Always mute host speaker for remote use — audio lives on the client.
		args := []string{"serve", "--no-voice"}
		if network == remoteNetworkTailscale {
			args = append(args, "--tailscale")
		}
		if dir := audio.DebugAudioDir(); dir != "" {
			args = append(args, "--debug-audio="+dir)
		}
		return exec.CommandContext(ctx, executable, args...), nil
	}
}

func (s *remoteServer) start(ctx context.Context, factory remoteCommandFactory) error {
	cmd, err := factory(ctx)
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("capture remote server output: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("capture remote server diagnostics: %w", err)
	}

	s.mu.Lock()
	s.cmd = cmd
	s.mu.Unlock()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start Samantha remote server: %w", err)
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
		s.emit(remoteExitedMsg{server: s, err: err, stopped: s.stopping.Load()})
	}()
	return nil
}

func (s *remoteServer) scan(wg *sync.WaitGroup, reader io.Reader) {
	defer wg.Done()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	scanner.Split(scanTerminalLines)
	for scanner.Scan() {
		s.emit(remoteOutputMsg{server: s, line: scanner.Text()})
	}
	if err := scanner.Err(); err != nil && !s.stopping.Load() {
		s.emit(remoteOutputMsg{server: s, line: "read server output: " + err.Error()})
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

func (s *remoteServer) emit(msg tea.Msg) {
	// Server output is diagnostic. Never let an inactive screen or a burst of
	// conversation logs block the child process. The structured URL, code, and
	// exit messages fit comfortably inside this bounded queue.
	select {
	case s.events <- msg:
	default:
	}
}

func (s *remoteServer) nextEvent() tea.Cmd {
	return func() tea.Msg { return <-s.events }
}

func (s *remoteServer) stop() {
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
			case <-time.After(remoteStopTimeout):
				_ = cmd.Process.Kill()
			}
		}()
	})
}

func (s *remoteServer) stopAndWait(timeout time.Duration) {
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

type remoteModel struct {
	ctx     context.Context
	factory remoteCommandFactory // optional override (tests); nil → mode factory
	server  *remoteServer
	width   int
	height  int
	status  string
	detail  string
	url     string
	pairing string
	// setupURL is the one-click admin page for enabling full mic (when limited).
	setupURL   string
	logs       []string
	starting   bool
	running    bool
	restarting bool
	leaving    bool
	// network is Tailscale (anywhere) or same Wi‑Fi / LAN.
	network remoteNetwork
	// clientLimited means the page can open but some browsers block the mic
	// until trusted HTTPS is available. Product language — not "self-signed".
	clientLimited bool
	// clientReady is true when serve reported full client access (trusted cert).
	clientReady bool
}

func newRemote(ctx context.Context, factory remoteCommandFactory) remoteModel {
	if ctx == nil {
		ctx = context.Background()
	}
	return remoteModel{
		ctx:     ctx,
		factory: factory,
		network: remoteNetworkTailscale,
		status:  "Starting remote access...",
	}
}

func (m *remoteModel) commandFactory() remoteCommandFactory {
	if m.factory != nil {
		return m.factory
	}
	return defaultRemoteCommand(m.network)
}

func (m *remoteModel) start() tea.Cmd {
	m.server = newRemoteServer()
	m.status = "Starting remote access..."
	m.detail = "Network: " + m.network.label() + " — " + m.network.detail()
	m.url = ""
	m.pairing = ""
	m.setupURL = ""
	m.logs = nil
	m.starting = true
	m.running = false
	m.restarting = false
	m.leaving = false
	m.clientLimited = false
	m.clientReady = false
	server := m.server
	factory := m.commandFactory()
	return func() tea.Msg {
		err := server.start(m.ctx, factory)
		return remoteStartedMsg{server: server, err: err}
	}
}

// switchNetwork changes Tailscale vs LAN. Restarts the server when running so
// the printed link matches the chosen network.
func (m *remoteModel) switchNetwork(next remoteNetwork) tea.Cmd {
	if next == m.network {
		return nil
	}
	m.network = next
	if m.starting {
		m.detail = "Still starting — switch again after it is ready"
		return nil
	}
	if m.running && m.server != nil {
		m.restarting = true
		m.status = "Switching network..."
		m.detail = "Restarting on " + m.network.label()
		m.server.stop()
		return nil
	}
	return m.start()
}

func (m remoteModel) Update(msg tea.Msg) (remoteModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case remoteStartedMsg:
		if msg.server != m.server {
			return m, nil
		}
		m.starting = false
		if msg.err != nil {
			if m.leaving {
				return m, func() tea.Msg { return switchScreenMsg(screenLauncher) }
			}
			m.status = "Could not start remote access"
			m.detail = msg.err.Error()
			return m, nil
		}
		m.running = true
		m.status = "Starting remote access..."
		m.detail = "Preparing the link and pairing code"
		return m, m.server.nextEvent()

	case remoteOutputMsg:
		if msg.server != m.server {
			return m, nil
		}
		m.consumeLine(msg.line)
		return m, m.server.nextEvent()

	case remoteExitedMsg:
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
			m.status = "Remote access stopped"
			m.detail = "Press r to start again"
		} else if m.status == "Port already in use" {
			// Keep the actionable error extracted from serve's diagnostics.
		} else {
			m.status = "Remote access stopped unexpectedly"
			m.detail = msg.err.Error()
		}
		return m, nil

	case remoteCopiedMsg:
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
				m.status = "Stopping remote access..."
				m.detail = "Closing the link so nothing stays open on the network"
				m.stop()
				return m, nil
			}
			return m, func() tea.Msg { return switchScreenMsg(screenLauncher) }
		case "q":
			m.stop()
			return m, func() tea.Msg { return quitMsg{} }
		case "r":
			if m.starting {
				m.detail = "Still starting — hang on a moment"
				return m, nil
			}
			if m.running && m.server != nil {
				m.restarting = true
				m.status = "Restarting..."
				m.detail = "Applying setup changes on " + m.network.label()
				m.server.stop()
				return m, nil
			}
			return m, m.start()
		case "1":
			return m, m.switchNetwork(remoteNetworkTailscale)
		case "2":
			return m, m.switchNetwork(remoteNetworkLAN)
		case "n", "tab":
			if m.network == remoteNetworkTailscale {
				return m, m.switchNetwork(remoteNetworkLAN)
			}
			return m, m.switchNetwork(remoteNetworkTailscale)
		case "c":
			if m.url != "" {
				return m, copyRemoteValue("Link", m.url)
			}
		case "p":
			if m.pairing != "" {
				return m, copyRemoteValue("Pairing code", m.pairing)
			}
		case "o":
			if setup := m.clientSetupLink(); setup != "" {
				return m, copyRemoteValue("Client setup link", setup)
			}
		}
	}
	return m, nil
}

func (m *remoteModel) clientSetupLink() string {
	if m.setupURL != "" {
		return m.setupURL
	}
	if m.clientLimited && m.network == remoteNetworkTailscale {
		return clientSetupURL
	}
	return ""
}

func (m *remoteModel) markReady() {
	if m.url == "" {
		return
	}
	if m.clientLimited {
		m.status = "Ready — mic limited in some browsers"
		m.detail = "Text works everywhere. Desktop voice usually works after one warning."
		return
	}
	if m.clientReady {
		m.status = "Ready for any device"
		m.detail = m.network.detail() + " — open the link, pair, then talk"
		return
	}
	// Cert mode unknown yet (older serve binary).
	m.status = "Ready"
	m.detail = "Open the link on any device, enter the code, then Start"
}

func (m *remoteModel) setClientAccess(limited bool) {
	m.clientLimited = limited
	m.clientReady = !limited
	if limited && m.setupURL == "" && m.network == remoteNetworkTailscale {
		m.setupURL = clientSetupURL
	}
	m.markReady()
}

func (m *remoteModel) consumeLine(line string) {
	clean := strings.TrimSpace(ansi.Strip(line))
	if clean == "" {
		return
	}
	// Prefer the client-facing URL labels serve prints (legacy "Open on phone"
	// kept for older binaries / log replay).
	for _, label := range []string{"Open on client:", "Open on phone:"} {
		if value, ok := remoteField(clean, label); ok {
			m.url = value
			m.markReady()
			break
		}
	}
	if value, ok := remoteField(clean, "Pairing code:"); ok {
		m.pairing = strings.Fields(value)[0]
	}
	if value, ok := remoteField(clean, "Network:"); ok {
		switch strings.ToLower(strings.Fields(value)[0]) {
		case "lan", "wifi", "wi-fi":
			m.network = remoteNetworkLAN
		case "tailscale", "tailnet":
			m.network = remoteNetworkTailscale
		}
	}
	// Product labels (current + short-lived "Phone *" aliases from earlier builds).
	for _, label := range []string{"Client setup:", "Phone setup:"} {
		if value, ok := remoteField(clean, label); ok {
			m.setupURL = strings.Fields(value)[0]
			m.setClientAccess(true)
			break
		}
	}
	for _, label := range []string{"Client access:", "Phone access:"} {
		if value, ok := remoteField(clean, label); ok {
			switch strings.ToLower(strings.Fields(value)[0]) {
			case "limited":
				m.setClientAccess(true)
			case "full":
				m.setClientAccess(false)
			}
			break
		}
	}
	// Legacy serve banners (pre product-language labels).
	if strings.Contains(clean, "unavailable — using self-signed TLS") ||
		strings.Contains(clean, "unavailable - using self-signed TLS") {
		m.setClientAccess(true)
	}
	if _, ok := remoteField(clean, "Listening:"); ok && m.url == "" {
		m.status = "Almost ready..."
		m.detail = "Finishing the link"
	}
	if strings.Contains(strings.ToLower(clean), "address already in use") {
		m.status = "Port already in use"
		m.detail = "Something else is already sharing Samantha — stop it, then press r"
	}

	// Never mirror the long-lived bearer token into the TUI. The short-lived,
	// single-use pairing code is the intended device onboarding path.
	if _, ok := remoteField(clean, "Token:"); ok {
		clean = "Token: stored securely (use the pairing code)"
	}
	m.logs = append(m.logs, clean)
	if len(m.logs) > remoteLogLimit {
		m.logs = append([]string(nil), m.logs[len(m.logs)-remoteLogLimit:]...)
	}
}

func remoteField(line, label string) (string, bool) {
	idx := strings.Index(line, label)
	if idx < 0 {
		return "", false
	}
	value := strings.TrimSpace(line[idx+len(label):])
	return value, value != ""
}

func copyRemoteValue(label, value string) tea.Cmd {
	return func() tea.Msg {
		return remoteCopiedMsg{label: label, err: clipboard.WriteAll(value)}
	}
}

func (m *remoteModel) stop() {
	if m != nil && m.server != nil {
		m.server.stop()
	}
}

func (m *remoteModel) stopAndWait(timeout time.Duration) {
	if m != nil && m.server != nil {
		m.server.stopAndWait(timeout)
	}
}

func (m remoteModel) View() string {
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
	title := titleStyle.Render("  Use on another device")
	if compact {
		title = headerStyle.Render("  Use on another device")
	}
	b.WriteString(line(title))
	b.WriteString("\n")

	// Network mode selector (always visible so LAN and Tailscale feel equal).
	if !compact {
		b.WriteString(line("  " + m.networkPickerLine()))
		b.WriteString("\n")
	}

	statusRender := statusStyle.Render
	switch {
	case strings.HasPrefix(m.status, "Could not"), strings.HasPrefix(m.status, "Port already"):
		statusRender = errorStyle.Render
	case m.clientLimited && m.url != "":
		statusRender = warningStyle.Render
	}
	b.WriteString(line("  " + statusRender(m.status)))
	b.WriteString("\n")
	if m.detail != "" && !compact {
		b.WriteString(line("  " + dimStyle.Render(m.detail)))
		b.WriteString("\n")
	}

	if m.url != "" {
		b.WriteString("\n")
		b.WriteString(line("  Link:  " + selectedStyle.Render(m.url)))
		b.WriteString("\n")
	}
	if m.pairing != "" {
		b.WriteString(line("  Code:  " + selectedStyle.Render(m.pairing)))
		b.WriteString("\n")
	}

	if m.url != "" && !compact {
		b.WriteString("\n")
		b.WriteString(line("  " + headerStyle.Render("On any device")))
		b.WriteString("\n")
		step1 := "1. Join the same Tailscale network as this computer"
		if m.network == remoteNetworkLAN {
			step1 = "1. Join the same Wi‑Fi / local network as this computer"
		}
		b.WriteString(line("  " + dimStyle.Render(step1)))
		b.WriteString("\n")
		b.WriteString(line("  " + dimStyle.Render("2. Open the link in a browser (press c to copy)")))
		b.WriteString("\n")
		b.WriteString(line("  " + dimStyle.Render("3. Enter the code → Pair → Start → Hold to Talk (or type)")))
		b.WriteString("\n")
	}

	if m.clientLimited && m.url != "" && !compact {
		b.WriteString("\n")
		b.WriteString(line("  " + warningStyle.Render("Microphone limited in some browsers")))
		b.WriteString("\n")
		b.WriteString(line("  " + dimStyle.Render("Works now: text on every device · voice on most desktop browsers")))
		b.WriteString("\n")
		b.WriteString(line("  " + dimStyle.Render("after one security warning. Mobile Safari needs trusted HTTPS.")))
		b.WriteString("\n")
		if setup := m.clientSetupLink(); setup != "" {
			b.WriteString(line("  " + dimStyle.Render("Free fix (Tailscale): open this page, enable HTTPS Certificates,")))
			b.WriteString("\n")
			b.WriteString(line("  " + selectedStyle.Render(setup)))
			b.WriteString("\n")
			b.WriteString(line("  " + dimStyle.Render("then press r here to restart.")))
			b.WriteString("\n")
		} else if m.network == remoteNetworkLAN {
			b.WriteString(line("  " + dimStyle.Render("For full mic on every browser: switch to Tailscale (press 1 or n)")))
			b.WriteString("\n")
			b.WriteString(line("  " + dimStyle.Render("and enable HTTPS Certificates, or pass a trusted cert to serve.")))
			b.WriteString("\n")
		}
	}

	// Only mirror raw serve output while starting or after a failure — ready
	// screens should read like a product, not a log tail.
	showLogs := !compact && len(m.logs) > 0 && (m.url == "" || strings.HasPrefix(m.status, "Could not") || strings.HasPrefix(m.status, "Port already") || strings.Contains(m.status, "unexpectedly"))
	if showLogs {
		fixedRows := 14
		visible := max(min(height-fixedRows, 4), 1)
		start := max(len(m.logs)-visible, 0)
		b.WriteString("\n")
		b.WriteString(line("  " + dimStyle.Render("Details")))
		b.WriteString("\n")
		for _, logLine := range m.logs[start:] {
			b.WriteString(line("  " + dimStyle.Render(logLine)))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	footer := "  1 Tailscale • 2 Wi‑Fi • n switch • c copy link • p copy code • r restart • esc back • q quit"
	if m.clientLimited && m.clientSetupLink() != "" {
		footer = "  o setup link • 1/2 network • c link • p code • r restart • esc back • q quit"
	}
	if m.starting || m.leaving {
		footer = "  esc stop & back • q quit"
	} else if !m.running {
		footer = "  1 Tailscale • 2 Wi‑Fi • r start • esc back • q quit"
	}
	b.WriteString(line(dimStyle.Render(footer)))
	b.WriteString("\n")
	return b.String()
}

func (m remoteModel) networkPickerLine() string {
	ts := "1 Tailscale"
	lan := "2 Same Wi‑Fi"
	switch m.network {
	case remoteNetworkLAN:
		return dimStyle.Render("Network  ") + dimStyle.Render(ts) + "   " + selectedStyle.Render("▸ "+lan)
	default:
		return dimStyle.Render("Network  ") + selectedStyle.Render("▸ "+ts) + "   " + dimStyle.Render(lan)
	}
}
