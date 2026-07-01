package pipeline

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/brain"
	"github.com/lancekrogers/samantha/internal/events"
	"github.com/lancekrogers/samantha/internal/stt"
	"github.com/lancekrogers/samantha/internal/tts"
)

func TestRunTurnOverlapsSynthesisWithPlayback(t *testing.T) {
	bus := events.NewBus()
	sttProvider := &fakeSTT{text: "hello"}
	brainProvider := &fakeBrain{chunks: []string{"First sentence. Second sentence."}}
	ttsProvider := &fakeTTS{
		delay: time.Millisecond * 20,
	}
	player := newFakePlayer(120 * time.Millisecond)
	defer player.Close()

	var metrics events.TurnMetrics
	metricsSeen := make(chan struct{}, 1)
	events.Subscribe(bus, func(e events.TurnMetrics) {
		metrics = e
		select {
		case metricsSeen <- struct{}{}:
		default:
		}
	})

	p := &Pipeline{
		STT:    sttProvider,
		Brain:  brainProvider,
		TTS:    ttsProvider,
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

	callTimes := ttsProvider.CallTimes()
	if len(callTimes) != 2 {
		t.Fatalf("TTS call count = %d, want 2", len(callTimes))
	}

	finished := player.FinishedTimes()
	if len(finished) == 0 {
		t.Fatal("player recorded no finished segments")
	}

	if !callTimes[1].Before(finished[0]) {
		t.Fatalf("second synthesis started at %v, want before first playback finished at %v", callTimes[1], finished[0])
	}
	select {
	case <-metricsSeen:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for TurnMetrics event")
	}
	if metrics.FirstModelChunkElapsed <= 0 {
		t.Fatalf("FirstModelChunkElapsed = %v, want > 0", metrics.FirstModelChunkElapsed)
	}
	if metrics.ModelCompleteElapsed <= 0 {
		t.Fatalf("ModelCompleteElapsed = %v, want > 0", metrics.ModelCompleteElapsed)
	}
}

func TestRunTurnDrainsFullPlaybackQueue(t *testing.T) {
	bus := events.NewBus()
	sttProvider := &fakeSTT{text: "hello"}
	// More sentences than voiceQueueDepth so the playback queue fills and the
	// loop must apply backpressure without blocking — a regression guard for
	// the slotSem deadlock that hung voice mode once the queue was full.
	brainProvider := &fakeBrain{chunks: []string{"One. Two. Three. Four. Five."}}
	ttsProvider := &fakeTTS{delay: 5 * time.Millisecond}
	player := newFakePlayer(60 * time.Millisecond)
	defer player.Close()

	p := &Pipeline{
		STT:    sttProvider,
		Brain:  brainProvider,
		TTS:    ttsProvider,
		Player: player,
		Events: bus,
	}

	done := make(chan error, 1)
	go func() {
		_, err := p.RunTurn(context.Background())
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunTurn() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunTurn deadlocked with a full playback queue")
	}

	if got := len(ttsProvider.CallTimes()); got != 5 {
		t.Fatalf("TTS call count = %d, want 5", got)
	}
	if got := len(player.FinishedTimes()); got != 5 {
		t.Fatalf("played segment count = %d, want 5", got)
	}
}

func TestRunTurnBargeInInterruptsPlayback(t *testing.T) {
	bus := events.NewBus()
	sttProvider := &fakeSTT{text: "hello"}
	brainProvider := &fakeBrain{chunks: []string{"This answer should be interrupted."}}
	ttsProvider := &fakeTTS{
		delay: time.Millisecond * 10,
	}
	// Play longer than bargeInArmDelay so barge-in is still armed when speech arrives.
	player := newFakePlayer(2 * time.Second)
	defer player.Close()

	capture := newFakeCapture()
	vad := &fakeVAD{}

	var response events.ResponseReady
	responseSeen := make(chan struct{}, 1)
	events.Subscribe(bus, func(e events.ResponseReady) {
		response = e
		select {
		case responseSeen <- struct{}{}:
		default:
		}
	})

	p := &Pipeline{
		STT:        sttProvider,
		Brain:      brainProvider,
		TTS:        ttsProvider,
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
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for interrupted turn to finish")
	}

	select {
	case <-responseSeen:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ResponseReady event")
	}

	if !response.Interrupted {
		t.Fatal("ResponseReady.Interrupted = false, want true")
	}
	if player.StopCount() == 0 {
		t.Fatal("player Stop() was not called during barge-in")
	}
	// One reset from the listen-start drain; the barge-in itself must add none,
	// and it arms keepCapture so the next turn preserves the user's in-progress
	// audio instead of draining it.
	if capture.ResetCount() != 1 {
		t.Fatalf("capture.Reset() count = %d, want 1 (listen drain only)", capture.ResetCount())
	}
	if !p.keepCapture {
		t.Fatal("barge-in should arm keepCapture so the next turn preserves the user's audio")
	}
}

func TestTranscribeTurnDrainsCaptureExceptAfterBargeIn(t *testing.T) {
	capture := newFakeCapture()
	p := &Pipeline{
		STT:     &fakeSTT{text: "hello"},
		Brain:   &fakeBrain{chunks: []string{"hi there"}},
		VAD:     &fakeVAD{},
		Capture: capture,
		Events:  events.NewBus(),
		// No TTS/Player: the speak path is skipped, isolating the listen drain.
	}

	// A normal turn drains the stale capture buffer exactly once before listening.
	if _, err := p.RunTurn(context.Background()); err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	if capture.ResetCount() != 1 {
		t.Fatalf("capture.Reset() count = %d, want 1 after a normal turn", capture.ResetCount())
	}

	// Simulate a prior barge-in: the next listen must preserve the buffer.
	p.keepCapture = true
	if _, err := p.RunTurn(context.Background()); err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	if capture.ResetCount() != 1 {
		t.Fatalf("capture.Reset() count = %d, want 1 (drain skipped after barge-in)", capture.ResetCount())
	}
	if p.keepCapture {
		t.Fatal("keepCapture should clear after the preserved turn")
	}
}

func TestWatchBargeInDisabledWhenVADNil(t *testing.T) {
	// With barge-in disabled (BargeInVAD nil), watchBargeIn must not subscribe to
	// capture or return a trigger channel.
	capture := newFakeCapture()
	p := &Pipeline{
		Player:  newFakePlayer(time.Second),
		Capture: capture,
	}
	defer p.Player.(*fakePlayer).Close()

	var armAt atomic.Int64
	if ch := p.watchBargeIn(context.Background(), &armAt); ch != nil {
		t.Fatal("watchBargeIn returned a non-nil channel with BargeInVAD nil")
	}
	if got := len(capture.subs); got != 0 {
		t.Fatalf("capture subscriptions = %d, want 0 when barge-in disabled", got)
	}
}

func TestRunTurnReturnsBrainStreamError(t *testing.T) {
	bus := events.NewBus()
	sttProvider := &fakeSTT{text: "hello"}
	brainProvider := &fakeBrain{streamErr: errors.New("boom")}

	p := &Pipeline{
		STT:    sttProvider,
		Brain:  brainProvider,
		Events: bus,
	}

	_, err := p.RunTurn(context.Background())
	if err == nil {
		t.Fatal("RunTurn() error = nil, want brain stream error")
	}
	if err.Error() != "brain: boom" {
		t.Fatalf("RunTurn() error = %q, want %q", err.Error(), "brain: boom")
	}
}

type fakeSTT struct {
	text string
}

type fakeSTTSession struct {
	events chan stt.Event
}

func (s *fakeSTTSession) Events() <-chan stt.Event { return s.events }
func (s *fakeSTTSession) Close() error             { return nil }

func (f *fakeSTT) Start(ctx context.Context) (stt.Session, error) {
	eventsCh := make(chan stt.Event, 2)
	eventsCh <- stt.PhaseEvent{Phase: "listening"}
	eventsCh <- stt.FinalTranscript{Text: f.text}
	close(eventsCh)
	return &fakeSTTSession{events: eventsCh}, nil
}

func (f *fakeSTT) Available() bool { return true }

type fakeBrain struct {
	chunks    []string
	streamErr error
}

func (f *fakeBrain) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	out := make(chan string, len(f.chunks))
	done := make(chan brain.StreamResult, 1)
	go func() {
		defer close(out)
		defer close(done)
		for _, chunk := range f.chunks {
			select {
			case <-ctx.Done():
				done <- brain.StreamResult{Err: ctx.Err()}
				return
			case out <- chunk:
			}
		}
		done <- brain.StreamResult{Err: f.streamErr}
	}()
	return &brain.Stream{Chunks: out, Done: done}, nil
}

