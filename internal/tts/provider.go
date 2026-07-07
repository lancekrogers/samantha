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

// SynthesisRequest describes a single synthesis call. Empty Voice and zero
// Speed fall back to the provider's configured defaults.
type SynthesisRequest struct {
	Text       string
	Voice      string
	Speed      float64
	SampleRate int
	Metadata   map[string]string
}

// SynthesisResult carries a synthesized stream and its metadata.
type SynthesisResult struct {
	Stream     *audio.PCMStream
	SampleRate int
	Provider   string
	Voice      string
}

// RequestProvider is implemented by providers that accept typed requests.
type RequestProvider interface {
	SynthesizeRequest(ctx context.Context, req SynthesisRequest) (SynthesisResult, error)
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
