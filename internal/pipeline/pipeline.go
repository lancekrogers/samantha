package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/brain"
	"github.com/lancekrogers/samantha/internal/events"
	"github.com/lancekrogers/samantha/internal/stt"
	"github.com/lancekrogers/samantha/internal/tts"
)

const voiceQueueDepth = 2

// defaultBrainTurnTimeout bounds model + tool work for one conversational
// turn. Without this, a hung Ollama/Claude stream after tools leaves the TUI
// stuck on "thinking" forever while listening never resumes.
const defaultBrainTurnTimeout = 3 * time.Minute

type captureMonitor interface {
	Subscribe(buffer int) (int, <-chan []float32)
	Unsubscribe(id int)
	Reset()
}

type voiceDetector interface {
	AcceptWaveform(samples []float32)
	IsSpeech() bool
	IsSpeechDetected() bool
	Clear()
}

// Pipeline orchestrates the voice conversation loop.
type Pipeline struct {
	STT   stt.Provider
	Brain brain.Provider
	TTS   tts.Provider
	// TTSFallback is an optional, explicitly configured provider used for one
	// affected sentence when the selected provider fails before playback.
	// Kokoro is constructed here when voice_fallback_provider=kokoro.
	TTSFallback       tts.Provider
	ttsMu             sync.RWMutex
	Player            audio.Engine
	Capture           captureMonitor
	VAD               voiceDetector
	BargeInVAD        voiceDetector
	Events            *events.Bus
	VoiceToolsEnabled bool
	OnTurn            func() // called after each completed turn for session auto-save

	// PlaybackStallTimeout overrides the watchdog timeout; zero uses the default.
	PlaybackStallTimeout time.Duration

	// BrainTurnTimeout bounds ThinkStream (model + tools) per turn. Zero uses
	// defaultBrainTurnTimeout; negative disables the bound.
	BrainTurnTimeout time.Duration

	// keepCapture preserves the capture buffer into the next turn after a
	// barge-in, where the buffered audio is the user already mid-utterance.
	keepCapture bool

	// outputMuted is toggled by interactive clients while a turn may be active.
	outputMuted atomic.Bool
}

// brainTimeout returns the per-turn model deadline and whether it is armed.
func (p *Pipeline) brainTimeout() (time.Duration, bool) {
	if p.BrainTurnTimeout < 0 {
		return 0, false
	}
	if p.BrainTurnTimeout > 0 {
		return p.BrainTurnTimeout, true
	}
	return defaultBrainTurnTimeout, true
}

// withBrainTimeout derives a child context that expires if the model/tools
// stall. Parent cancellation still wins immediately.
func (p *Pipeline) withBrainTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if d, ok := p.brainTimeout(); ok {
		return context.WithTimeout(ctx, d)
	}
	return context.WithCancel(ctx)
}

// SetOutputMuted enables or disables spoken responses without rebuilding the
// pipeline. Muting immediately stops any audio already in flight.
func (p *Pipeline) SetOutputMuted(muted bool) {
	p.outputMuted.Store(muted)
	if muted && p.Player != nil {
		p.Player.Stop()
	}
}

// OutputMuted reports whether spoken responses are currently disabled.
func (p *Pipeline) OutputMuted() bool { return p.outputMuted.Load() }

// ReplaceTTS atomically changes the providers used by subsequent synthesis
// calls. Callers own provider cleanup and must keep retired providers alive
// until any already-started utterance has drained.
func (p *Pipeline) ReplaceTTS(primary, fallback tts.Provider) {
	p.ttsMu.Lock()
	p.TTS = primary
	p.TTSFallback = fallback
	p.ttsMu.Unlock()
}

// HasTTS reports whether a primary voice provider is configured.
func (p *Pipeline) HasTTS() bool {
	primary, _ := p.ttsProviders()
	return primary != nil
}

func (p *Pipeline) ttsProviders() (tts.Provider, tts.Provider) {
	p.ttsMu.RLock()
	defer p.ttsMu.RUnlock()
	return p.TTS, p.TTSFallback
}

func (p *Pipeline) ttsReady() bool {
	primary, _ := p.ttsProviders()
	return primary != nil && primary.Available()
}

type playbackEventType int

const (
	playbackStarted playbackEventType = iota
	playbackFinished
	// playbackNotEnqueued reports a handed-off sentence that produced no
	// playback (TTS/playback error, or synthesis canceled), so the loop can
	// release its pending slot.
	playbackNotEnqueued
)

type playbackEvent struct {
	kind         playbackEventType
	sentence     string
	synthElapsed time.Duration
	playElapsed  time.Duration
	result       audio.PlaybackResult
}

