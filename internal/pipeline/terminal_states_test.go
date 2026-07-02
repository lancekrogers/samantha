package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/events"
)

// Terminal-state coverage for the state-machine-backed turn runtime. Every turn
// must reach exactly one terminal outcome and emit exactly one terminal metrics
// event. These tests are deterministic and use no real microphone or models.
//
// The eight terminal paths from the implementation steps are covered here and in
// sibling tests:
//
//	no speech              -> TestRunTurnNoSpeechEmitsSingleMetrics (pipeline_test.go)
//	successful playback    -> TestTerminalSuccessfulPlaybackCompletesWithSingleMetrics
//	text reply without TTS -> TestTerminalTextResponseWithoutTTSCompletes
//	STT error              -> TestTerminalSTTErrorFailsWithSingleMetrics
//	brain error            -> TestRunTurnBrainErrorEmitsSingleMetrics (pipeline_test.go)
//	                          + TestTerminalTextModeBrainErrorFailsWithSingleMetrics
//	playback error         -> TestTerminalTextModePlaybackErrorCompletesDegraded
//	                          + TestRunTurnWatchdogRecoversStalledPlayback (watchdog_test.go)
//	cancellation           -> TestTerminalCancellationInterruptsWithSingleMetrics
//	barge-in               -> TestTerminalBargeInInterruptsWithSingleMetrics

// capturedMetrics counts terminal metrics events and records the last one so
// tests can assert both exactly-once emission and the machine-decided Outcome.
type capturedMetrics struct {
	n    atomic.Int32
	last atomic.Value // events.TurnMetrics
}

func (c *capturedMetrics) Load() int32 { return c.n.Load() }

func (c *capturedMetrics) Outcome() string {
	if v, ok := c.last.Load().(events.TurnMetrics); ok {
		return v.Outcome
	}
	return ""
}

// countTurnMetrics subscribes a capture for terminal metrics events.
func countTurnMetrics(bus *events.Bus) *capturedMetrics {
	c := &capturedMetrics{}
	events.Subscribe(bus, func(e events.TurnMetrics) {
		c.n.Add(1)
		c.last.Store(e)
	})
	return c
}

// failPlayer rejects every stream, modeling an unavailable output device.
type failPlayer struct{ err error }

func (p *failPlayer) PlayStream(ctx context.Context, stream *audio.PCMStream) (*audio.Playback, error) {
	return nil, p.err
}
func (p *failPlayer) Stop()           {}
func (p *failPlayer) IsPlaying() bool { return false }
func (p *failPlayer) Close() error    { return nil }

func TestTerminalSuccessfulPlaybackCompletesWithSingleMetrics(t *testing.T) {
	bus := events.NewBus()
	metrics := countTurnMetrics(bus)
	var response events.ResponseReady
	events.Subscribe(bus, func(e events.ResponseReady) { response = e })

	player := newFakePlayer(20 * time.Millisecond)
	defer player.Close()

	p := &Pipeline{
		STT:    &fakeSTT{text: "hello"},
		Brain:  &fakeBrain{chunks: []string{"All done."}},
		TTS:    &fakeTTS{delay: time.Millisecond},
		Player: player,
		Events: bus,
	}

	text, err := p.RunTurn(context.Background())
	if err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	if text != "hello" {
		t.Fatalf("RunTurn() text = %q, want %q", text, "hello")
	}
	if response.Interrupted {
		t.Fatal("ResponseReady.Interrupted = true, want false on normal completion")
	}
	if got := metrics.Load(); got != 1 {
		t.Fatalf("TurnMetrics emitted %d times, want exactly 1", got)
	}
	if got := metrics.Outcome(); got != "completed" {
		t.Fatalf("TurnMetrics.Outcome = %q, want completed", got)
	}
}

func TestTerminalTextResponseWithoutTTSCompletes(t *testing.T) {
	bus := events.NewBus()
	metrics := countTurnMetrics(bus)
	var response events.ResponseReady
	events.Subscribe(bus, func(e events.ResponseReady) { response = e })

	// No TTS or Player: the turn must still complete with a single metrics event.
	p := &Pipeline{Brain: &fakeBrain{chunks: []string{"Plain reply."}}, Events: bus}

	if err := p.RunTurnTextMode(context.Background(), "hi"); err != nil {
		t.Fatalf("RunTurnTextMode() error = %v", err)
	}
	if response.Response != "Plain reply." {
		t.Fatalf("ResponseReady.Response = %q, want %q", response.Response, "Plain reply.")
	}
	if got := metrics.Load(); got != 1 {
		t.Fatalf("TurnMetrics emitted %d times, want exactly 1", got)
	}
	if got := metrics.Outcome(); got != "completed" {
		t.Fatalf("TurnMetrics.Outcome = %q, want completed", got)
	}
}

func TestTerminalSTTErrorFailsWithSingleMetrics(t *testing.T) {
	bus := events.NewBus()
	metrics := countTurnMetrics(bus)

	p := &Pipeline{STT: &fakeSTT{err: errors.New("model load failed")}, Brain: &fakeBrain{}, Events: bus}

	_, err := p.RunTurn(context.Background())
	if err == nil || !strings.Contains(err.Error(), "STT") {
		t.Fatalf("RunTurn() error = %v, want an actionable error naming STT", err)
	}
	if got := metrics.Load(); got != 1 {
		t.Fatalf("TurnMetrics emitted %d times, want exactly 1", got)
	}
	if got := metrics.Outcome(); got != "failed" {
		t.Fatalf("TurnMetrics.Outcome = %q, want failed", got)
	}
}

