package tts

import (
	"context"

	"github.com/lancekrogers/samantha/internal/audio"
)

// Provider is the interface all TTS backends implement.
type Provider interface {
	// Synthesize streams synthesized PCM frames for the given text.
	Synthesize(ctx context.Context, text string) (*audio.PCMStream, error)

	// Available returns true if this provider is ready.
	Available() bool

	// ListVoices returns available voices.
	ListVoices(locale, gender string) []Voice
}

// Voice describes an available TTS voice.
type Voice struct {
	Name         string
	FriendlyName string
	Gender       string
	Locale       string
}

// VoiceNames returns the ordered list of all kokoro voice names.
func VoiceNames() []string {
	// Copy to avoid mutation.
	out := make([]string, len(voiceNames))
	copy(out, voiceNames)
	return out
}

// VoiceInfo returns voice metadata for a given name.
func VoiceInfo(name string) Voice {
	if len(name) < 3 {
		return Voice{Name: name}
	}
	locale := langMap[name[0]]
	gender := genderMap[name[1]]
	return Voice{
		Name:   name,
		Gender: gender,
		Locale: locale,
	}
}
