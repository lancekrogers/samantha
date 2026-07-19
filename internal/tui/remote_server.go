package tui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/audio"
)

const remoteStopTimeout = 3 * time.Second

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
