package pipeline

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/brain"
	"github.com/lancekrogers/samantha/internal/events"
	"github.com/lancekrogers/samantha/internal/tts"
)

// TestStallTimeoutHonorsQwenFirstAudioGrace is the regression for Uncle Fu /
// qwen3-tts degraded turns: the default 8s playback stall fired while the
// managed Qwen worker was still generating a whole utterance, aborted the
// turn, and injected "I hit an error while working on that…".
func TestStallTimeoutHonorsQwenFirstAudioGrace(t *testing.T) {
	graceful := &graceTTS{grace: 90 * time.Second}
	p := &Pipeline{TTS: graceful}
	if got := p.stallTimeout(); got != 90*time.Second {
		t.Fatalf("stallTimeout() = %v, want provider FirstAudioGrace 90s", got)
	}

	// Explicit override always wins (including short values used by tests).
	p.PlaybackStallTimeout = 150 * time.Millisecond
	if got := p.stallTimeout(); got != 150*time.Millisecond {
		t.Fatalf("stallTimeout() = %v, want explicit 150ms override", got)
	}

	// Kokoro-style providers without FirstAudioGrace keep the default.
	p = &Pipeline{TTS: &fakeTTS{}}
	if got := p.stallTimeout(); got != defaultPlaybackStallTimeout {
		t.Fatalf("stallTimeout() = %v, want default %v without grace", got, defaultPlaybackStallTimeout)
	}
}

// graceTTS is a minimal Provider that also implements tts.FirstAudioGracer.
type graceTTS struct {
	grace time.Duration
}

func (g *graceTTS) Synthesize(context.Context, string) (*audio.PCMStream, error) {
	return nil, nil
}
func (g *graceTTS) Available() bool                       { return true }
func (g *graceTTS) ListVoices(string, string) []tts.Voice { return nil }
func (g *graceTTS) FirstAudioGrace() time.Duration        { return g.grace }

// A long grace must stay legible: the watchdog says it is still waiting once
// the old 8s budget passes, so a 120s Qwen turn does not look frozen. Budgets
// at or under the default recover before the wait is worth narrating.
func TestStallNoticeDelay(t *testing.T) {
	if got := stallNoticeDelay(120 * time.Second); got != defaultPlaybackStallTimeout {
		t.Fatalf("stallNoticeDelay(120s) = %v, want %v", got, defaultPlaybackStallTimeout)
	}
	if got := stallNoticeDelay(defaultPlaybackStallTimeout); got != 0 {
		t.Fatalf("stallNoticeDelay(default) = %v, want 0 (no notice)", got)
	}
	if got := stallNoticeDelay(150 * time.Millisecond); got != 0 {
		t.Fatalf("stallNoticeDelay(150ms) = %v, want 0 (no notice)", got)
	}
}

// The recovery message may only name qwen_tts_timeout when that knob is what
// set the budget. A Kokoro turn on the default 8s is not governed by it.
func TestStallHintNamesTheKnobOnlyWhenItApplies(t *testing.T) {
	graceful := &Pipeline{TTS: &graceTTS{grace: 90 * time.Second}}
	if got := graceful.stallHint(90 * time.Second); !strings.Contains(got, "qwen_tts_timeout") {
		t.Fatalf("stallHint(grace budget) = %q, want the qwen_tts_timeout knob named", got)
	}
	// Explicit override on the same provider: the budget is no longer the grace.
	if got := graceful.stallHint(150 * time.Millisecond); strings.Contains(got, "qwen_tts_timeout") {
		t.Fatalf("stallHint(override) = %q, want no qwen_tts_timeout for a budget it did not set", got)
	}

	plain := &Pipeline{TTS: &fakeTTS{}}
	if got := plain.stallHint(defaultPlaybackStallTimeout); strings.Contains(got, "qwen_tts_timeout") {
		t.Fatalf("stallHint(default, non-gracer) = %q, want no qwen_tts_timeout", got)
	}
}

