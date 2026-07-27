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
	"github.com/lancekrogers/samantha/internal/tts"
)

const defaultPlaybackStallTimeout = 8 * time.Second

// errPlaybackStalled signals that the watchdog recovered a turn whose playback
// never became audible. It is intentionally not context.Canceled so the run
// loop retries listening rather than treating it as a shutdown.
var errPlaybackStalled = errors.New("playback stalled; turn recovered")

func (p *Pipeline) stallTimeout() time.Duration {
	// Explicit override always wins (tests arm a short budget; ops can raise it).
	if p.PlaybackStallTimeout > 0 {
		return p.PlaybackStallTimeout
	}
	// Otherwise use the active TTS provider's time-to-first-frame budget when
	// it exceeds the default. Without this, Qwen (and any whole-utterance
	// worker) regularly trips the 8s stall and the TUI shows the recovery
	// line mid-reply even though synthesis is still healthy.
	if grace := p.ttsFirstAudioGrace(); grace > defaultPlaybackStallTimeout {
		return grace
	}
	return defaultPlaybackStallTimeout
}

// ttsFirstAudioGrace returns the primary TTS provider's FirstAudioGrace when
// it implements tts.FirstAudioGracer; zero otherwise.
func (p *Pipeline) ttsFirstAudioGrace() time.Duration {
	if p == nil {
		return 0
	}
	primary, _ := p.ttsProviders()
	if g, ok := primary.(tts.FirstAudioGracer); ok {
		if d := g.FirstAudioGrace(); d > 0 {
			return d
		}
	}
	return 0
}

// stallHint names the knob that widens the budget, but only when the budget
// actually came from the provider's FirstAudioGrace. On the default 8s a
// Kokoro turn is not governed by qwen_tts_timeout, so naming it there points
// the operator at a setting with no effect.
func (p *Pipeline) stallHint(timeout time.Duration) string {
	if grace := p.ttsFirstAudioGrace(); grace > 0 && timeout == grace {
		return " (TTS may still be synthesizing — raise qwen_tts_timeout if this is a slow worker)"
	}
	return " (TTS may still be synthesizing)"
}

// stallNoticeDelay is when the watchdog tells the user it is still waiting. It
// fires at the old default budget so anything past 8s of silence is explained.
// Zero disables the notice: a budget that short recovers before the wait is
// worth narrating.
func stallNoticeDelay(timeout time.Duration) time.Duration {
	if timeout <= defaultPlaybackStallTimeout {
		return 0
	}
	return defaultPlaybackStallTimeout
}

// watchPlaybackStall recovers a turn whose synthesis began but never produced
// audible playback within timeout. On a stall it dumps every goroutine stack,
// cancels streamCtx (unblocking waitReady/pumpSegment/Write/observeStream) and
// signals streamResponse to return; returning then trips RunTurn's turnCancel,
// which stops the brain stream too.
func (p *Pipeline) watchPlaybackStall(streamCtx context.Context, started *atomic.Bool, cancel context.CancelFunc, stalled chan<- struct{}, timeout time.Duration) {
	p.watchPlaybackStallAfter(streamCtx, started, cancel, stalled, timeout, stallNoticeDelay(timeout))
}

// watchPlaybackStallAfter is watchPlaybackStall with the interim-notice delay
// injected so tests can exercise both timers without waiting out the real
// budget. noticeDelay of zero suppresses the notice.
func (p *Pipeline) watchPlaybackStallAfter(streamCtx context.Context, started *atomic.Bool, cancel context.CancelFunc, stalled chan<- struct{}, timeout, noticeDelay time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	// A whole-utterance worker can hold the grace for a minute or more. Say so
	// once the old 8s budget elapses so a long wait reads as synthesis in
	// progress rather than a frozen turn.
	var noticeC <-chan time.Time
	if noticeDelay > 0 {
		notice := time.NewTimer(noticeDelay)
		defer notice.Stop()
		noticeC = notice.C
	}

waiting:
	for {
		select {
		case <-streamCtx.Done():
			return
		case <-noticeC:
			noticeC = nil
			if started.Load() || streamCtx.Err() != nil {
				return
			}
			p.emit(events.Info{Message: fmt.Sprintf(
				"still synthesizing speech; waiting up to %s for the first audio", timeout)})
		case <-timer.C:
			break waiting
		}
	}

	if started.Load() || streamCtx.Err() != nil {
		return
	}

	msg := fmt.Sprintf("playback did not start within %s; recovering turn%s", timeout, p.stallHint(timeout))
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
