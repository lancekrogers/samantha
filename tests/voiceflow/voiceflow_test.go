//go:build integration
// +build integration

package voiceflow

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/brain"
	"github.com/lancekrogers/samantha/internal/events"
	"github.com/lancekrogers/samantha/internal/pipeline"
	"github.com/lancekrogers/samantha/internal/stt"
	"github.com/lancekrogers/samantha/internal/tts"
)

// testBargeInArmDelay mirrors the pipeline's unexported bargeInArmDelay: barge-in
// is held off this long after playback starts so the echo of Samantha's own
// first words can't trip it. Keep this in sync with pipeline.bargeInArmDelay.
const testBargeInArmDelay = 600 * time.Millisecond

func TestFixtureStreamingSTTFlow(t *testing.T) {
	t.Parallel()

	bus := events.NewBus()
	var partials []string
	var userInput string
	var metrics events.TurnMetrics
	var response events.ResponseReady
	responseSeen := make(chan struct{}, 1)
	events.Subscribe(bus, func(e events.TranscriptPartial) {
		partials = append(partials, e.Text)
	})
	events.Subscribe(bus, func(e events.UserInput) {
		userInput = e.Text
	})
	events.Subscribe(bus, func(e events.TurnMetrics) {
		metrics = e
	})
	events.Subscribe(bus, func(e events.ResponseReady) {
		response = e
		select {
		case responseSeen <- struct{}{}:
		default:
		}
	})

	p := &pipeline.Pipeline{
		STT:    &fixtureStreamingSTT{fixture: fixturePath(t, "utterance.wav"), final: "hello samantha", partials: []string{"hello", "hello samantha"}},
		Brain:  &fixtureBrain{response: "All set."},
		Events: bus,
	}

	text, err := p.RunTurn(context.Background())
	if err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	if text != "hello samantha" {
		t.Fatalf("RunTurn() text = %q, want %q", text, "hello samantha")
	}
	if userInput != "hello samantha" {
		t.Fatalf("UserInput = %q, want %q", userInput, "hello samantha")
	}
	if len(partials) < 2 {
		t.Fatalf("partials = %v, want at least 2 transcript partials", partials)
	}
	select {
	case <-responseSeen:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for response event")
	}
	if response.Response != "All set." {
		t.Fatalf("ResponseReady.Response = %q, want %q", response.Response, "All set.")
	}
	if response.Interrupted {
		t.Fatal("ResponseReady.Interrupted = true, want false")
	}
	if metrics.STTFinalElapsed <= 0 {
		t.Fatalf("STTFinalElapsed = %v, want > 0", metrics.STTFinalElapsed)
	}
	if metrics.ModelCompleteElapsed <= 0 {
		t.Fatalf("ModelCompleteElapsed = %v, want > 0", metrics.ModelCompleteElapsed)
	}
}

func TestFixtureBargeInInterruptsPlayback(t *testing.T) {
	t.Parallel()

	bus := events.NewBus()
	// Playback must outlast the arm window (testBargeInArmDelay) so barge-in can
	// arm while a realistically long response is still playing.
	player := newFixturePlayer(2 * time.Second)
	defer player.Close()

	capture := newFixtureCapture()
	vad := &energyVAD{threshold: 0.03, required: 2}

	var response events.ResponseReady
	var metrics events.TurnMetrics
	responseSeen := make(chan struct{}, 1)
	events.Subscribe(bus, func(e events.ResponseReady) {
		response = e
		select {
		case responseSeen <- struct{}{}:
		default:
		}
	})
	events.Subscribe(bus, func(e events.TurnMetrics) {
		metrics = e
	})

	p := &pipeline.Pipeline{
		STT:        &instantSTT{text: "start"},
		Brain:      &fixtureBrain{response: "This response should be interrupted."},
		TTS:        &fixtureTTS{},
		Player:     player,
		Capture:    capture,
		BargeInVAD: vad,
		Events:     bus,
	}

	done := make(chan error, 1)
	go func() {
		_, err := p.RunTurn(context.Background())
		done <- err
	}()

	select {
	case <-player.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for playback start")
	}

	time.Sleep(testBargeInArmDelay + 50*time.Millisecond)
	go capture.replayWAV(t, fixturePath(t, "barge_in.wav"), 4.0)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunTurn() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for turn completion")
	}

	select {
	case <-responseSeen:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for response event")
	}

	if !response.Interrupted {
		t.Fatal("ResponseReady.Interrupted = false, want true")
	}
	if player.stopCount.Load() == 0 {
		t.Fatal("player Stop() was not called")
	}
	if !metrics.Interrupted {
		t.Fatal("TurnMetrics.Interrupted = false, want true")
	}
}