type turnMetrics struct {
	start            time.Time
	sttFinal         time.Time
	firstModelChunk  time.Time
	modelComplete    time.Time
	firstSegment     time.Time
	firstAudioReady  time.Time
	playbackStart    time.Time
	playbackComplete time.Time
	bargeIn          time.Time
	interrupted      bool
}

func newTurnMetrics() *turnMetrics {
	return &turnMetrics{start: time.Now()}
}

func (m *turnMetrics) snapshot() events.TurnMetrics {
	return events.TurnMetrics{
		Interrupted:             m.interrupted,
		STTFinalElapsed:         m.elapsed(m.sttFinal),
		FirstModelChunkElapsed:  m.elapsed(m.firstModelChunk),
		ModelCompleteElapsed:    m.elapsed(m.modelComplete),
		FirstSegmentElapsed:     m.elapsed(m.firstSegment),
		FirstAudioReadyElapsed:  m.elapsed(m.firstAudioReady),
		PlaybackStartElapsed:    m.elapsed(m.playbackStart),
		PlaybackCompleteElapsed: m.elapsed(m.playbackComplete),
		BargeInElapsed:          m.elapsed(m.bargeIn),
	}
}

func (m *turnMetrics) elapsed(ts time.Time) time.Duration {
	if ts.IsZero() {
		return 0
	}
	return ts.Sub(m.start)
}

// turnConductor bridges the turn state machine into the live pipeline: stages
// report state transitions and it guarantees exactly one terminal TurnMetrics
// emission per turn, regardless of which return path ends the turn.
type turnConductor struct {
	p        *Pipeline
	machine  *turnMachine
	metrics  *turnMetrics
	finished bool
}

func (p *Pipeline) newTurnConductor(metrics *turnMetrics) *turnConductor {
	return &turnConductor{p: p, machine: newTurnMachine(), metrics: metrics}
}

// to advances the lifecycle state, ignoring illegal or out-of-order transitions
// so a late stage signal cannot corrupt the turn outcome.
func (c *turnConductor) to(state TurnState) { c.machine.To(state) }

// finish moves the machine to its terminal state and emits the single terminal
// metrics event. Later calls are no-ops, so every return path can call it.
func (c *turnConductor) finish(terminal TurnState) {
	if c.finished {
		return
	}
	c.finished = true
	c.machine.To(terminal)
	// The machine is the outcome authority: the emitted metrics carry the
	// terminal state it settled on. A rejected transition (late or illegal
	// signal) falls back to the caller's intent so the outcome is never a
	// non-terminal state.
	outcome, ok := c.machine.Terminal()
	if !ok {
		outcome = terminal
	}
	m := c.metrics.snapshot()
	m.Outcome = outcome.String()
	c.p.emit(m)
}

// StopPlayback aborts audible TTS immediately. Used by the TUI when the user
// types to barge in so speech does not continue until the turn context drains.
func (p *Pipeline) StopPlayback() {
	if p == nil || p.Player == nil {
		return
	}
	p.Player.Stop()
}

// RunTurn executes one conversational turn with streaming TTS.
// Returns the user's input text, or empty string if no speech was detected.
func (p *Pipeline) RunTurn(ctx context.Context) (string, error) {
	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)

	if p.Player != nil {
		p.Player.Stop()
	}

	turn.to(TurnListening)
	text, err := p.transcribeTurn(ctx, metrics, turn)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			turn.finish(TurnInterrupted)
		} else {
			turn.finish(TurnFailed)
		}
		return "", err
	}
	if text == "" {
		turn.finish(TurnTimedOut)
		return "", nil
	}

	turn.to(TurnThinking)
	p.emit(events.UserInput{Text: text})
	p.emit(events.ThinkingStarted{})

	// turnCtx scopes the whole turn so the playback watchdog can abort the brain
	// stream too — not just the derived playback context. Brain work is further
	// bounded so a hung model after tools cannot wedge the conversation loop.
	turnCtx, turnCancel := p.withBrainTimeout(ctx)
	defer turnCancel()

	brainStream, err := p.Brain.ThinkStream(turnCtx, text, brain.StreamOptions{
		VoiceMode:    true,
		ToolsEnabled: p.VoiceToolsEnabled,
		OnToolStart:  p.toolStartHook(),
		OnToolEnd:    p.toolEndHook(),
	})
	if err != nil {
		turn.finish(TurnFailed)
		return text, fmt.Errorf("brain: %w", err)
	}

	fullResponse, interrupted, err := p.streamResponse(turnCtx, turnCancel, brainStream, true, metrics, turn)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			// Model/tool stall — distinct from STT timed_out (no speech).
			p.emit(events.Error{Stage: "brain", Message: "model timed out before finishing a reply"})
			turn.finish(TurnFailed)
			return text, fmt.Errorf("brain: %w", err)
		}
		if interrupted || errors.Is(err, context.Canceled) {
			turn.finish(TurnInterrupted)
		} else {
			turn.finish(TurnFailed)
		}
		return text, err
	}

	// On a barge-in the user is already speaking their next turn into the mic;
	// keep that audio instead of draining it when the next listen begins.
	p.keepCapture = interrupted

	p.emit(events.ResponseReady{
		Response:    fullResponse,
		Interrupted: interrupted,
	})
	if interrupted {
		turn.finish(TurnInterrupted)
	} else {
		turn.finish(TurnCompleted)
	}

	if p.OnTurn != nil {
		p.OnTurn()
	}

	return text, nil
}