func (f *fakeBrain) ThinkFull(ctx context.Context, input string) (string, error) {
	if len(f.chunks) == 0 {
		return "", nil
	}
	return f.chunks[0], nil
}

func (f *fakeBrain) ClearHistory()            {}
func (f *fakeBrain) History() []brain.Turn    { return nil }
func (f *fakeBrain) LoadHistory([]brain.Turn) {}

type fakeTTS struct {
	mu        sync.Mutex
	delay     time.Duration
	callTimes []time.Time
}

func (f *fakeTTS) Synthesize(ctx context.Context, text string) (*audio.PCMStream, error) {
	f.mu.Lock()
	f.callTimes = append(f.callTimes, time.Now())
	f.mu.Unlock()

	stream := audio.NewPCMStream(ctx)
	go func() {
		defer func() {
			if ctx.Err() != nil {
				stream.CloseWithError(ctx.Err())
			}
		}()

		time.Sleep(f.delay)
		if ctx.Err() != nil {
			stream.CloseWithError(ctx.Err())
			return
		}

		if err := stream.SetSampleRate(24000); err != nil {
			stream.CloseWithError(err)
			return
		}
		if err := stream.Write(make([]float32, 4096)); err != nil {
			stream.CloseWithError(err)
			return
		}
		stream.Close()
	}()

	return stream, nil
}