type fixtureStreamingSTT struct {
	fixture  string
	partials []string
	final    string
}

func (f *fixtureStreamingSTT) Start(ctx context.Context) (stt.Session, error) {
	source, err := audio.NewFixtureSourceFromWAV(f.fixture, audio.ChunkSize, true)
	if err != nil {
		return nil, err
	}

	sessionCtx, cancel := context.WithCancel(ctx)
	eventsCh := make(chan stt.Event, 8)
	go func() {
		defer close(eventsCh)
		defer cancel()

		start := time.Now()
		eventsCh <- stt.PhaseEvent{Phase: "listening"}
		heard := false
		emitted := 0
		total := 0

		for {
			select {
			case <-sessionCtx.Done():
				return
			default:
			}

			chunk := source.Read()
			if len(chunk) == 0 {
				break
			}

			total += len(chunk)
			if !heard && meanAbs(chunk) > 0.01 {
				heard = true
				eventsCh <- stt.PhaseEvent{Phase: "hearing", Elapsed: time.Since(start).Nanoseconds()}
			}

			progress := float64(total) / float64(audio.SampleRate)
			if heard && emitted == 0 && progress >= 0.55 {
				eventsCh <- stt.PhaseEvent{Phase: "transcribing", Elapsed: time.Since(start).Nanoseconds()}
				eventsCh <- stt.PartialTranscript{Text: f.partials[0]}
				emitted++
			}
			if heard && emitted == 1 && progress >= 0.9 {
				eventsCh <- stt.PartialTranscript{Text: f.partials[1]}
				emitted++
			}
		}

		eventsCh <- stt.FinalTranscript{Text: f.final}
	}()

	return fixtureSession{events: eventsCh, cancel: cancel}, nil
}

func (f *fixtureStreamingSTT) Available() bool { return true }

type instantSTT struct{ text string }

func (i *instantSTT) Start(ctx context.Context) (stt.Session, error) {
	eventsCh := make(chan stt.Event, 2)
	eventsCh <- stt.PhaseEvent{Phase: "listening"}
	eventsCh <- stt.FinalTranscript{Text: i.text}
	close(eventsCh)
	return fixtureSession{events: eventsCh}, nil
}

func (i *instantSTT) Available() bool { return true }

type fixtureSession struct {
	events <-chan stt.Event
	cancel context.CancelFunc
}

func (f fixtureSession) Events() <-chan stt.Event { return f.events }

func (f fixtureSession) Close() error {
	if f.cancel != nil {
		f.cancel()
	}
	return nil
}

type fixtureBrain struct{ response string }

func (f *fixtureBrain) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	chunks := make(chan string, 1)
	done := make(chan brain.StreamResult, 1)
	go func() {
		defer close(chunks)
		defer close(done)
		chunks <- f.response
		done <- brain.StreamResult{}
	}()
	return &brain.Stream{Chunks: chunks, Done: done}, nil
}

func (f *fixtureBrain) ThinkFull(ctx context.Context, input string) (string, error) {
	return f.response, nil
}

func (f *fixtureBrain) ClearHistory()                  {}
func (f *fixtureBrain) History() []brain.Turn          { return nil }
func (f *fixtureBrain) LoadHistory(turns []brain.Turn) {}

type fixtureTTS struct{}

func (f *fixtureTTS) Synthesize(ctx context.Context, text string) (*audio.PCMStream, error) {
	stream := audio.NewPCMStream(ctx)
	go func() {
		_ = stream.SetSampleRate(audio.SampleRate)
		_ = stream.Write(make([]float32, audio.SampleRate/8))
		stream.Close()
	}()
	return stream, nil
}

