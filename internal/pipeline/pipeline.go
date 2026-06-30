package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/brain"
	"github.com/lancekrogers/samantha/internal/events"
	"github.com/lancekrogers/samantha/internal/stt"
	"github.com/lancekrogers/samantha/internal/tts"
)

const (
	voiceQueueDepth        = 2
	bargeInArmDelay        = 180 * time.Millisecond
	bargeInMinSpeechChunks = 3
	bargeInBufferSize      = 8
)

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
	STT               stt.Provider
	Brain             brain.Provider
	TTS               tts.Provider
	Player            audio.Engine
	Capture           captureMonitor
	VAD               voiceDetector
	BargeInVAD        voiceDetector
	Events            *events.Bus
	VoiceToolsEnabled bool
	OnTurn            func() // called after each completed turn for session auto-save

	// PlaybackStallTimeout overrides the watchdog timeout; zero uses the default.
	PlaybackStallTimeout time.Duration
}

type playbackEventType int

const (
	playbackStarted playbackEventType = iota
	playbackFinished
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

// RunTurn executes one conversational turn with streaming TTS.
// Returns the user's input text, or empty string if no speech was detected.
func (p *Pipeline) RunTurn(ctx context.Context) (string, error) {
	metrics := newTurnMetrics()

	if p.Player != nil {
		p.Player.Stop()
	}

	text, err := p.transcribeTurn(ctx, metrics)
	if err != nil {
		return "", err
	}
	if text == "" {
		p.emit(events.TurnMetrics(metrics.snapshot()))
		return "", nil
	}

	p.emit(events.UserInput{Text: text})
	p.emit(events.ThinkingStarted{})

	// turnCtx scopes the whole turn so the playback watchdog can abort the brain
	// stream too — not just the derived playback context.
	turnCtx, turnCancel := context.WithCancel(ctx)
	defer turnCancel()

	brainStream, err := p.Brain.ThinkStream(turnCtx, text, brain.StreamOptions{
		VoiceMode:    true,
		ToolsEnabled: p.VoiceToolsEnabled,
	})
	if err != nil {
		return text, fmt.Errorf("brain: %w", err)
	}

	fullResponse, interrupted, err := p.streamResponse(turnCtx, brainStream, true, metrics)
	if err != nil {
		return text, err
	}

	p.emit(events.ResponseReady{
		Response:    fullResponse,
		Interrupted: interrupted,
	})
	p.emit(events.TurnMetrics(metrics.snapshot()))

	if p.OnTurn != nil {
		p.OnTurn()
	}

	return text, nil
}

// RunTurnTextMode runs a turn with text input instead of mic.
func (p *Pipeline) RunTurnTextMode(ctx context.Context, input string) error {
	metrics := newTurnMetrics()

	p.emit(events.UserInput{Text: input})
	p.emit(events.ThinkingStarted{})
	thinkingStarted := time.Now()

	response, err := p.Brain.ThinkFull(ctx, input)
	if err != nil {
		return fmt.Errorf("brain: %w", err)
	}

	now := time.Now()
	metrics.firstModelChunk = now
	metrics.modelComplete = now
	p.emit(events.ResponseStreamingStarted{Elapsed: time.Since(thinkingStarted)})
	p.emit(events.ThinkingComplete{
		Response: response,
		Elapsed:  time.Since(thinkingStarted),
	})

	if p.TTS != nil && p.Player != nil && p.TTS.Available() {
		if metrics.firstSegment.IsZero() {
			metrics.firstSegment = time.Now()
		}
		p.emit(events.SpeechSegmentReady{Text: response})
		p.emit(events.GeneratingVoice{Sentence: response})

		synthStarted := time.Now()
		stream, err := p.TTS.Synthesize(ctx, response)
		if err != nil {
			p.emit(events.Error{Stage: "tts", Message: fmt.Sprintf("TTS: %v", err)})
			p.emit(events.ResponseReady{Response: response})
			p.emit(events.TurnMetrics(metrics.snapshot()))
			return nil
		}

		playback, err := p.Player.PlayStream(ctx, stream)
		if err != nil {
			p.emit(events.Error{Stage: "playback", Message: fmt.Sprintf("playback: %v", err)})
			p.emit(events.ResponseReady{Response: response})
			p.emit(events.TurnMetrics(metrics.snapshot()))
			return nil
		}

		p.handlePlaybackLifecycle(response, synthStarted, playback, metrics)
	}

	p.emit(events.ResponseReady{Response: response})
	p.emit(events.TurnMetrics(metrics.snapshot()))

	if p.OnTurn != nil {
		p.OnTurn()
	}

	return nil
}

func (p *Pipeline) transcribeTurn(ctx context.Context, metrics *turnMetrics) (string, error) {
	if p.STT == nil {
		return "", errors.New("STT provider is not configured")
	}

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

			switch e := event.(type) {
			case stt.PhaseEvent:
				p.emit(events.STTPhase{
					Phase:   e.Phase,
					Elapsed: time.Duration(e.Elapsed),
				})
			case stt.PartialTranscript:
				if e.Text != "" {
					p.emit(events.TranscriptPartial{Text: e.Text})
				}
			case stt.FinalTranscript:
				metrics.sttFinal = time.Now()
				return e.Text, nil
			case stt.Timeout:
				return "", nil
			case stt.Failure:
				if errors.Is(e.Err, context.Canceled) && ctx.Err() != nil {
					return "", ctx.Err()
				}
				return "", fmt.Errorf("STT: %w", e.Err)
			}
		}
	}
}

