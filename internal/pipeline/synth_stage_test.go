package pipeline

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/events"
	"github.com/lancekrogers/samantha/internal/tts"
)

// errTTS fails every synthesis, modeling an unavailable or broken TTS backend.
type errTTS struct{ err error }

func (e *errTTS) Synthesize(ctx context.Context, text string) (*audio.PCMStream, error) {
	return nil, e.err
}
func (e *errTTS) Available() bool                              { return true }
func (e *errTTS) ListVoices(locale, gender string) []tts.Voice { return nil }

// bufferingPlayer lets a test mute output after PlayStream begins but before
// it returns, matching the complete-sentence buffering window in audio.Player.
type bufferingPlayer struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	stops   atomic.Int32
}

func newBufferingPlayer() *bufferingPlayer {
	return &bufferingPlayer{entered: make(chan struct{}), release: make(chan struct{})}
}

func (p *bufferingPlayer) PlayStream(context.Context, *audio.PCMStream) (*audio.Playback, error) {
	p.once.Do(func() { close(p.entered) })
	<-p.release
	return audio.NewPlayback(make(chan struct{}), make(chan audio.PlaybackResult, 1)), nil
}
func (p *bufferingPlayer) Stop()           { p.stops.Add(1) }
func (p *bufferingPlayer) IsPlaying() bool { return false }
func (p *bufferingPlayer) Close() error    { return nil }

func drainPlaybackKinds(t *testing.T, out <-chan playbackEvent, n int) []playbackEventType {
	t.Helper()
	got := make([]playbackEventType, 0, n)
	timeout := time.After(2 * time.Second)
	for len(got) < n {
		select {
		case e := <-out:
			got = append(got, e.kind)
		case <-timeout:
			t.Fatalf("timed out: saw %d of %d playback events", len(got), n)
		}
	}
	return got
}

func TestSynthesizeSegmentEnqueuesPlayback(t *testing.T) {
	bus := events.NewBus()
	var sawSegment, sawGen atomic.Bool
	events.Subscribe(bus, func(events.SpeechSegmentReady) { sawSegment.Store(true) })
	events.Subscribe(bus, func(events.GeneratingVoice) { sawGen.Store(true) })

	player := newFakePlayer(15 * time.Millisecond)
	defer player.Close()
	p := &Pipeline{TTS: &fakeTTS{delay: time.Millisecond}, Player: player, Events: bus}

	var audioStarted atomic.Bool
	out := make(chan playbackEvent, 4)

	if !p.synthesizeSegment(context.Background(), make(chan struct{}), "hello world", &audioStarted, out) {
		t.Fatal("synthesizeSegment returned false, want true (playback enqueued)")
	}
	if !sawSegment.Load() || !sawGen.Load() {
		t.Fatalf("segment events: ready=%v gen=%v, want both", sawSegment.Load(), sawGen.Load())
	}

	kinds := drainPlaybackKinds(t, out, 2)
	if kinds[0] != playbackStarted || kinds[1] != playbackFinished {
		t.Fatalf("playback events = %v, want [started finished]", kinds)
	}
	if !audioStarted.Load() {
		t.Fatal("audioStarted was not set by the playback watcher")
	}
}

// A producer that keeps writing after Synthesize returns models the real
// Kokoro path: mute-after-synth must drain the stream or the producer blocks
// on the buffered frames channel forever.
type floodingTTS struct {
	started chan struct{}
	done    chan struct{}
}

func (f *floodingTTS) Synthesize(ctx context.Context, text string) (*audio.PCMStream, error) {
	stream := audio.NewPCMStream(ctx)
	if err := stream.SetSampleRate(24000); err != nil {
		return nil, err
	}
	go func() {
		defer close(f.done)
		defer stream.Close()
		close(f.started)
		// Write more frames than the stream buffer (8) so a non-draining
		// consumer leaves this goroutine blocked in Write.
		for i := 0; i < 32; i++ {
			if err := stream.Write(make([]float32, 256)); err != nil {
				return
			}
		}
	}()
	return stream, nil
}
func (f *floodingTTS) Available() bool                              { return true }
func (f *floodingTTS) ListVoices(locale, gender string) []tts.Voice { return nil }

