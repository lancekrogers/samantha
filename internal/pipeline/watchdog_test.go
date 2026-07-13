package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/brain"
	"github.com/lancekrogers/samantha/internal/events"
)

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
		if !errors.Is(err, errPlaybackStalled) {
			t.Fatalf("RunTurn() error = %v, want errPlaybackStalled", err)
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