func TestTerminalTextModeBrainErrorFailsWithSingleMetrics(t *testing.T) {
	bus := events.NewBus()
	metrics := countTurnMetrics(bus)

	p := &Pipeline{Brain: &fakeBrain{fullErr: errors.New("api down")}, Events: bus}

	err := p.RunTurnTextMode(context.Background(), "hi")
	if err == nil || !strings.Contains(err.Error(), "brain") {
		t.Fatalf("RunTurnTextMode() error = %v, want an actionable error naming brain", err)
	}
	if got := metrics.Load(); got != 1 {
		t.Fatalf("TurnMetrics emitted %d times, want exactly 1", got)
	}
	if got := metrics.Outcome(); got != "failed" {
		t.Fatalf("TurnMetrics.Outcome = %q, want failed", got)
	}
}

func TestTerminalTextModePlaybackErrorCompletesDegraded(t *testing.T) {
	bus := events.NewBus()
	metrics := countTurnMetrics(bus)
	var playbackErr events.Error
	sawErr := false
	events.Subscribe(bus, func(e events.Error) {
		playbackErr = e
		sawErr = true
	})

	// Voice is best-effort in text mode: a playback failure surfaces an Error
	// event but the turn still completes (returns nil) with one metrics event.
	p := &Pipeline{
		Brain:  &fakeBrain{chunks: []string{"Reply."}},
		TTS:    &fakeTTS{delay: time.Millisecond},
		Player: &failPlayer{err: errors.New("device busy")},
		Events: bus,
	}

	if err := p.RunTurnTextMode(context.Background(), "hi"); err != nil {
		t.Fatalf("RunTurnTextMode() error = %v, want nil (voice degraded, not failed)", err)
	}
	if !sawErr || playbackErr.Stage != "playback" {
		t.Fatalf("playback Error event = %+v (saw=%v), want Stage=playback", playbackErr, sawErr)
	}
	if got := metrics.Load(); got != 1 {
		t.Fatalf("TurnMetrics emitted %d times, want exactly 1", got)
	}
	if got := metrics.Outcome(); got != "completed" {
		t.Fatalf("TurnMetrics.Outcome = %q, want completed", got)
	}
}

func TestTerminalCancellationInterruptsWithSingleMetrics(t *testing.T) {
	bus := events.NewBus()
	metrics := countTurnMetrics(bus)

	// A stalled STT session blocks in the listen select until cancellation.
	p := &Pipeline{STT: &fakeSTT{stall: true}, Brain: &fakeBrain{}, Events: bus}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := p.RunTurn(ctx)
		done <- err
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunTurn() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunTurn did not return promptly after cancellation")
	}
	if got := metrics.Load(); got != 1 {
		t.Fatalf("TurnMetrics emitted %d times, want exactly 1", got)
	}
	if got := metrics.Outcome(); got != "interrupted" {
		t.Fatalf("TurnMetrics.Outcome = %q, want interrupted", got)
	}
}

func TestTerminalBargeInInterruptsWithSingleMetrics(t *testing.T) {
	bus := events.NewBus()
	metrics := countTurnMetrics(bus)
	var response events.ResponseReady
	responseSeen := make(chan struct{}, 1)
	events.Subscribe(bus, func(e events.ResponseReady) {
		response = e
		select {
		case responseSeen <- struct{}{}:
		default:
		}
	})

	player := newFakePlayer(2 * time.Second)
	defer player.Close()
	capture := newFakeCapture()
	vad := &fakeVAD{}

	p := &Pipeline{
		STT:        &fakeSTT{text: "hello"},
		Brain:      &fakeBrain{chunks: []string{"This answer should be interrupted."}},
		TTS:        &fakeTTS{delay: 10 * time.Millisecond},
		Player:     player,
		Capture:    capture,
		VAD:        &fakeVAD{},
		BargeInVAD: vad,
		Events:     bus,
	}

	done := make(chan error, 1)
	go func() {
		_, err := p.RunTurn(context.Background())
		done <- err
	}()

	select {
	case <-player.StartedSignal():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for playback to start")
	}

	time.Sleep(bargeInArmDelay + 80*time.Millisecond)
	for range bargeInMinSpeechChunks {
		capture.Publish([]float32{0.9, 0.9, 0.9})
		time.Sleep(60 * time.Millisecond)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunTurn() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("interrupted turn did not finish")
	}

	select {
	case <-responseSeen:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ResponseReady")
	}
	if !response.Interrupted {
		t.Fatal("ResponseReady.Interrupted = false, want true")
	}
	if got := metrics.Load(); got != 1 {
		t.Fatalf("TurnMetrics emitted %d times, want exactly 1", got)
	}
	if got := metrics.Outcome(); got != "interrupted" {
		t.Fatalf("TurnMetrics.Outcome = %q, want interrupted", got)
	}
}
