package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/lancekrogers/samantha/internal/events"
)

// playbackStallTimeout bounds how long a turn may go after synthesis begins
// without any audible playback before the watchdog force-recovers it.
const playbackStallTimeout = 8 * time.Second

// playbackStalled reports whether synthesis has begun but playback has not
// started within timeout — the signature of a wedged playback path.
func playbackStalled(synthStart time.Time, playbackStarted bool, now time.Time, timeout time.Duration) bool {
	if synthStart.IsZero() || playbackStarted {
		return false
	}
	return now.Sub(synthStart) >= timeout
}

// watchPlaybackStall guards a turn against a playback path that never produces
// audio. If playback has not started within playbackStallTimeout it dumps all
// goroutine stacks for diagnosis, then cancels the turn so the conversation
// loop returns to listening instead of hanging until Ctrl-C.
func (p *Pipeline) watchPlaybackStall(ctx context.Context, synthStart time.Time, started *atomic.Bool, cancel context.CancelFunc) {
	timer := time.NewTimer(playbackStallTimeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return
	case now := <-timer.C:
		if !playbackStalled(synthStart, started.Load(), now, playbackStallTimeout) {
			return
		}
	}

	msg := fmt.Sprintf("playback did not start within %s; recovering turn", playbackStallTimeout)
	if path, err := writeGoroutineDump(); err == nil {
		msg += fmt.Sprintf(" (goroutine dump: %s)", path)
	}
	p.emit(events.Error{Stage: "playback", Message: msg})

	cancel()
	if p.Player != nil {
		p.Player.Stop()
	}
}

// writeGoroutineDump captures every goroutine stack to a temp file so a stalled
// turn can be root-caused after the fact, returning the file path.
func writeGoroutineDump() (string, error) {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}

	dir := filepath.Join(os.TempDir(), "samantha")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	path := filepath.Join(dir, fmt.Sprintf("playback-stall-%d.txt", time.Now().UnixNano()))
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		return "", err
	}
	return path, nil
}