// completeTextTurn finishes a successful text-mode turn exactly once:
// ResponseReady for the transcript, terminal metrics via finish, and OnTurn
// for session auto-save. Every degraded and mute short-circuit must use this
// so finish logic cannot drift across early returns.
func (p *Pipeline) completeTextTurn(turn *turnConductor, response string) {
	p.emit(events.ResponseReady{Response: response})
	turn.finish(TurnCompleted)
	if p.OnTurn != nil {
		p.OnTurn()
	}
}

// RunTurnTextMode runs a turn with text input instead of mic.
func (p *Pipeline) RunTurnTextMode(ctx context.Context, input string) error {
	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)

	turn.to(TurnThinking)
	p.emit(events.UserInput{Text: input})
	p.emit(events.ThinkingStarted{})
	thinkingStarted := time.Now()

	// Bound model + tools so a hung Ollama chat after tool results cannot leave
	// the TUI forever on "thinking" / last tool status.
	brainCtx, brainCancel := p.withBrainTimeout(ctx)
	defer brainCancel()

	stream, err := p.Brain.ThinkStream(brainCtx, input, brain.StreamOptions{
		ToolsEnabled: p.VoiceToolsEnabled,
		OnToolStart:  p.toolStartHook(),
		OnToolEnd:    p.toolEndHook(),
	})
	if err != nil {
		turn.finish(TurnFailed)
		return fmt.Errorf("brain: %w", err)
	}

	// Stream the reply so the TUI can render it token-by-token, then fall back
	// to the existing whole-response synthesis path below. ResponseStreamingStarted
	// and per-chunk ResponseDelta events are emitted inside observeStream.
	response, err := p.collectTextStream(brainCtx, stream, metrics)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			p.emit(events.Error{Stage: "brain", Message: "model timed out before finishing a reply"})
		}
		turn.finish(TurnFailed)
		return fmt.Errorf("brain: %w", err)
	}

	metrics.modelComplete = time.Now()
	p.emit(events.ThinkingComplete{
		Response: response,
		Elapsed:  time.Since(thinkingStarted),
	})

	if !p.OutputMuted() && p.Player != nil && p.ttsReady() {
		turn.to(TurnSpeaking)
		if metrics.firstSegment.IsZero() {
			metrics.firstSegment = time.Now()
		}
		p.emit(events.SpeechSegmentReady{Text: response})
		p.emit(events.GeneratingVoice{Sentence: response})

		synthStarted := time.Now()
		stream, usedFallback, err := p.synthesizeWithFallback(ctx, response)
		if err != nil {
			// Voice is best-effort in text mode: the text response still
			// completed, so the turn is completed (degraded), not failed.
			p.emit(events.Error{Stage: "tts", Message: fmt.Sprintf("TTS: %v", err)})
			p.completeTextTurn(turn, response)
			return nil
		}
		if p.OutputMuted() {
			// Drop the stream so the synth producer is not left blocked on a
			// full frames channel after we skip PlayStream.
			discardPCMStream(stream)
			p.completeTextTurn(turn, response)
			return nil
		}

		playback, err := p.Player.PlayStream(ctx, stream)
		if err != nil {
			if !usedFallback {
				playback, usedFallback, err = p.playFallback(ctx, response, err)
			}
			if err == nil {
				// The fallback playback is ready; continue through the normal
				// lifecycle below.
			} else {
				// Engine.PlayStream owns the stream once called (even on error) and
				// must drain it; the pipeline does not discard here.
				p.emit(events.Error{Stage: "playback", Message: fmt.Sprintf("playback: %v", err)})
				p.completeTextTurn(turn, response)
				return nil
			}
		}
		// PlayStream waits for the complete sentence buffer. Output may have
		// been muted while it was waiting, after SetOutputMuted's first Stop
		// saw an empty queue. Stop again so that late-enqueued audio cannot play.
		if p.OutputMuted() {
			p.Player.Stop()
			p.completeTextTurn(turn, response)
			return nil
		}

		p.handlePlaybackLifecycle(response, synthStarted, playback, metrics)
	}

	p.completeTextTurn(turn, response)
	return nil
}