func (p *Pipeline) streamResponse(ctx context.Context, stream *brain.Stream, allowBargeIn bool, metrics *turnMetrics) (string, bool, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	streamedChunks := p.observeStream(streamCtx, stream, metrics)
	sentences := brain.ChunkSentences(streamedChunks)

	var fullResponse strings.Builder
	var interrupted bool
	var pending int
	var bargeIn <-chan struct{}
	var armAt atomic.Int64
	armAt.Store(time.Now().Add(24 * time.Hour).UnixNano())

	if allowBargeIn {
		bargeIn = p.watchBargeIn(streamCtx, &armAt)
	}

	playbackEvents := make(chan playbackEvent, voiceQueueDepth*2)
	modelDone := stream.Done
	modelFinished := false

	var audioStarted atomic.Bool
	stalled := make(chan struct{})
	watchdogArmed := false

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
			return strings.TrimSpace(fullResponse.String()), interrupted, ctx.Err()

		case <-stalled:
			return strings.TrimSpace(fullResponse.String()), interrupted, errPlaybackStalled

		case <-bargeIn:
			if interrupted {
				continue
			}
			interrupted = true
			metrics.interrupted = true
			metrics.bargeIn = time.Now()
			cancel()
			if p.Player != nil {
				p.Player.Stop()
			}
			p.emit(events.SpeakingInterrupted{Reason: "barge_in"})
			p.emit(events.TurnInterrupted{Reason: "barge_in"})

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

			if interrupted || p.TTS == nil || p.Player == nil || !p.TTS.Available() {
				continue
			}

			if metrics.firstSegment.IsZero() {
				metrics.firstSegment = time.Now()
			}
			if !watchdogArmed {
				watchdogArmed = true
				go p.watchPlaybackStall(streamCtx, &audioStarted, cancel, stalled, p.stallTimeout())
			}
			p.emit(events.SpeechSegmentReady{Text: sentence})
			p.emit(events.GeneratingVoice{Sentence: sentence})

			synthStarted := time.Now()
			stream, err := p.TTS.Synthesize(streamCtx, sentence)
			if err != nil {
				if interrupted || errors.Is(err, context.Canceled) {
					continue
				}
				p.emit(events.Error{Stage: "tts", Message: fmt.Sprintf("TTS: %v", err)})
				continue
			}

			playback, err := p.Player.PlayStream(streamCtx, stream)
			if err != nil {
				if interrupted || errors.Is(err, context.Canceled) {
					continue
				}
				p.emit(events.Error{Stage: "playback", Message: fmt.Sprintf("playback: %v", err)})
				continue
			}

			pending++
			go watchPlayback(sentence, synthStarted, playback, &audioStarted, playbackEvents)

		case event := <-playbackEvents:
			switch event.kind {
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

			case playbackFinished:
				pending--
				metrics.playbackComplete = time.Now()

				if event.result.Interrupted {
					p.emit(events.SpeakingComplete{
						Elapsed:     event.playElapsed,
						Interrupted: true,
					})
					continue
				}

				if event.result.Err != nil && !errors.Is(event.result.Err, context.Canceled) {
					p.emit(events.Error{Stage: "playback", Message: fmt.Sprintf("playback: %v", event.result.Err)})
				}

				p.emit(events.SpeakingComplete{Elapsed: event.playElapsed})
			}
		}
	}

	if !modelFinished {
		metrics.modelComplete = time.Now()
		p.emit(events.ThinkingComplete{Elapsed: metrics.elapsed(metrics.modelComplete)})
	}

	if !interrupted {
		p.resetEchoState()
	}

	return strings.TrimSpace(fullResponse.String()), interrupted, nil
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