// The notice fires while the turn is still healthy and does not itself recover
// the turn — the stall budget keeps running underneath it.
func TestWatchPlaybackStallEmitsInterimNotice(t *testing.T) {
	bus := events.NewBus()
	var mu sync.Mutex
	var infos []string
	bus.SubscribeAll(func(ev events.Event) {
		if info, ok := ev.(events.Info); ok {
			mu.Lock()
			infos = append(infos, info.Message)
			mu.Unlock()
		}
	})

	p := &Pipeline{Events: bus}
	streamCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var started atomic.Bool
	stalled := make(chan struct{})
	// Notice at 30ms (scaled default), stall at 300ms.
	go p.watchPlaybackStallAfter(streamCtx, &started, cancel, stalled, 300*time.Millisecond, 30*time.Millisecond)

	select {
	case <-stalled:
		t.Fatal("watchdog recovered the turn at the notice, not the stall budget")
	case <-time.After(150 * time.Millisecond):
	}

	mu.Lock()
	got := append([]string(nil), infos...)
	mu.Unlock()
	if len(got) != 1 || !strings.Contains(got[0], "still synthesizing") {
		t.Fatalf("interim notice = %v, want one 'still synthesizing' message", got)
	}

	select {
	case <-stalled:
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog never reached the stall budget after the notice")
	}
}

func TestRunTurnWatchdogRecoversStalledPlayback(t *testing.T) {
	bus := events.NewBus()
	sttProvider := &fakeSTT{text: "hello"}
	brainProvider := &stallBrain{}
	ttsProvider := &fakeTTS{delay: time.Millisecond}
	player := &stallPlayer{}

	recovered := make(chan struct{}, 1)
	events.Subscribe(bus, func(e events.Error) {
		if strings.Contains(e.Message, "recovering turn") {
			select {
			case recovered <- struct{}{}:
			default:
			}
		}
	})

	p := &Pipeline{
		STT:                  sttProvider,
		Brain:                brainProvider,
		TTS:                  ttsProvider,
		Player:               player,
		Events:               bus,
		PlaybackStallTimeout: 150 * time.Millisecond,
	}

	done := make(chan error, 1)
	go func() {
		_, err := p.RunTurn(context.Background())
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunTurn() error = %v, want nil (stall completes degraded)", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watchdog did not recover a stalled turn")
	}

	select {
	case <-recovered:
	case <-time.After(time.Second):
		t.Fatal("recovery message was not emitted")
	}

	brainCtx := brainProvider.recordedCtx()
	if brainCtx == nil {
		t.Fatal("brain stream context was never recorded")
	}
	select {
	case <-brainCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("brain stream context was not cancelled after recovery")
	}
}

// stallBrain emits one sentence then holds the stream open until its context is
// cancelled, so the only way streamResponse can exit is the watchdog.
type stallBrain struct {
	mu  sync.Mutex
	ctx context.Context
}

func (b *stallBrain) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	b.mu.Lock()
	b.ctx = ctx
	b.mu.Unlock()

	out := make(chan string, 1)
	done := make(chan brain.StreamResult, 1)
	go func() {
		defer close(out)
		defer close(done)
		select {
		case out <- "Hello there. ":
		case <-ctx.Done():
			done <- brain.StreamResult{Err: ctx.Err()}
			return
		}
		<-ctx.Done()
		done <- brain.StreamResult{Err: ctx.Err()}
	}()
	return &brain.Stream{Chunks: out, Done: done}, nil
}

func (b *stallBrain) recordedCtx() context.Context {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ctx
}

func (b *stallBrain) ThinkFull(context.Context, string, brain.StreamOptions) (string, error) {
	return "", nil
}
func (b *stallBrain) ClearHistory()            {}
func (b *stallBrain) History() []brain.Turn    { return nil }
func (b *stallBrain) LoadHistory([]brain.Turn) {}

// stallPlayer accepts segments but never makes them audible. Its playback only
// finishes when Stop is called, mimicking a wedged playback path.
type stallPlayer struct {
	mu    sync.Mutex
	dones []chan audio.PlaybackResult
}

func (p *stallPlayer) PlayStream(ctx context.Context, stream *audio.PCMStream) (*audio.Playback, error) {
	started := make(chan struct{})
	done := make(chan audio.PlaybackResult, 1)

	p.mu.Lock()
	p.dones = append(p.dones, done)
	p.mu.Unlock()

	return audio.NewPlayback(started, done), nil
}

func (p *stallPlayer) Stop() {
	p.mu.Lock()
	dones := p.dones
	p.dones = nil
	p.mu.Unlock()

	for _, d := range dones {
		select {
		case d <- audio.PlaybackResult{Interrupted: true}:
		default:
		}
	}
}

func (p *stallPlayer) IsPlaying() bool { return false }
func (p *stallPlayer) Close() error    { return nil }