func (p *Pipeline) transcribeTurn(ctx context.Context, metrics *turnMetrics, turn *turnConductor) (string, error) {
	if p.STT == nil {
		return "", errors.New("STT provider is not configured")
	}

	// Drain echo and silence captured while Samantha spoke — including the tail
	// that keeps hitting the mic after playback stops — so this turn hears live
	// speech, not stale audio. Skip right after a barge-in, where the buffered
	// audio is the user already mid-utterance.
	if p.Capture != nil && !p.keepCapture {
		p.Capture.Reset()
	}
	p.keepCapture = false

	session, err := p.STT.Start(ctx)
	if err != nil {
		return "", fmt.Errorf("STT: %w", err)
	}
	defer session.Close()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case event, ok := <-session.Events():
			if !ok {
				return "", nil
			}

			// InputLevel is droppable UI meter data; handle before the typed
			// switch so high-rate samples never participate in turn control.
			if lvl, ok := event.(stt.InputLevel); ok {
				p.emit(events.AudioLevel{Source: "input", Level: lvl.Level})
				continue
			}

			te := stt.ToTyped(event)
			switch te.Kind {
			case stt.KindPhase:
				if te.Phase == "transcribing" {
					turn.to(TurnTranscribing)
				}
				p.emit(events.STTPhase{
					Phase:   te.Phase,
					Elapsed: te.Elapsed,
				})
			case stt.KindPartialTranscript:
				if te.Text != "" {
					p.emit(events.TranscriptPartial{Text: te.Text})
				}
			case stt.KindFinalTranscript:
				metrics.sttFinal = time.Now()
				return te.Text, nil
			case stt.KindTimeout:
				return "", nil
			case stt.KindFailure:
				// Failure needs the error value (errors.Is + %w), not ErrText.
				fail, _ := event.(stt.Failure)
				if errors.Is(fail.Err, context.Canceled) && ctx.Err() != nil {
					return "", ctx.Err()
				}
				return "", fmt.Errorf("STT: %w", fail.Err)
			case stt.KindInputLevel:
				p.emit(events.AudioLevel{Source: "input", Level: te.Level})
			}
		}
	}
}