func (f *fakeTTS) Available() bool { return true }

func (f *fakeTTS) ListVoices(locale, gender string) []tts.Voice {
	return nil
}

func (f *fakeTTS) CallTimes() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]time.Time, len(f.callTimes))
	copy(out, f.callTimes)
	return out
}

type fakePlayer struct {
	playDuration time.Duration
	notify       chan struct{}
	quit         chan struct{}
	started      chan struct{}
	playing      atomic.Bool

	mu         sync.Mutex
	active     *playbackRequest
	queue      []*playbackRequest
	finishedAt []time.Time
	stopCount  int
}

type playbackRequest struct {
	ctx     context.Context
	stop    chan struct{}
	started chan struct{}
	done    chan audio.PlaybackResult
}

func newFakePlayer(playDuration time.Duration) *fakePlayer {
	p := &fakePlayer{
		playDuration: playDuration,
		notify:       make(chan struct{}, 1),
		quit:         make(chan struct{}),
		started:      make(chan struct{}, 8),
	}
	go p.loop()
	return p
}

func (p *fakePlayer) PlayStream(ctx context.Context, stream *audio.PCMStream) (*audio.Playback, error) {
	if _, err := stream.WaitReady(ctx); err != nil {
		return nil, err
	}

	req := &playbackRequest{
		ctx:     ctx,
		stop:    make(chan struct{}),
		started: make(chan struct{}),
		done:    make(chan audio.PlaybackResult, 1),
	}

	p.mu.Lock()
	p.queue = append(p.queue, req)
	p.mu.Unlock()
	p.signal()

	return audio.NewPlayback(req.started, req.done), nil
}

