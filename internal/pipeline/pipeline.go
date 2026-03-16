package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/Obedience-Corp/samantha/internal/audio"
	"github.com/Obedience-Corp/samantha/internal/brain"
	"github.com/Obedience-Corp/samantha/internal/events"
	"github.com/Obedience-Corp/samantha/internal/stt"
	"github.com/Obedience-Corp/samantha/internal/tts"
)

// Pipeline orchestrates the voice conversation loop.
type Pipeline struct {
	STT    stt.Provider
	Brain  brain.Provider
	TTS    tts.Provider
	Player *audio.Player
	Events *events.Bus
}

// RunTurn executes one conversational turn with streaming TTS.
// Returns the user's input text, or empty string if no speech was detected.
func (p *Pipeline) RunTurn(ctx context.Context) (string, error) {
	// 1. Listen + Transcribe
	t0 := time.Now()
	text, err := p.STT.Transcribe(ctx, func(phase string) {
		p.Events.Emit(events.STTPhase{Phase: phase})
	})
	if err != nil {
		return "", fmt.Errorf("STT: %w", err)
	}
	if text == "" {
		return "", nil // silence
	}

	p.Events.Emit(events.UserInput{Text: text})
	p.Events.Emit(events.STTPhase{Phase: "transcribing", Elapsed: time.Since(t0)})

	// 2. Stream Claude response
	p.Events.Emit(events.ThinkingStarted{})
	t1 := time.Now()

	chunks, err := p.Brain.ThinkStream(ctx, text)
	if err != nil {
		return text, fmt.Errorf("brain: %w", err)
	}

	// 3. Chunk into sentences and stream TTS
	sentences := brain.ChunkSentences(chunks)

	thinkReported := false
	var fullResponse string

	for sentence := range sentences {
		if !thinkReported {
			p.Events.Emit(events.ThinkingComplete{
				Elapsed: time.Since(t1),
			})
			thinkReported = true
		}

		fullResponse += sentence + " "

		// Generate and play TTS (skip if no voice output)
		if p.TTS != nil && p.Player != nil {
			p.Events.Emit(events.GeneratingVoice{Sentence: sentence})
			t2 := time.Now()

			samples, sampleRate, err := p.TTS.Generate(sentence)
			if err != nil {
				p.Events.Emit(events.Error{Message: fmt.Sprintf("TTS: %v", err)})
				continue
			}

			p.Events.Emit(events.VoiceGenerated{Elapsed: time.Since(t2)})

			p.Events.Emit(events.SpeakingStarted{Text: sentence})
			t3 := time.Now()

			done := p.Player.PlayAsync(ctx, samples, sampleRate)
			<-done

			p.Events.Emit(events.SpeakingComplete{Elapsed: time.Since(t3)})
		}
	}

	if !thinkReported {
		p.Events.Emit(events.ThinkingComplete{Elapsed: time.Since(t1)})
	}

	p.Events.Emit(events.ResponseReady{Response: fullResponse})

	return text, nil
}

// RunTurnTextMode runs a turn with text input instead of mic.
func (p *Pipeline) RunTurnTextMode(ctx context.Context, input string) error {
	p.Events.Emit(events.UserInput{Text: input})

	// Think
	p.Events.Emit(events.ThinkingStarted{})
	t0 := time.Now()

	response, err := p.Brain.ThinkFull(ctx, input)
	if err != nil {
		return fmt.Errorf("brain: %w", err)
	}

	p.Events.Emit(events.ThinkingComplete{
		Response: response,
		Elapsed:  time.Since(t0),
	})

	// Generate and play TTS
	if p.TTS != nil && p.TTS.Available() {
		p.Events.Emit(events.GeneratingVoice{Sentence: response})
		t1 := time.Now()

		samples, sampleRate, err := p.TTS.Generate(response)
		if err != nil {
			p.Events.Emit(events.Error{Message: fmt.Sprintf("TTS: %v", err)})
			p.Events.Emit(events.ResponseReady{Response: response})
			return nil
		}

		p.Events.Emit(events.VoiceGenerated{Elapsed: time.Since(t1)})

		p.Events.Emit(events.SpeakingStarted{Text: response})
		t2 := time.Now()
		done := p.Player.PlayAsync(ctx, samples, sampleRate)
		<-done
		p.Events.Emit(events.SpeakingComplete{Elapsed: time.Since(t2)})
	}

	p.Events.Emit(events.ResponseReady{Response: response})
	return nil
}