func (p *Pipeline) observeStream(ctx context.Context, stream *brain.Stream, metrics *turnMetrics) <-chan string {
	out := make(chan string, 8)

	go func() {
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
				out <- chunk
			}
		}
	}()

	return out
}

func (p *Pipeline) watchBargeIn(ctx context.Context, armAt *atomic.Int64) <-chan struct{} {
	if p.Capture == nil || p.BargeInVAD == nil || p.Player == nil {
		return nil
	}

	triggered := make(chan struct{}, 1)
	subscriptionID, chunks := p.Capture.Subscribe(bargeInBufferSize)

	go func() {
		defer p.Capture.Unsubscribe(subscriptionID)
		defer p.BargeInVAD.Clear()

		consecutiveSpeech := 0
		for {
			select {
			case <-ctx.Done():
				return
			case samples, ok := <-chunks:
				if !ok {
					return
				}

				if !p.Player.IsPlaying() || time.Now().UnixNano() < armAt.Load() {
					consecutiveSpeech = 0
					p.BargeInVAD.Clear()
					continue
				}

				p.BargeInVAD.AcceptWaveform(samples)
				if p.BargeInVAD.IsSpeech() {
					consecutiveSpeech++
				} else {
					consecutiveSpeech = 0
				}

				if p.BargeInVAD.IsSpeechDetected() || consecutiveSpeech >= bargeInMinSpeechChunks {
					select {
					case triggered <- struct{}{}:
					default:
					}
					return
				}
			}
		}
	}()

	return triggered
}

func (p *Pipeline) resetEchoState() {
	if p.Capture != nil {
		p.Capture.Reset()
	}
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

func watchPlayback(sentence string, synthStarted time.Time, playback *audio.Playback, started *atomic.Bool, out chan<- playbackEvent) {
	startedAt := time.Time{}

	select {
	case <-playback.Started():
		started.Store(true)
		startedAt = time.Now()
		out <- playbackEvent{
			kind:         playbackStarted,
			sentence:     sentence,
			synthElapsed: time.Since(synthStarted),
		}
	case result := <-playback.Done():
		out <- playbackEvent{
			kind:     playbackFinished,
			sentence: sentence,
			result:   result,
		}
		return
	}

	result := <-playback.Done()
	elapsed := time.Duration(0)
	if !startedAt.IsZero() {
		elapsed = time.Since(startedAt)
	}

	out <- playbackEvent{
		kind:        playbackFinished,
		sentence:    sentence,
		playElapsed: elapsed,
		result:      result,
	}
}