func (p *fakePlayer) Stop() {
	p.mu.Lock()
	p.stopCount++
	active := p.active
	queued := append([]*playbackRequest(nil), p.queue...)
	p.queue = nil
	p.mu.Unlock()

	if active != nil {
		close(active.stop)
	}
	for _, req := range queued {
		req.done <- audio.PlaybackResult{Interrupted: true}
		close(req.done)
	}
}

func (p *fakePlayer) IsPlaying() bool {
	return p.playing.Load()
}

func (p *fakePlayer) Close() error {
	close(p.quit)
	return nil
}

func (p *fakePlayer) FinishedTimes() []time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()

	out := make([]time.Time, len(p.finishedAt))
	copy(out, p.finishedAt)
	return out
}

func (p *fakePlayer) StopCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopCount
}

func (p *fakePlayer) StartedSignal() <-chan struct{} {
	return p.started
}

func (p *fakePlayer) loop() {
	for {
		req := p.nextRequest()
		if req == nil {
			select {
			case <-p.quit:
				return
			case <-p.notify:
				continue
			}
		}

		p.playing.Store(true)
		close(req.started)
		select {
		case p.started <- struct{}{}:
		default:
		}

		timer := time.NewTimer(p.playDuration)
		result := audio.PlaybackResult{}
		select {
		case <-p.quit:
			timer.Stop()
			result.Interrupted = true
		case <-req.ctx.Done():
			timer.Stop()
			result.Interrupted = true
			result.Err = req.ctx.Err()
		case <-req.stop:
			timer.Stop()
			result.Interrupted = true
		case <-timer.C:
		}

		req.done <- result
		close(req.done)
		p.playing.Store(false)

		p.mu.Lock()
		if p.active == req {
			p.active = nil
		}
		p.finishedAt = append(p.finishedAt, time.Now())
		p.mu.Unlock()
	}
}

func (p *fakePlayer) nextRequest() *playbackRequest {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.active != nil {
		return p.active
	}
	if len(p.queue) == 0 {
		return nil
	}

	p.active = p.queue[0]
	p.queue = p.queue[1:]
	return p.active
}

func (p *fakePlayer) signal() {
	select {
	case p.notify <- struct{}{}:
	default:
	}
}

type fakeCapture struct {
	mu         sync.Mutex
	subs       map[int]chan []float32
	nextID     int
	resetCount int
}

func newFakeCapture() *fakeCapture {
	return &fakeCapture{
		subs: make(map[int]chan []float32),
	}
}

func (c *fakeCapture) Subscribe(buffer int) (int, <-chan []float32) {
	if buffer <= 0 {
		buffer = 1
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	id := c.nextID
	c.nextID++
	ch := make(chan []float32, buffer)
	c.subs[id] = ch
	return id, ch
}

func (c *fakeCapture) Unsubscribe(id int) {
	c.mu.Lock()
	ch, ok := c.subs[id]
	if ok {
		delete(c.subs, id)
	}
	c.mu.Unlock()

	if ok {
		close(ch)
	}
}

func (c *fakeCapture) Reset() {
	c.mu.Lock()
	c.resetCount++
	c.mu.Unlock()
}

func (c *fakeCapture) Publish(samples []float32) {
	c.mu.Lock()
	subs := make([]chan []float32, 0, len(c.subs))
	for _, ch := range c.subs {
		subs = append(subs, ch)
	}
	c.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- samples:
		default:
		}
	}
}

func (c *fakeCapture) ResetCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resetCount
}

type fakeVAD struct {
	mu      sync.Mutex
	speech  bool
	cleared int
}

func (v *fakeVAD) AcceptWaveform(samples []float32) {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.speech = false
	for _, sample := range samples {
		if sample > 0.5 {
			v.speech = true
			break
		}
	}
}

func (v *fakeVAD) IsSpeech() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.speech
}

func (v *fakeVAD) IsSpeechDetected() bool {
	return false
}

func (v *fakeVAD) Clear() {
	v.mu.Lock()
	v.speech = false
	v.cleared++
	v.mu.Unlock()
}
