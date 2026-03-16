package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Obedience-Corp/samantha/internal/audio"
	"github.com/Obedience-Corp/samantha/internal/brain"
	"github.com/Obedience-Corp/samantha/internal/events"
	"github.com/Obedience-Corp/samantha/internal/stt"
	"github.com/Obedience-Corp/samantha/internal/tts"
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

// RunTurn executes one conversational turn with streaming TTS.
// Returns the user's input text, or empty string if no speech was detected.
func (p *Pipeline) RunTurn(ctx context.Context) (string, error) {
	if p.Player != nil {
		p.Player.Stop()
	}

	t0 := time.Now()
	text, err := p.STT.Transcribe(ctx, func(phase string) {
		p.emit(events.STTPhase{Phase: phase})
	})
	if err != nil {
		return "", fmt.Errorf("STT: %w", err)
	}
	if text == "" {
		return "", nil
	}

	p.emit(events.UserInput{Text: text})
	p.emit(events.STTPhase{Phase: "transcribing", Elapsed: time.Since(t0)})
	p.emit(events.ThinkingStarted{})

	thinkingStarted := time.Now()
	chunks, err := p.Brain.ThinkStream(ctx, text, brain.StreamOptions{
		VoiceMode:    true,
		ToolsEnabled: p.VoiceToolsEnabled,
	})
	if err != nil {
		return text, fmt.Errorf("brain: %w", err)
	}

	fullResponse, interrupted, err := p.streamResponse(ctx, chunks, thinkingStarted, true)
	if err != nil {
		return text, err
	}

	p.emit(events.ResponseReady{
		Response:    fullResponse,
		Interrupted: interrupted,
	})

	if p.OnTurn != nil {
		p.OnTurn()
	}

	return text, nil
}

// RunTurnTextMode runs a turn with text input instead of mic.
func (p *Pipeline) RunTurnTextMode(ctx context.Context, input string) error {
	p.emit(events.UserInput{Text: input})
	p.emit(events.ThinkingStarted{})
	thinkingStarted := time.Now()

	response, err := p.Brain.ThinkFull(ctx, input)
	if err != nil {
		return fmt.Errorf("brain: %w", err)
	}

	p.emit(events.ThinkingComplete{
		Response: response,
		Elapsed:  time.Since(thinkingStarted),
	})

	if p.TTS != nil && p.Player != nil && p.TTS.Available() {
		p.emit(events.SpeechSegmentReady{Text: response})
		p.emit(events.GeneratingVoice{Sentence: response})

		synthStarted := time.Now()
		stream, err := p.TTS.Synthesize(ctx, response)
		if err != nil {
			p.emit(events.Error{Message: fmt.Sprintf("TTS: %v", err)})
			p.emit(events.ResponseReady{Response: response})
			return nil
		}

		playback, err := p.Player.PlayStream(ctx, stream)
		if err != nil {
			p.emit(events.Error{Message: fmt.Sprintf("playback: %v", err)})
			p.emit(events.ResponseReady{Response: response})
			return nil
		}

		p.handlePlaybackLifecycle(response, synthStarted, playback)
	}

	p.emit(events.ResponseReady{Response: response})

	if p.OnTurn != nil {
		p.OnTurn()
	}

	return nil
}

