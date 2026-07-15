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

func TestTailscaleModelParsesConnectionDetailsAndHidesToken(t *testing.T) {
	m := newTailscale(context.Background(), nil)
	m.width, m.height = 100, 24
	m.consumeLine("\x1b[36mOpen on phone:\x1b[0m https://mac.tailnet.ts.net:7262/")
	m.consumeLine("Pairing code: 042917  (expires 12:34:56)")
	m.consumeLine("Token: this-must-not-be-rendered")

	if m.url != "https://mac.tailnet.ts.net:7262/" {
		t.Fatalf("url = %q", m.url)
	}
	if m.pairing != "042917" {
		t.Fatalf("pairing = %q", m.pairing)
	}
	view := stripANSI(m.View())
	for _, want := range []string{"Ready on your tailnet", m.url, m.pairing, "Pair → Start → hold Talk"} {
		if !strings.Contains(view, want) {
			t.Errorf("Tailscale view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "this-must-not-be-rendered") {
		t.Fatal("Tailscale view exposed the long-lived bearer token")
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

func TestTailscaleModelFitsSmallTerminal(t *testing.T) {
	m := newTailscale(context.Background(), nil)
	m.width, m.height = 38, 8
	m.status = "Ready on your tailnet"
	m.url = "https://very-long-machine-name.tailnet.ts.net:7262/"
	m.pairing = "123456"
	m.running = true

	view := stripANSI(m.View())
	if got := len(strings.Split(view, "\n")); got > 8 {
		t.Fatalf("compact Tailscale screen rendered %d lines in 8-row terminal:\n%s", got, view)
	}
	for _, line := range strings.Split(view, "\n") {
		if len([]rune(line)) > 38 {
			t.Fatalf("compact line exceeds terminal width: %q", line)
		}
	}
}

func TestTailscaleModelExplainsExistingServerConflict(t *testing.T) {
	m := newTailscale(context.Background(), nil)
	m.consumeLine("listen tcp 100.72.165.77:7262: bind: address already in use")
	if m.status != "Port 7262 is already in use" || !strings.Contains(m.detail, "existing `samantha serve`") {
		t.Fatalf("conflict status/detail = %q / %q", m.status, m.detail)
	}
}

func TestTailscaleManagedProcessStreamsBannerAndExit(t *testing.T) {
	factory := func(ctx context.Context) (*exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestTailscaleHelperProcess", "--")
		cmd.Env = append(os.Environ(), "SAMANTHA_TAILSCALE_HELPER=1")
		return cmd, nil
	}
	m := newTailscale(context.Background(), factory)
	cmd := m.start()

	for step := 0; step < 12 && cmd != nil; step++ {
		msg := runTailscaleCmd(t, cmd)
		m, cmd = m.Update(msg)
	}
	if m.running {
		t.Fatal("managed process still marked running after helper exit")
	}
	if m.url != "https://mac.tailnet.ts.net:7262/" || m.pairing != "654321" {
		t.Fatalf("managed details = url %q, pairing %q", m.url, m.pairing)
	}
	if m.status != "Tailscale server stopped" {
		t.Fatalf("status = %q", m.status)
	}
}

func TestTailscaleManagedProcessStopsGracefully(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not support os.Interrupt for child processes")
	}
	factory := func(ctx context.Context) (*exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestTailscaleHelperProcess", "--")
		cmd.Env = append(os.Environ(), "SAMANTHA_TAILSCALE_HELPER=block")
		return cmd, nil
	}
	server := newTailscaleServer()
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

func TestTailscaleStopRequestedBeforeProcessStartIsHonored(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not support os.Interrupt for child processes")
	}
	factory := func(ctx context.Context) (*exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestTailscaleHelperProcess", "--")
		cmd.Env = append(os.Environ(), "SAMANTHA_TAILSCALE_HELPER=block")
		return cmd, nil
	}
	server := newTailscaleServer()
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

func TestTailscaleBackWaitsForManagedServerExit(t *testing.T) {
	m := newTailscale(context.Background(), nil)
	m.server = newTailscaleServer()
	m.starting = false
	m.running = true

	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Fatal("back navigated before the managed server exited")
	}
	if !m.leaving || !m.server.stopping.Load() {
		t.Fatal("back did not enter graceful server shutdown")
	}

	m, cmd = m.Update(tailscaleExitedMsg{server: m.server, stopped: true})
	if cmd == nil {
		t.Fatal("server exit did not return to the launcher")
	}
	msg, ok := cmd().(switchScreenMsg)
	if !ok || screen(msg) != screenLauncher {
		t.Fatalf("exit navigation message = %#v", msg)
	}
}

func runTailscaleCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
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

func TestTailscaleHelperProcess(t *testing.T) {
	mode := os.Getenv("SAMANTHA_TAILSCALE_HELPER")
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
	fmt.Println("Open on phone: https://mac.tailnet.ts.net:7262/")
	fmt.Println("Pairing code: 654321  (expires 12:34:56)")
	// The subprocess must terminate without running the parent test suite.
	os.Exit(0)
}
