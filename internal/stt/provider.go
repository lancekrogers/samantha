package stt

import "context"

// Provider is the interface all STT backends implement.
type Provider interface {
	// Transcribe listens on the mic and returns transcribed text.
	// onStatus reports phase transitions: "listening", "hearing", "transcribing".
	Transcribe(ctx context.Context, onStatus func(phase string)) (string, error)

	// Available returns true if this provider is ready.
	Available() bool
}