func (p *Pipeline) streamResponse(ctx context.Context, cancelTurn context.CancelFunc, stream *brain.Stream, allowBargeIn bool, metrics *turnMetrics, turn *turnConductor) (string, bool, error) {
	streamCtx, cancel := context.WithCancel(ctx)

	// loopDone closes when this function returns — the moment the loop stops
	// draining playbackEvents. Playback watchers select on it so their sends
	// cannot block (and leak) after an early return (ctx cancel, stall, or brain
	// error). It is distinct from streamCtx on purpose: a barge-in cancels
	// streamCtx but the loop keeps reading playbackEvents to drain pending
	// playbacks, so watchers must still deliver their finished events.
	loopDone := make(chan struct{})
	defer close(loopDone)

	// Two narrow upstream stages feed the turn loop: the model observer forwards
	// brain chunks (stamping first-chunk metrics), then the segmenter turns that
	// chunk stream into voice-ready sentences. Both are testable in isolation.
	streamedChunks, observeDone := p.observeStream(streamCtx, stream, metrics)
	// Join the observer before returning: it stamps firstModelChunk, which the
	// caller reads via snapshot() in finish(). Cancel first so the goroutine
	// unblocks, then wait for it to exit. cancel is idempotent — the barge-in and
	// normal-completion paths also call it. This runs after close(loopDone).
	defer func() {
		cancel()
		<-observeDone
	}()
	sentences := brain.ChunkSentences(streamedChunks)

	var fullResponse strings.Builder
	var interrupted bool
	var pending int
	var bargeIn <-chan interruptRequest
	var bargeDone <-chan struct{}
	var armAt atomic.Int64
	armAt.Store(time.Now().Add(24 * time.Hour).UnixNano())

	if allowBargeIn {
		watch := p.newInterruptController().watchWithDone(streamCtx, &armAt)
		bargeIn = watch.requests
		bargeDone = watch.done
		// Join the watcher on every exit path — including early returns via
		// outer cancellation, playback stall, and brain error — so its capture
		// subscription and VAD Clear never overlap the next turn. Cancel first
		// so it unblocks; joining twice is fine (done is closed).
		defer func() {
			if bargeDone == nil {
				return
			}
			cancel()
			<-bargeDone
		}()
	}

	playbackEvents := make(chan playbackEvent, voiceQueueDepth*2)
	modelDone := stream.Done
	modelFinished := false

	var audioStarted atomic.Bool
	stalled := make(chan struct{})
	watchdogArmed := false

	// Synthesis runs on a single ordered worker so this loop keeps servicing
	// barge-in and playback events while a sentence is being generated —
	// PlayStream cannot return until the TTS engine has produced audio, and
	// Kokoro generates a whole sentence per uncancellable cgo call. One worker
	// (not one goroutine per sentence) preserves playback order. Backpressure is
	// unchanged: the pending gate keeps at most voiceQueueDepth sentences
	// outstanding, so the buffered handoff below never blocks.
	synthQueue := make(chan string, voiceQueueDepth)
	var synthQueueOnce sync.Once
	closeSynthQueue := func() { synthQueueOnce.Do(func() { close(synthQueue) }) }
	defer closeSynthQueue()
	go func() {
		for sentence := range synthQueue {
			if !p.synthesizeSegment(streamCtx, loopDone, sentence, &audioStarted, playbackEvents) {
				// No playback was enqueued: release the pending slot so the
				// loop's accounting and the intake gate stay correct.
				sendPlaybackEvent(loopDone, playbackEvents, playbackEvent{kind: playbackNotEnqueued, sentence: sentence})
			}
		}
	}()

	for sentences != nil || pending > 0 || modelDone != nil {
		// Pause sentence intake while the playback queue is full so the loop
		// always returns to select and can drain playbackEvents. Blocking here
		// to acquire a slot would deadlock the turn once the queue fills.
		var sentenceCh <-chan string
		if pending < voiceQueueDepth {
			sentenceCh = sentences
		}

		select {
		case <-ctx.Done():
			if interrupted {
				return strings.TrimSpace(fullResponse.String()), interrupted, nil
			}
			return strings.TrimSpace(fullResponse.String()), interrupted, ctx.Err()

		case <-stalled:
			return strings.TrimSpace(fullResponse.String()), interrupted, errPlaybackStalled

		case req := <-bargeIn:
			if interrupted {
				continue
			}
			interrupted = true
			metrics.interrupted = true
			metrics.bargeIn = req.At
			turn.to(TurnInterrupted)
			cancel()
			if cancelTurn != nil {
				cancelTurn()
			}
			if p.Player != nil {
				p.Player.Stop()
			}
			p.emit(events.SpeakingInterrupted{Reason: req.Reason})
			p.emit(events.TurnInterrupted{Reason: req.Reason})

		case result, ok := <-modelDone:
			if !ok {
				modelDone = nil
				continue
			}
			modelDone = nil
			modelFinished = true
			metrics.modelComplete = time.Now()
			p.emit(events.ThinkingComplete{Elapsed: metrics.elapsed(metrics.modelComplete)})
			if result.Err != nil && !interrupted {
				if p.Player != nil {
					p.Player.Stop()
				}
				return strings.TrimSpace(fullResponse.String()), interrupted, fmt.Errorf("brain: %w", result.Err)
			}

		case sentence, ok := <-sentenceCh:
			if !ok {
				sentences = nil
				closeSynthQueue() // no more handoffs; let the worker exit
				continue
			}

			sentence = strings.TrimSpace(sentence)
			if sentence == "" {
				continue
			}

			if fullResponse.Len() > 0 {
				fullResponse.WriteByte(' ')
			}
			fullResponse.WriteString(sentence)

			if interrupted || p.OutputMuted() || p.Player == nil || !p.ttsReady() {
				continue
			}

			turn.to(TurnSpeaking)
			if metrics.firstSegment.IsZero() {
				metrics.firstSegment = time.Now()
			}
			if !watchdogArmed {
				watchdogArmed = true
				go p.watchPlaybackStall(streamCtx, &audioStarted, cancel, stalled, p.stallTimeout())
			}

			pending++
			synthQueue <- sentence

		case event := <-playbackEvents:
			if interrupted && event.kind == playbackStarted && p.Player != nil {
				// A synthesis that raced the barge-in finished after Stop();
				// stop the late playback immediately.
				p.Player.Stop()
			}
			if p.applyPlaybackEvent(event, metrics, &armAt) {
				pending--
			}
		}
	}

	if !modelFinished {
		metrics.modelComplete = time.Now()
		p.emit(events.ThinkingComplete{Elapsed: metrics.elapsed(metrics.modelComplete)})
	}

	if !interrupted {
		cancel()
		if bargeDone != nil {
			<-bargeDone
		}
		p.resetEchoState()
	}

	return strings.TrimSpace(fullResponse.String()), interrupted, nil
}