func TestDiscardPCMStreamUnblocksFloodingProducer(t *testing.T) {
	flood := &floodingTTS{started: make(chan struct{}), done: make(chan struct{})}
	stream, err := flood.Synthesize(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	select {
	case <-flood.started:
	case <-time.After(time.Second):
		t.Fatal("producer never started")
	}
	// Same helper synthesizeSegment / RunTurnTextMode use on mute short-circuit.
	discardPCMStream(stream)

	select {
	case <-flood.done:
	case <-time.After(2 * time.Second):
		t.Fatal("producer still blocked — stream was not drained after mute")
	}
}

func TestSynthesizeSegmentSkipsWhenMutedBeforeSynth(t *testing.T) {
	p := &Pipeline{TTS: &fakeTTS{}, Player: newFakePlayer(time.Millisecond), Events: events.NewBus()}
	defer p.Player.(*fakePlayer).Close()
	p.SetOutputMuted(true)

	var audioStarted atomic.Bool
	out := make(chan playbackEvent, 1)
	if p.synthesizeSegment(context.Background(), make(chan struct{}), "skip", &audioStarted, out) {
		t.Fatal("muted synthesizeSegment enqueued playback")
	}
	if len(out) != 0 {
		t.Fatalf("muted path enqueued %d playback events", len(out))
	}
}

func TestSynthesizeSegmentStopsAudioEnqueuedAfterMute(t *testing.T) {
	player := newBufferingPlayer()
	p := &Pipeline{TTS: &fakeTTS{}, Player: player, Events: events.NewBus()}
	var audioStarted atomic.Bool
	result := make(chan bool, 1)

	go func() {
		result <- p.synthesizeSegment(context.Background(), make(chan struct{}), "late sentence", &audioStarted, make(chan playbackEvent, 1))
	}()

	select {
	case <-player.entered:
	case <-time.After(time.Second):
		t.Fatal("PlayStream did not enter buffering window")
	}
	p.SetOutputMuted(true)
	close(player.release)

	select {
	case enqueued := <-result:
		if enqueued {
			t.Fatal("late playback reported as enqueued after output was muted")
		}
	case <-time.After(time.Second):
		t.Fatal("synthesizeSegment did not return after buffering completed")
	}
	if got := player.stops.Load(); got != 2 {
		t.Fatalf("Stop calls = %d, want 2 (initial mute and late-enqueue guard)", got)
	}
}

func TestSynthesizeSegmentTTSErrorEmitsErrorAndSkips(t *testing.T) {
	bus := events.NewBus()
	var errEvent events.Error
	sawErr := false
	events.Subscribe(bus, func(e events.Error) {
		errEvent = e
		sawErr = true
	})

	p := &Pipeline{TTS: &errTTS{err: errors.New("model missing")}, Player: newFakePlayer(time.Millisecond), Events: bus}
	defer p.Player.(*fakePlayer).Close()

	var audioStarted atomic.Bool
	out := make(chan playbackEvent, 1)
	if p.synthesizeSegment(context.Background(), make(chan struct{}), "hi", &audioStarted, out) {
		t.Fatal("synthesizeSegment returned true on TTS error, want false")
	}
	if !sawErr || errEvent.Stage != "tts" {
		t.Fatalf("Error event = %+v (saw=%v), want Stage=tts", errEvent, sawErr)
	}
	if len(out) != 0 {
		t.Fatalf("playback enqueued %d events on TTS error, want 0", len(out))
	}
}

func TestSynthesizeSegmentPlaybackErrorEmitsError(t *testing.T) {
	bus := events.NewBus()
	var errEvent events.Error
	sawErr := false
	events.Subscribe(bus, func(e events.Error) {
		errEvent = e
		sawErr = true
	})

	p := &Pipeline{TTS: &fakeTTS{delay: time.Millisecond}, Player: &failPlayer{err: errors.New("device busy")}, Events: bus}

	var audioStarted atomic.Bool
	out := make(chan playbackEvent, 1)
	if p.synthesizeSegment(context.Background(), make(chan struct{}), "hi", &audioStarted, out) {
		t.Fatal("synthesizeSegment returned true on playback error, want false")
	}
	if !sawErr || errEvent.Stage != "playback" {
		t.Fatalf("Error event = %+v (saw=%v), want Stage=playback", errEvent, sawErr)
	}
}

func TestSynthesizeSegmentSuppressesErrorsAfterCancel(t *testing.T) {
	// After the turn context is canceled (cancel or barge-in), synthesis
	// failures must not produce noisy Error events.
	bus := events.NewBus()
	sawErr := false
	events.Subscribe(bus, func(events.Error) { sawErr = true })

	p := &Pipeline{TTS: &errTTS{err: errors.New("model missing")}, Player: newFakePlayer(time.Millisecond), Events: bus}
	defer p.Player.(*fakePlayer).Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var audioStarted atomic.Bool
	out := make(chan playbackEvent, 1)
	if p.synthesizeSegment(ctx, make(chan struct{}), "hi", &audioStarted, out) {
		t.Fatal("synthesizeSegment returned true with canceled ctx, want false")
	}
	if sawErr {
		t.Fatal("Error event emitted after cancellation, want suppressed")
	}
}

func TestApplyPlaybackEventStartedUpdatesMetrics(t *testing.T) {
	bus := events.NewBus()
	var sawVoice, sawSpeaking bool
	events.Subscribe(bus, func(events.VoiceGenerated) { sawVoice = true })
	events.Subscribe(bus, func(events.SpeakingStarted) { sawSpeaking = true })

	p := &Pipeline{Events: bus}
	metrics := newTurnMetrics()
	var armAt atomic.Int64

	finished := p.applyPlaybackEvent(playbackEvent{
		kind:         playbackStarted,
		sentence:     "hi",
		synthElapsed: 5 * time.Millisecond,
	}, metrics, &armAt)

	if finished {
		t.Fatal("applyPlaybackEvent(started) = true, want false")
	}
	if metrics.firstAudioReady.IsZero() || metrics.playbackStart.IsZero() {
		t.Fatal("playbackStarted did not populate first-audio/playback-start metrics")
	}
	if armAt.Load() == 0 {
		t.Fatal("playbackStarted did not arm barge-in")
	}
	if !sawVoice || !sawSpeaking {
		t.Fatalf("events: voice=%v speaking=%v, want both", sawVoice, sawSpeaking)
	}
}

func TestApplyPlaybackEventFinishedUpdatesMetrics(t *testing.T) {
	bus := events.NewBus()
	var complete events.SpeakingComplete
	sawComplete := false
	events.Subscribe(bus, func(e events.SpeakingComplete) {
		complete = e
		sawComplete = true
	})

	p := &Pipeline{Events: bus}
	metrics := newTurnMetrics()
	var armAt atomic.Int64

	finished := p.applyPlaybackEvent(playbackEvent{
		kind:        playbackFinished,
		sentence:    "hi",
		playElapsed: 30 * time.Millisecond,
	}, metrics, &armAt)

	if !finished {
		t.Fatal("applyPlaybackEvent(finished) = false, want true")
	}
	if metrics.playbackComplete.IsZero() {
		t.Fatal("playbackFinished did not set playbackComplete metric")
	}
	if !sawComplete || complete.Interrupted || complete.Elapsed != 30*time.Millisecond {
		t.Fatalf("SpeakingComplete = %+v (saw=%v), want {Elapsed:30ms Interrupted:false}", complete, sawComplete)
	}
}

func TestApplyPlaybackEventFinishedInterrupted(t *testing.T) {
	bus := events.NewBus()
	var complete events.SpeakingComplete
	events.Subscribe(bus, func(e events.SpeakingComplete) { complete = e })

	p := &Pipeline{Events: bus}
	var armAt atomic.Int64

	finished := p.applyPlaybackEvent(playbackEvent{
		kind:   playbackFinished,
		result: audio.PlaybackResult{Interrupted: true},
	}, newTurnMetrics(), &armAt)

	if !finished {
		t.Fatal("interrupted finish should still return true")
	}
	if !complete.Interrupted {
		t.Fatal("SpeakingComplete.Interrupted = false, want true")
	}
}

func TestApplyPlaybackEventFinishedErrorEmitsError(t *testing.T) {
	bus := events.NewBus()
	var errEvent events.Error
	sawErr := false
	events.Subscribe(bus, func(e events.Error) {
		errEvent = e
		sawErr = true
	})

	p := &Pipeline{Events: bus}
	var armAt atomic.Int64

	p.applyPlaybackEvent(playbackEvent{
		kind:   playbackFinished,
		result: audio.PlaybackResult{Err: errors.New("underrun")},
	}, newTurnMetrics(), &armAt)

	if !sawErr || errEvent.Stage != "playback" {
		t.Fatalf("Error event = %+v (saw=%v), want Stage=playback", errEvent, sawErr)
	}
}
