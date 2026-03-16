package events

import "time"

// Event is the base interface all events implement.
type Event interface {
	eventType() string
}

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

// GeneratingVoice signals TTS generation has started.
type GeneratingVoice struct {
	Sentence string
}

func (e GeneratingVoice) eventType() string { return "generating_voice" }

// VoiceGenerated signals TTS generation is complete.
type VoiceGenerated struct {
	Elapsed time.Duration
}

func (e VoiceGenerated) eventType() string { return "voice_generated" }

// SpeakingStarted signals audio playback has begun.
type SpeakingStarted struct {
	Text string
}

func (e SpeakingStarted) eventType() string { return "speaking_started" }

// SpeakingComplete signals audio playback is done.
type SpeakingComplete struct {
	Elapsed time.Duration
}

func (e SpeakingComplete) eventType() string { return "speaking_complete" }

// ResponseReady carries the final display text.
type ResponseReady struct {
	Response string
}

func (e ResponseReady) eventType() string { return "response_ready" }

// ConversationCleared signals history was wiped.
type ConversationCleared struct{}

func (e ConversationCleared) eventType() string { return "conversation_cleared" }

// Error carries an error message for display.
type Error struct {
	Message string
}

func (e Error) eventType() string { return "error" }

// Info carries an informational message.
type Info struct {
	Message string
}

func (e Info) eventType() string { return "info" }
