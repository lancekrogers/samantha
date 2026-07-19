package tui

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRemoteModelParsesConnectionDetailsAndHidesToken(t *testing.T) {
	m := newRemote(context.Background(), nil)
	m.width, m.height = 100, 28
	m.running = true
	m.consumeLine("Network: tailscale")
	m.consumeLine("Client access: full")
	m.consumeLine("\x1b[36mOpen on client:\x1b[0m https://mac.tailnet.ts.net:7262/")
	m.consumeLine("Pairing code: 042917  (expires 12:34:56)")
	m.consumeLine("Token: this-must-not-be-rendered")

	if m.url != "https://mac.tailnet.ts.net:7262/" {
		t.Fatalf("url = %q", m.url)
	}
	if m.pairing != "042917" {
		t.Fatalf("pairing = %q", m.pairing)
	}
	if !m.clientReady || m.clientLimited {
		t.Fatalf("clientReady/limited = %v/%v", m.clientReady, m.clientLimited)
	}
	view := stripANSI(m.View())
	for _, want := range []string{
		"Ready for any device",
		m.url,
		m.pairing,
		"Hold to Talk",
		"Use on another device",
		"On any device",
		"Tailscale",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("remote screen missing %q:\n%s", want, view)
		}
	}
	for _, banned := range []string{"this-must-not-be-rendered", "self-signed", "MagicDNS", "On the phone"} {
		if strings.Contains(view, banned) {
			t.Fatalf("remote screen leaked %q:\n%s", banned, view)
		}
	}
}

func TestScanTerminalLinesHandlesCarriageReturnStatus(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("listening\rthinking\nready\r\n"))
	scanner.Split(scanTerminalLines)
	var got []string
	for scanner.Scan() {
		if token := scanner.Text(); token != "" {
			got = append(got, token)
		}
	}
	want := []string{"listening", "thinking", "ready"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("terminal records = %q, want %q", got, want)
	}
}

func TestRemoteModelFitsSmallTerminal(t *testing.T) {
	m := newRemote(context.Background(), nil)
	m.width, m.height = 38, 8
	m.status = "Ready for any device"
	m.url = "https://very-long-machine-name.tailnet.ts.net:7262/"
	m.pairing = "123456"
	m.running = true

	view := stripANSI(m.View())
	if got := len(strings.Split(view, "\n")); got > 8 {
		t.Fatalf("compact remote screen rendered %d lines in 8-row terminal:\n%s", got, view)
	}
	for _, line := range strings.Split(view, "\n") {
		if len([]rune(line)) > 38 {
			t.Fatalf("compact line exceeds terminal width: %q", line)
		}
	}
}

func TestRemoteModelExplainsExistingServerConflict(t *testing.T) {
	m := newRemote(context.Background(), nil)
	m.consumeLine("listen tcp 100.72.165.77:7262: bind: address already in use")
	if m.status != "Port already in use" || !strings.Contains(m.detail, "press r") {
		t.Fatalf("conflict status/detail = %q / %q", m.status, m.detail)
	}
}

func TestRemoteModelLimitedClientAccessShowsSetup(t *testing.T) {
	m := newRemote(context.Background(), nil)
	m.width, m.height = 100, 32
	m.running = true
	m.consumeLine("Network: tailscale")
	m.consumeLine("Client access: limited")
	m.consumeLine("Client setup: https://login.tailscale.com/admin/dns")
	m.consumeLine("Open on client: https://mac-studio.tail37114b.ts.net:7262/")
	m.consumeLine("Pairing code: 042917  (expires 12:34:56)")

	if !m.clientLimited || m.clientReady {
		t.Fatalf("clientLimited/ready = %v/%v", m.clientLimited, m.clientReady)
	}
	if m.status != "Ready — mic limited in some browsers" {
		t.Fatalf("status = %q", m.status)
	}
	if m.clientSetupLink() != "https://login.tailscale.com/admin/dns" {
		t.Fatalf("setup URL = %q", m.clientSetupLink())
	}
	view := stripANSI(m.View())
	for _, want := range []string{
		"Use on another device",
		m.url,
		m.pairing,
		"Microphone limited in some browsers",
		"HTTPS Certificates",
		"press r",
		"o setup link",
		"On any device",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("limited remote screen missing %q:\n%s", want, view)
		}
	}
	for _, banned := range []string{"self-signed", "TOFU", "leaf cert", "MagicDNS", "On the phone"} {
		if strings.Contains(view, banned) {
			t.Fatalf("limited screen leaked %q:\n%s", banned, view)
		}
	}
}