// discardPCMStream drains a synthesized stream that will not be played so the
// producer goroutine is not left blocked on a full frames channel. Drain runs
// asynchronously: the caller must not wait on synth completion after mute.
func discardPCMStream(stream *audio.PCMStream) {
	if stream == nil {
		return
	}
	go func() {
		for range stream.Frames() {
		}
	}()
}

// synthesizeSegment is the synth scheduler stage, run on the ordered synth
// worker: it announces the segment, synthesizes it, starts playback, and hands
// the playback to the watcher goroutine, returning true when a playback was
// enqueued so the loop can track it as pending. TTS/playback failures surface as
// Error events; failures after the turn context is canceled (cancel or barge-in)
// are swallowed so teardown stays quiet. It never blocks on a full queue —
// backpressure is the loop's job via the pending count.
func (p *Pipeline) synthesizeSegment(ctx context.Context, loopDone <-chan struct{}, sentence string, audioStarted *atomic.Bool, out chan<- playbackEvent) bool {
	if ctx.Err() != nil || p.OutputMuted() {
		return false // canceled while queued: drain without synthesizing
	}
	p.emit(events.SpeechSegmentReady{Text: sentence})
	p.emit(events.GeneratingVoice{Sentence: sentence})

	synthStarted := time.Now()
	stream, usedFallback, err := p.synthesizeWithFallback(ctx, sentence)
	if err != nil {
		if ctx.Err() == nil {
			p.emit(events.Error{Stage: "tts", Message: fmt.Sprintf("TTS: %v", err)})
		}
		return false
	}
	if p.OutputMuted() {
		// Synthesize already started a producer goroutine. Drain so it is not
		// left blocked on the buffered frames channel until turn cancel.
		discardPCMStream(stream)
		return false
	}

	playback, err := p.Player.PlayStream(ctx, stream)
	if err != nil {
		if !usedFallback {
			playback, usedFallback, err = p.playFallback(ctx, sentence, err)
		}
		if err == nil {
			// The fallback playback is ready; continue to enqueue its watcher.
		} else {
			// PlayStream owns stream once invoked (real Player pumps even when
			// waitReady fails); do not discard here or two consumers race.
			if ctx.Err() == nil {
				p.emit(events.Error{Stage: "playback", Message: fmt.Sprintf("playback: %v", err)})
			}
			return false
		}
	}
	// PlayStream buffers a complete sentence before enqueueing it. Muting can
	// race that wait, so the initial Stop may see nothing; close the race by
	// checking once more after enqueue and stopping the late segment.
	if p.OutputMuted() {
		p.Player.Stop()
		return false
	}

	go watchPlayback(loopDone, sentence, synthStarted, playback, audioStarted, out)
	return true
}

// synthesizeWithFallback retries a synchronous provider failure once with the
// configured fallback. A Qwen worker failure is surfaced by PlayStream instead
// and is handled by playFallback below because Player owns that stream.
func (p *Pipeline) synthesizeWithFallback(ctx context.Context, text string) (*audio.PCMStream, bool, error) {
	primary, fallback := p.ttsProviders()
	if primary == nil {
		return nil, false, errors.New("TTS provider is not configured")
	}
	stream, err := primary.Synthesize(ctx, text)
	if err == nil {
		return stream, false, nil
	}
	if !p.canUseFallback(ctx, primary, fallback) || !shouldFallback(err) {
		return nil, false, err
	}
	p.emit(events.Error{Stage: "tts-fallback", Message: fmt.Sprintf("primary TTS failed; retrying with Kokoro: %v", err)})
	fallbackStream, fallbackErr := fallback.Synthesize(ctx, text)
	if fallbackErr != nil {
		return nil, true, fmt.Errorf("primary TTS: %v; Kokoro fallback: %w", err, fallbackErr)
	}
	return fallbackStream, true, nil
}

// playFallback retries an error returned after Player takes ownership of the
// primary stream. This covers file-oriented native workers whose failure is
// reported when the stream becomes ready.
func (p *Pipeline) playFallback(ctx context.Context, text string, primaryErr error) (*audio.Playback, bool, error) {
	primary, fallback := p.ttsProviders()
	if !p.canUseFallback(ctx, primary, fallback) || !shouldFallback(primaryErr) {
		return nil, false, primaryErr
	}
	p.emit(events.Error{Stage: "tts-fallback", Message: fmt.Sprintf("primary playback TTS failed; retrying with Kokoro: %v", primaryErr)})
	stream, err := fallback.Synthesize(ctx, text)
	if err != nil {
		return nil, true, fmt.Errorf("primary playback TTS: %v; Kokoro fallback: %w", primaryErr, err)
	}
	playback, err := p.Player.PlayStream(ctx, stream)
	return playback, true, err
}

