package tts

import (
	"errors"
	"fmt"
)

// VoiceMode identifies a provider-neutral way of choosing a speaker. Providers
// advertise only modes they can actually support for the selected model.
type VoiceMode string

const (
	VoiceModeStatic        VoiceMode = "static"
	VoiceModeCustomVoice   VoiceMode = "customvoice"
	VoiceModeVoiceDesign   VoiceMode = "voicedesign"
	VoiceModeApprovedClone VoiceMode = "approved_clone"
)

// VoiceModeCapability describes the inputs a mode accepts. Voices is
// populated only when the provider can verify a preset speaker list.
type VoiceModeCapability struct {
	ID                     VoiceMode
	Voices                 []Voice
	RequiresInstruction    bool
	RequiresReferenceAudio bool
	RequiresReferenceText  bool
	Experimental           bool
}

// ProviderCapabilities is the provider-neutral discovery result. An empty
// Modes list means that the provider is unavailable or that the selected model
// cannot be probed; it never means that Kokoro voices should be substituted.
type ProviderCapabilities struct {
	Provider               string
	Model                  string
	ModelReady             bool
	Modes                  []VoiceModeCapability
	Languages              []string
	SampleRates            []int
	SupportsPreview        bool
	SupportsStreaming      bool
	SupportsCancellation   bool
	SupportsReferenceAudio bool
}

// ProviderStatus reports readiness without requiring callers to parse an
// error string. Available remains on Provider for backwards compatibility.
type ProviderStatus struct {
	Provider   string
	Available  bool
	ModelReady bool
	Detail     string
}

// CapabilityProvider is an optional extension of Provider. Keeping this
// separate preserves the existing Provider interface for Kokoro-shaped fakes
// and third-party integrations while allowing the TUI and future providers to
// discover model-specific voice modes.
type CapabilityProvider interface {
	Provider
	Capabilities() ProviderCapabilities
	Status() ProviderStatus
}

// FallbackProvider is the only fallback policy currently supported by the
// product contract. It is deliberately explicit so selecting an optional
// provider does not silently change behavior.
type FallbackProvider string

const (
	FallbackDisabled FallbackProvider = "disabled"
	FallbackKokoro   FallbackProvider = "kokoro"
)

// ErrUnsupportedFeature is the sentinel for a valid request that a provider
// cannot honor. Callers can use errors.Is without depending on provider text.
var ErrUnsupportedFeature = errors.New("tts feature unsupported")

var (
	ErrProviderUnavailable = errors.New("tts provider unavailable")
	ErrUnsupportedInput    = errors.New("tts input unsupported")
	ErrWorkerFailure       = errors.New("tts worker failure")
	ErrMalformedOutput     = errors.New("tts malformed output")
)

// ProviderErrorKind lets the pipeline distinguish setup/input failures from
// worker and output failures without parsing provider-specific strings.
type ProviderErrorKind string

const (
	ProviderErrorUnavailable ProviderErrorKind = "unavailable"
	ProviderErrorInput       ProviderErrorKind = "unsupported_input"
	ProviderErrorWorker      ProviderErrorKind = "worker_failure"
	ProviderErrorCanceled    ProviderErrorKind = "canceled"
	ProviderErrorMalformed   ProviderErrorKind = "malformed_output"
)

// ProviderError is an actionable, unwrap-compatible provider failure.
type ProviderError struct {
	Provider  string
	Operation string
	Kind      ProviderErrorKind
	Err       error
}

func (e *ProviderError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("%s: %s", e.Provider, e.Kind)
	}
	return fmt.Sprintf("%s: %s: %v", e.Provider, e.Kind, e.Err)
}

func (e *ProviderError) Unwrap() []error {
	var causes []error
	if e.Err != nil {
		causes = append(causes, e.Err)
	}
	switch e.Kind {
	case ProviderErrorUnavailable:
		causes = append(causes, ErrProviderUnavailable)
	case ProviderErrorInput:
		causes = append(causes, ErrUnsupportedInput)
	case ProviderErrorWorker:
		causes = append(causes, ErrWorkerFailure)
	case ProviderErrorCanceled:
		causes = append(causes, ErrWorkerFailure)
	case ProviderErrorMalformed:
		causes = append(causes, ErrMalformedOutput)
	}
	return causes
}

// IsProviderErrorKind reports a provider error kind through wrapped errors.
func IsProviderErrorKind(err error, kind ProviderErrorKind) bool {
	var providerErr *ProviderError
	return errors.As(err, &providerErr) && providerErr.Kind == kind
}

// UnsupportedFeatureError identifies the rejected provider-neutral feature.
type UnsupportedFeatureError struct {
	Provider string
	Feature  string
	Detail   string
}

func (e *UnsupportedFeatureError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("%s: %s unsupported: %s", e.Provider, e.Feature, e.Detail)
	}
	return fmt.Sprintf("%s: %s unsupported", e.Provider, e.Feature)
}

func (e *UnsupportedFeatureError) Unwrap() error { return ErrUnsupportedFeature }

// Clone returns an isolated copy so a provider cannot mutate a cached
// capability response held by the TUI or a request planner.
func (c ProviderCapabilities) Clone() ProviderCapabilities {
	out := c
	out.Languages = append([]string(nil), c.Languages...)
	out.SampleRates = append([]int(nil), c.SampleRates...)
	out.Modes = make([]VoiceModeCapability, len(c.Modes))
	for i, mode := range c.Modes {
		out.Modes[i] = mode
		out.Modes[i].Voices = append([]Voice(nil), mode.Voices...)
	}
	return out
}