func TestRemoteModelLANModeSteps(t *testing.T) {
	m := newRemote(context.Background(), nil)
	m.width, m.height = 100, 28
	m.running = true
	m.network = remoteNetworkLAN
	m.consumeLine("Network: lan")
	m.consumeLine("Client access: limited")
	m.consumeLine("Open on client: https://192.168.0.124:7262/")
	m.consumeLine("Pairing code: 111222  (expires 12:00:00)")

	view := stripANSI(m.View())
	for _, want := range []string{
		"Same Wi‑Fi",
		"same Wi‑Fi",
		"192.168.0.124",
		"switch to Tailscale",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("LAN remote screen missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Join the same Tailscale") {
		t.Fatalf("LAN mode should not tell users to join Tailscale:\n%s", view)
	}
}

func TestRemoteModelSwitchNetworkKeys(t *testing.T) {
	m := newRemote(context.Background(), nil)
	if m.network != remoteNetworkTailscale {
		t.Fatalf("default network = %v", m.network)
	}
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	// Not running: switchNetwork calls start() which needs a real binary —
	// with nil factory and no server process, start() still schedules a cmd.
	if m.network != remoteNetworkLAN {
		t.Fatalf("after 2 network = %v", m.network)
	}
	if cmd == nil {
		t.Fatal("switching to LAN while stopped should start serve")
	}
	// Cancel the start attempt for the test process.
	if m.server != nil {
		m.server.stop()
	}
}

func TestRemoteManagedProcessStreamsBannerAndExit(t *testing.T) {
	factory := func(ctx context.Context) (*exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestRemoteHelperProcess", "--")
		cmd.Env = append(os.Environ(), "SAMANTHA_REMOTE_HELPER=1")
		return cmd, nil
	}
	m := newRemote(context.Background(), factory)
	cmd := m.start()

	for step := 0; step < 12 && cmd != nil; step++ {
		msg := runRemoteCmd(t, cmd)
		m, cmd = m.Update(msg)
	}
	if m.running {
		t.Fatal("managed process still marked running after helper exit")
	}
	if m.url != "https://mac.tailnet.ts.net:7262/" || m.pairing != "654321" {
		t.Fatalf("managed details = url %q, pairing %q", m.url, m.pairing)
	}
	if m.status != "Remote access stopped" {
		t.Fatalf("status = %q", m.status)
	}
}

func TestRemoteManagedProcessStopsGracefully(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not support os.Interrupt for child processes")
	}
	factory := func(ctx context.Context) (*exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestRemoteHelperProcess", "--")
		cmd.Env = append(os.Environ(), "SAMANTHA_REMOTE_HELPER=block")
		return cmd, nil
	}
	server := newRemoteServer()
	if err := server.start(context.Background(), factory); err != nil {
		t.Fatalf("start() error = %v", err)
	}
	server.stopAndWait(2 * time.Second)
	select {
	case <-server.done:
	case <-time.After(time.Second):
		t.Fatal("managed server did not exit after graceful stop")
	}
}

func TestRemoteStopRequestedBeforeProcessStartIsHonored(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not support os.Interrupt for child processes")
	}
	factory := func(ctx context.Context) (*exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestRemoteHelperProcess", "--")
		cmd.Env = append(os.Environ(), "SAMANTHA_REMOTE_HELPER=block")
		return cmd, nil
	}
	server := newRemoteServer()
	server.stop()
	if err := server.start(context.Background(), factory); err != nil {
		t.Fatalf("start() error = %v", err)
	}
	select {
	case <-server.done:
	case <-time.After(2 * time.Second):
		_ = server.cmd.Process.Kill()
		t.Fatal("pre-start stop request left the managed server running")
	}
}

func TestRemoteBackWaitsForManagedServerExit(t *testing.T) {
	m := newRemote(context.Background(), nil)
	m.server = newRemoteServer()
	m.starting = false
	m.running = true

	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Fatal("back navigated before the managed server exited")
	}
	if !m.leaving || !m.server.stopping.Load() {
		t.Fatal("back did not enter graceful server shutdown")
	}

	m, cmd = m.Update(remoteExitedMsg{server: m.server, stopped: true})
	if cmd == nil {
		t.Fatal("server exit did not return to the launcher")
	}
	msg, ok := cmd().(switchScreenMsg)
	if !ok || screen(msg) != screenLauncher {
		t.Fatalf("exit navigation message = %#v", msg)
	}
}

func TestDefaultRemoteCommandArgs(t *testing.T) {
	ts, err := defaultRemoteCommand(remoteNetworkTailscale)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(ts.Args[1:], " ")
	if !strings.Contains(got, "serve") || !strings.Contains(got, "--tailscale") || !strings.Contains(got, "--no-voice") {
		t.Fatalf("tailscale args = %q", got)
	}
	lan, err := defaultRemoteCommand(remoteNetworkLAN)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got = strings.Join(lan.Args[1:], " ")
	if !strings.Contains(got, "serve") || strings.Contains(got, "--tailscale") || !strings.Contains(got, "--no-voice") {
		t.Fatalf("lan args = %q", got)
	}
}

func runRemoteCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case msg := <-result:
		return msg
	case <-time.After(5 * time.Second):
		t.Fatal("Tailscale tea command timed out")
		return nil
	}
}

func TestRemoteHelperProcess(t *testing.T) {
	mode := os.Getenv("SAMANTHA_REMOTE_HELPER")
	if mode == "" {
		return
	}
	if mode == "block" {
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, os.Interrupt)
		<-signals
		fmt.Println("server stopped")
		os.Exit(0)
	}
	fmt.Println("Network: tailscale")
	fmt.Println("Client access: full")
	fmt.Println("Open on client: https://mac.tailnet.ts.net:7262/")
	fmt.Println("Pairing code: 654321  (expires 12:34:56)")
	// The subprocess must terminate without running the parent test suite.
	os.Exit(0)
}