// shouldFallback limits Kokoro recovery to provider failures that may be
// transient or caused by an unusable worker output. Permanent request/config
// errors must reach the user instead of being silently converted into a
// different voice policy.
func shouldFallback(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return tts.IsProviderErrorKind(err, tts.ProviderErrorUnavailable) ||
		tts.IsProviderErrorKind(err, tts.ProviderErrorWorker) ||
		tts.IsProviderErrorKind(err, tts.ProviderErrorMalformed)
}

func (p *Pipeline) canUseFallback(ctx context.Context, primary, fallback tts.Provider) bool {
	return ctx.Err() == nil && !p.OutputMuted() && fallback != nil && fallback != primary && fallback.Available()
}

// applyPlaybackEvent applies one playback lifecycle event (produced by the
// watchPlayback watcher) to the turn's metrics and events. It returns true when
// the event marks a finished playback so the caller can decrement its pending
// count.
func (p *Pipeline) applyPlaybackEvent(event playbackEvent, metrics *turnMetrics, armAt *atomic.Int64) (finished bool) {
	switch event.kind {
	case playbackNotEnqueued:
		return true

	case playbackStarted:
		armAt.Store(time.Now().Add(bargeInArmDelay).UnixNano())
		if metrics.firstAudioReady.IsZero() {
			metrics.firstAudioReady = time.Now()
		}
		if metrics.playbackStart.IsZero() {
			metrics.playbackStart = time.Now()
		}
		p.emit(events.VoiceGenerated{
			Sentence: event.sentence,
			Elapsed:  event.synthElapsed,
		})
		p.emit(events.SpeakingStarted{Text: event.sentence})
		return false

	case playbackFinished:
		metrics.playbackComplete = time.Now()
		if event.result.Interrupted {
			p.emit(events.SpeakingComplete{
				Elapsed:     event.playElapsed,
				Interrupted: true,
			})
			return true
		}
		if event.result.Err != nil && !errors.Is(event.result.Err, context.Canceled) {
			p.emit(events.Error{Stage: "playback", Message: fmt.Sprintf("playback: %v", event.result.Err)})
		}
		p.emit(events.SpeakingComplete{Elapsed: event.playElapsed})
		return true
	}
	return false
}

func (p *Pipeline) handlePlaybackLifecycle(sentence string, synthStarted time.Time, playback *audio.Playback, metrics *turnMetrics) {
	startedAt := time.Time{}

	select {
	case <-playback.Started():
		startedAt = time.Now()
		if metrics.firstAudioReady.IsZero() {
			metrics.firstAudioReady = startedAt
		}
		if metrics.playbackStart.IsZero() {
			metrics.playbackStart = startedAt
		}
		p.emit(events.VoiceGenerated{
			Sentence: sentence,
			Elapsed:  time.Since(synthStarted),
		})
		p.emit(events.SpeakingStarted{Text: sentence})
	case result := <-playback.Done():
		if result.Err != nil && !result.Interrupted && !errors.Is(result.Err, context.Canceled) {
			p.emit(events.Error{Stage: "playback", Message: fmt.Sprintf("playback: %v", result.Err)})
		}
		metrics.playbackComplete = time.Now()
		p.emit(events.SpeakingComplete{Interrupted: result.Interrupted})
		return
	}

	result := <-playback.Done()
	elapsed := time.Duration(0)
	if !startedAt.IsZero() {
		elapsed = time.Since(startedAt)
	}

	if result.Err != nil && !result.Interrupted && !errors.Is(result.Err, context.Canceled) {
		p.emit(events.Error{Stage: "playback", Message: fmt.Sprintf("playback: %v", result.Err)})
	}
	if result.Interrupted {
		p.emit(events.SpeakingInterrupted{Reason: "stopped"})
	}
	metrics.playbackComplete = time.Now()
	p.emit(events.SpeakingComplete{
		Elapsed:     elapsed,
		Interrupted: result.Interrupted,
	})
}