func (f *fixtureTTS) Available() bool                              { return true }
func (f *fixtureTTS) ListVoices(locale, gender string) []tts.Voice { return nil }

type fixturePlayer struct {
	duration  time.Duration
	started   chan struct{}
	stopCount atomic.Int32
}

func newFixturePlayer(duration time.Duration) *fixturePlayer {
	return &fixturePlayer{
		duration: duration,
		started:  make(chan struct{}, 1),
	}
}

func (p *fixturePlayer) PlayStream(ctx context.Context, stream *audio.PCMStream) (*audio.Playback, error) {
	started := make(chan struct{})
	done := make(chan audio.PlaybackResult, 1)

	go func() {
		close(started)
		select {
		case p.started <- struct{}{}:
		default:
		}

		timer := time.NewTimer(p.duration)
		defer timer.Stop()

		select {
		case <-ctx.Done():
			done <- audio.PlaybackResult{Err: ctx.Err(), Interrupted: true}
		case <-timer.C:
			done <- audio.PlaybackResult{}
		}
		close(done)
	}()

	return audio.NewPlayback(started, done), nil
}

func (p *fixturePlayer) Stop() {
	p.stopCount.Add(1)
}

func (p *fixturePlayer) IsPlaying() bool { return true }
func (p *fixturePlayer) Close() error    { return nil }

type fixtureCapture struct {
	mu      sync.RWMutex
	subs    map[int]chan []float32
	nextSub int
}

func newFixtureCapture() *fixtureCapture {
	return &fixtureCapture{
		subs: make(map[int]chan []float32),
	}
}

func (c *fixtureCapture) Subscribe(buffer int) (int, <-chan []float32) {
	if buffer <= 0 {
		buffer = 1
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextSub
	c.nextSub++
	ch := make(chan []float32, buffer)
	c.subs[id] = ch
	return id, ch
}

func (c *fixtureCapture) Unsubscribe(id int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ch, ok := c.subs[id]; ok {
		delete(c.subs, id)
		close(ch)
	}
}

func (c *fixtureCapture) Reset() {}

func (c *fixtureCapture) publish(samples []float32) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, ch := range c.subs {
		select {
		case ch <- append([]float32(nil), samples...):
		default:
		}
	}
}

func (c *fixtureCapture) replayWAV(t *testing.T, path string, speedup float64) {
	t.Helper()
	samples, sampleRate, err := audio.ReadWAVFloat32(path)
	if err != nil {
		t.Fatalf("ReadWAVFloat32(%s): %v", path, err)
	}
	if sampleRate != audio.SampleRate {
		t.Fatalf("fixture sample rate = %d, want %d", sampleRate, audio.SampleRate)
	}

	for _, chunk := range audio.ChunkSamples(samples, audio.ChunkSize) {
		c.publish(chunk)
		sleep := time.Duration(float64(len(chunk)) / float64(audio.SampleRate) * float64(time.Second) / speedup)
		time.Sleep(sleep)
	}
}

type energyVAD struct {
	mu        sync.Mutex
	threshold float64
	required  int
	speech    bool
	detected  bool
	streak    int
}

func (v *energyVAD) AcceptWaveform(samples []float32) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if meanAbs(samples) >= v.threshold {
		v.streak++
		v.speech = true
		if v.streak >= v.required {
			v.detected = true
		}
		return
	}
	v.speech = false
	v.streak = 0
}

func (v *energyVAD) IsSpeech() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.speech
}

func (v *energyVAD) IsSpeechDetected() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.detected
}

func (v *energyVAD) Clear() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.speech = false
	v.detected = false
	v.streak = 0
}

func fixturePath(t *testing.T, name string) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd(): %v", err)
	}
	return filepath.Join(wd, "testdata", name)
}

func meanAbs(samples []float32) float64 {
	sum := 0.0
	for _, sample := range samples {
		if sample < 0 {
			sum -= float64(sample)
		} else {
			sum += float64(sample)
		}
	}
	return sum / float64(max(len(samples), 1))
}
