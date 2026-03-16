package tts

// Provider is the interface all TTS backends implement.
type Provider interface {
	// Generate produces audio from text.
	// Returns float32 samples and sample rate.
	Generate(text string) (samples []float32, sampleRate int, err error)

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
