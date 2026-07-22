package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/events"
)

const defaultPlaybackStallTimeout = 8 * time.Second

// errPlaybackStalled signals that the watchdog recovered a turn whose playback
// never became audible. It is intentionally not context.Canceled so the run
// loop retries listening rather than treating it as a shutdown.
var errPlaybackStalled = errors.New("playback stalled; turn recovered")

func (p *Pipeline) stallTimeout() time.Duration {
	if p.PlaybackStallTimeout > 0 {
		return p.PlaybackStallTimeout
	}
	return defaultPlaybackStallTimeout
}

// watchPlaybackStall recovers a turn whose synthesis began but never produced
// audible playback within timeout. On a stall it dumps every goroutine stack,
// cancels streamCtx (unblocking waitReady/pumpSegment/Write/observeStream) and
// signals streamResponse to return; returning then trips RunTurn's turnCancel,
// which stops the brain stream too.
func (p *Pipeline) watchPlaybackStall(streamCtx context.Context, started *atomic.Bool, cancel context.CancelFunc, stalled chan<- struct{}, timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-streamCtx.Done():
		return
	case <-timer.C:
	}

	if started.Load() || streamCtx.Err() != nil {
		return
	}

	msg := fmt.Sprintf("playback did not start within %s; recovering turn", timeout)
	if path, err := writeGoroutineDump(); err == nil {
		msg += fmt.Sprintf(" (goroutine dump: %s)", path)
	}
	p.emit(events.Error{Stage: "playback", Message: msg})

	cancel()
	if p.Player != nil {
		p.Player.Stop()
	}
	close(stalled)
}

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

	dir := filepath.Join(os.TempDir(), config.AppSlug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	path := filepath.Join(dir, fmt.Sprintf("playback-stall-%d.txt", time.Now().UnixNano()))
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		return "", err
	}
	return path, nil
}