// observeStream is the model-observer stage. It is the sole consumer of
// stream.Chunks, forwarding each chunk to a fresh channel and stamping the
// first-model-chunk metric (with a ResponseStreamingStarted event) on the first
// one. The terminal result path (stream.Done) is owned separately by
// streamResponse. The forwarding send is cancellation-safe: on ctx cancellation
// the stage returns instead of blocking on a downstream that has stopped
// reading, so it cannot wedge teardown, model completion, or barge-in.
// The returned done channel closes once the goroutine has fully exited, so a
// caller can join it before reading metrics (it stamps firstModelChunk) and
// avoid racing snapshot() on an early return.
func (p *Pipeline) observeStream(ctx context.Context, stream *brain.Stream, metrics *turnMetrics) (<-chan string, <-chan struct{}) {
	out := make(chan string, 8)
	done := make(chan struct{})

	go func() {
		defer close(done)
		defer close(out)
		first := true
		for {
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-stream.Chunks:
				if !ok {
					return
				}
				if first {
					first = false
					metrics.firstModelChunk = time.Now()
					p.emit(events.ResponseStreamingStarted{Elapsed: metrics.elapsed(metrics.firstModelChunk)})
				}
				// Surface the raw chunk for incremental TUI display before it is
				// batched into sentences for TTS downstream.
				p.emit(events.ResponseDelta{Text: chunk})
				select {
				case <-ctx.Done():
					return
				case out <- chunk:
				}
			}
		}
	}()

	return out, done
}

// collectTextStream drains a brain stream to completion for the text-input
// path, letting observeStream emit ResponseStreamingStarted and per-chunk
// ResponseDelta events along the way, and returns the full accumulated text.
// It waits on stream.Done for the terminal error so a failed stream surfaces
// even after partial chunks were displayed.
func (p *Pipeline) collectTextStream(ctx context.Context, stream *brain.Stream, metrics *turnMetrics) (string, error) {
	out, done := p.observeStream(ctx, stream, metrics)

	var b strings.Builder
	for chunk := range out {
		b.WriteString(chunk)
	}
	<-done

	select {
	case res := <-stream.Done:
		if res.Err != nil {
			return "", res.Err
		}
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return strings.TrimSpace(b.String()), nil
}

func (p *Pipeline) resetEchoState() {
	// The capture buffer is drained at the start of the next listen (see
	// transcribeTurn), which also catches echo arriving after playback stops.
	if p.VAD != nil {
		p.VAD.Clear()
	}
	if p.BargeInVAD != nil {
		p.BargeInVAD.Clear()
	}
}

func (p *Pipeline) emit(event events.Event) {
	if p.Events != nil {
		p.Events.Emit(event)
	}
}

// toolStartHook emits ToolCallStarted for UI (stdout + TUI).
func (p *Pipeline) toolStartHook() func(name, summary string) {
	return func(name, summary string) {
		p.emit(events.ToolCallStarted{Name: name, Summary: summary})
	}
}

// toolEndHook emits ToolCallFinished for UI.
func (p *Pipeline) toolEndHook() func(name, preview string) {
	return func(name, preview string) {
		p.emit(events.ToolCallFinished{Name: name, Preview: preview})
	}
}

// watchPlayback is the playback watcher: it turns one playback's lifecycle into
// playbackStarted/playbackFinished events on out. Every send selects on loopDone
// (closed when streamResponse stops draining) so a watcher can never block — and
// leak — after the loop has returned on cancel, stall, or model error. loopDone
// is deliberately not the turn context: a barge-in cancels the turn context but
// the loop keeps consuming, so finished events must still be delivered to drain
// the pending count.
func watchPlayback(loopDone <-chan struct{}, sentence string, synthStarted time.Time, playback *audio.Playback, started *atomic.Bool, out chan<- playbackEvent) {
	startedAt := time.Time{}

	select {
	case <-loopDone:
		return
	case <-playback.Started():
		started.Store(true)
		startedAt = time.Now()
		if !sendPlaybackEvent(loopDone, out, playbackEvent{
			kind:         playbackStarted,
			sentence:     sentence,
			synthElapsed: time.Since(synthStarted),
		}) {
			return
		}
	case result := <-playback.Done():
		sendPlaybackEvent(loopDone, out, playbackEvent{
			kind:     playbackFinished,
			sentence: sentence,
			result:   result,
		})
		return
	}

	var result audio.PlaybackResult
	select {
	case <-loopDone:
		return
	case result = <-playback.Done():
	}
	elapsed := time.Duration(0)
	if !startedAt.IsZero() {
		elapsed = time.Since(startedAt)
	}

	sendPlaybackEvent(loopDone, out, playbackEvent{
		kind:        playbackFinished,
		sentence:    sentence,
		playElapsed: elapsed,
		result:      result,
	})
}

// sendPlaybackEvent delivers ev unless the loop has stopped consuming, returning
// false if it abandoned the send.
func sendPlaybackEvent(loopDone <-chan struct{}, out chan<- playbackEvent, ev playbackEvent) bool {
	select {
	case <-loopDone:
		return false
	case out <- ev:
		return true
	}
}