func (p *Pipeline) streamResponse(ctx context.Context, chunks <-chan string, thinkingStarted time.Time, allowBargeIn bool) (string, bool, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	streamedChunks := p.observeChunks(streamCtx, chunks, thinkingStarted)
	sentences := brain.ChunkSentences(streamedChunks)

	var thinkReported bool
	var fullResponse strings.Builder
	var interrupted bool
	var pending int
	var bargeIn <-chan struct{}
	var armAt atomic.Int64
	armAt.Store(time.Now().Add(24 * time.Hour).UnixNano())

	if allowBargeIn {
		bargeIn = p.watchBargeIn(streamCtx, &armAt)
	}

	slotSem := make(chan struct{}, voiceQueueDepth)
	playbackEvents := make(chan playbackEvent, voiceQueueDepth*2)

	for sentences != nil || pending > 0 {
		select {
		case <-ctx.Done():
			return strings.TrimSpace(fullResponse.String()), interrupted, ctx.Err()

		case <-bargeIn:
			if interrupted {
				continue
			}
			interrupted = true
			cancel()
			if p.Player != nil {
				p.Player.Stop()
			}
			p.emit(events.SpeakingInterrupted{Reason: "barge_in"})
			p.emit(events.TurnInterrupted{Reason: "barge_in"})

		case sentence, ok := <-sentences:
			if !ok {
				sentences = nil
				if !thinkReported {
					p.emit(events.ThinkingComplete{Elapsed: time.Since(thinkingStarted)})
					thinkReported = true
				}
				continue
			}

			if !thinkReported {
				p.emit(events.ThinkingComplete{Elapsed: time.Since(thinkingStarted)})
				thinkReported = true
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

			slotSem <- struct{}{}
			p.emit(events.SpeechSegmentReady{Text: sentence})
			p.emit(events.GeneratingVoice{Sentence: sentence})

			synthStarted := time.Now()
			stream, err := p.TTS.Synthesize(streamCtx, sentence)
			if err != nil {
				<-slotSem
				if interrupted || errors.Is(err, context.Canceled) {
					continue
				}
				p.emit(events.Error{Message: fmt.Sprintf("TTS: %v", err)})
				continue
			}

			playback, err := p.Player.PlayStream(streamCtx, stream)
			if err != nil {
				<-slotSem
				if interrupted || errors.Is(err, context.Canceled) {
					continue
				}
				p.emit(events.Error{Message: fmt.Sprintf("playback: %v", err)})
				continue
			}

			pending++
			go watchPlayback(sentence, synthStarted, playback, playbackEvents)

		case event := <-playbackEvents:
			switch event.kind {
			case playbackStarted:
				armAt.Store(time.Now().Add(bargeInArmDelay).UnixNano())
				p.emit(events.VoiceGenerated{
					Sentence: event.sentence,
					Elapsed:  event.synthElapsed,
				})
				p.emit(events.SpeakingStarted{Text: event.sentence})

			case playbackFinished:
				pending--
				<-slotSem

				if event.result.Interrupted {
					p.emit(events.SpeakingComplete{
						Elapsed:     event.playElapsed,
						Interrupted: true,
					})
					continue
				}

				if event.result.Err != nil && !errors.Is(event.result.Err, context.Canceled) {
					p.emit(events.Error{Message: fmt.Sprintf("playback: %v", event.result.Err)})
				}

				p.emit(events.SpeakingComplete{Elapsed: event.playElapsed})
			}
		}
	}

	if !interrupted {
		p.resetEchoState()
	}

	return strings.TrimSpace(fullResponse.String()), interrupted, nil
}

func (p *Pipeline) handlePlaybackLifecycle(sentence string, synthStarted time.Time, playback *audio.Playback) {
	startedAt := time.Time{}

	select {
	case <-playback.Started():
		startedAt = time.Now()
		p.emit(events.VoiceGenerated{
			Sentence: sentence,
			Elapsed:  time.Since(synthStarted),
		})
		p.emit(events.SpeakingStarted{Text: sentence})
	case result := <-playback.Done():
		if result.Err != nil && !result.Interrupted && !errors.Is(result.Err, context.Canceled) {
			p.emit(events.Error{Message: fmt.Sprintf("playback: %v", result.Err)})
		}
		p.emit(events.SpeakingComplete{Interrupted: result.Interrupted})
		return
	}

	result := <-playback.Done()
	elapsed := time.Duration(0)
	if !startedAt.IsZero() {
		elapsed = time.Since(startedAt)
	}

	if result.Err != nil && !result.Interrupted && !errors.Is(result.Err, context.Canceled) {
		p.emit(events.Error{Message: fmt.Sprintf("playback: %v", result.Err)})
	}
	if result.Interrupted {
		p.emit(events.SpeakingInterrupted{Reason: "stopped"})
	}
	p.emit(events.SpeakingComplete{
		Elapsed:     elapsed,
		Interrupted: result.Interrupted,
	})
}

func (p *Pipeline) observeChunks(ctx context.Context, chunks <-chan string, thinkingStarted time.Time) <-chan string {
	out := make(chan string, 8)

	go func() {
		defer close(out)
		first := true
		for {
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-chunks:
				if !ok {
					return
				}
				if first {
					first = false
					p.emit(events.ResponseStreamingStarted{Elapsed: time.Since(thinkingStarted)})
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

func watchPlayback(sentence string, synthStarted time.Time, playback *audio.Playback, out chan<- playbackEvent) {
	startedAt := time.Time{}

	select {
	case <-playback.Started():
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
