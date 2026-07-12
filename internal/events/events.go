package events

import "time"

// Event is the base interface all events implement.
type Event interface {
	eventType() string
}

// EventType returns the event's stable wire name (e.g. "response_ready"),
// for consumers outside this package that need a type discriminator.
func EventType(e Event) string { return e.eventType() }

// STTPhase reports a speech-to-text phase transition.
type STTPhase struct {
	Phase   string        // "listening", "hearing", "transcribing"
	Elapsed time.Duration // time spent in previous phase (0 if starting)
}

func (e STTPhase) eventType() string { return "stt_phase" }

// UserInput carries the transcribed user text.
type UserInput struct {
	Text string
}

func (e UserInput) eventType() string { return "user_input" }

// TranscriptPartial carries an incremental transcript update.
type TranscriptPartial struct {
	Text string
}

func (e TranscriptPartial) eventType() string { return "transcript_partial" }

// ThinkingStarted signals Claude has begun processing.
type ThinkingStarted struct{}

func (e ThinkingStarted) eventType() string { return "thinking_started" }

// ThinkingComplete signals Claude has finished.
type ThinkingComplete struct {
	Response     string
	FullResponse string
	Elapsed      time.Duration
}

func (e ThinkingComplete) eventType() string { return "thinking_complete" }

// TurnMetrics captures per-turn latency milestones for benchmarking.
type TurnMetrics struct {
	// Outcome is the turn's terminal state as decided by the turn state
	// machine: completed, interrupted, failed, or timed_out.
	Outcome                 string
	Interrupted             bool
	STTFinalElapsed         time.Duration
	FirstModelChunkElapsed  time.Duration
	ModelCompleteElapsed    time.Duration
	FirstSegmentElapsed     time.Duration
	FirstAudioReadyElapsed  time.Duration
	PlaybackStartElapsed    time.Duration
	PlaybackCompleteElapsed time.Duration
	BargeInElapsed          time.Duration
}

func (e TurnMetrics) eventType() string { return "turn_metrics" }

// ResponseStreamingStarted signals the first response chunk has arrived.
type ResponseStreamingStarted struct {
	Elapsed time.Duration
}

func (e ResponseStreamingStarted) eventType() string { return "response_streaming_started" }

// SpeechSegmentReady signals a sentence is ready for voice synthesis.
type SpeechSegmentReady struct {
	Text string
}

func (e SpeechSegmentReady) eventType() string { return "speech_segment_ready" }

// GeneratingVoice signals TTS generation has started.
type GeneratingVoice struct {
	Sentence string
}

func (e GeneratingVoice) eventType() string { return "generating_voice" }

// VoiceGenerated signals TTS generation is complete.
type VoiceGenerated struct {
	Sentence string
	Elapsed  time.Duration
}

func (e VoiceGenerated) eventType() string { return "voice_generated" }

// SpeakingStarted signals audio playback has begun.
type SpeakingStarted struct {
	Text string
}

func (e SpeakingStarted) eventType() string { return "speaking_started" }

// SpeakingComplete signals audio playback is done.
type SpeakingComplete struct {
	Elapsed     time.Duration
	Interrupted bool
}

func (e SpeakingComplete) eventType() string { return "speaking_complete" }

// SpeakingInterrupted signals playback was cut short.
type SpeakingInterrupted struct {
	Reason string
}

func (e SpeakingInterrupted) eventType() string { return "speaking_interrupted" }

// TurnInterrupted signals the active assistant turn was canceled.
type TurnInterrupted struct {
	Reason string
}

func (e TurnInterrupted) eventType() string { return "turn_interrupted" }

// ResponseReady carries the final display text.
type ResponseReady struct {
	Response    string
	Interrupted bool
}

func (e ResponseReady) eventType() string { return "response_ready" }

// ConversationCleared signals history was wiped.
type ConversationCleared struct{}

func (e ConversationCleared) eventType() string { return "conversation_cleared" }

// Error carries an error message for display.
type Error struct {
	Stage   string
	Message string
}

func (e Error) eventType() string { return "error" }

// Info carries an informational message.
type Info struct {
	Message string
}

func (e Info) eventType() string { return "info" }
