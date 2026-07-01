// Package endpoint represents utterance endpointing as an explicit policy,
// separate from VAD (which only reports speech activity) and from individual
// STT provider loops. The policy is a pure function of observed durations so it
// stays deterministic for fixture tests and free of native/cgo dependencies.
package endpoint

import (
	"time"

	"github.com/lancekrogers/samantha/internal/config"
)

// DecisionKind enumerates the endpointing outcomes.
type DecisionKind int

const (
	// Continue: keep listening; the utterance is not finished.
	Continue DecisionKind = iota
	// SpeechStarted: speech onset was detected (emitted by the caller on the VAD
	// transition; Decide returns Continue while speech is ongoing).
	SpeechStarted
	// Finalize: the utterance is complete and should be transcribed.
	Finalize
	// Timeout: no speech arrived before the start timeout.
	Timeout
	// TooShort: speech ended but was shorter than MinSpeech; reject it.
	TooShort
	// SourceExhausted: a finite source ended without a usable utterance.
	SourceExhausted
)

func (k DecisionKind) String() string {
	switch k {
	case Continue:
		return "continue"
	case SpeechStarted:
		return "speech_started"
	case Finalize:
		return "finalize"
	case Timeout:
		return "timeout"
	case TooShort:
		return "too_short"
	case SourceExhausted:
		return "source_exhausted"
	default:
		return "unknown"
	}
}

// Policy decides when an utterance should be finalized. It separates the
// finalization decision from VAD, which only answers whether speech is present.
type Policy struct {
	// MinSpeech is the least cumulative speech required to accept an utterance;
	// shorter speech is rejected as too short.
	MinSpeech time.Duration
	// MinSilence is the trailing silence after speech that finalizes an utterance.
	MinSilence time.Duration
	// MaxUtterance caps utterance length; 0 disables the cap.
	MaxUtterance time.Duration
	// StartTimeout bounds how long to wait for speech to begin; 0 disables it.
	// Finite sources set this to 0 because EOF, not a clock, ends them.
	StartTimeout time.Duration
	// FinalizeOnEOF finalizes a finite source's buffered speech when it reaches EOF.
	FinalizeOnEOF bool
	// AllowProviderEnd lets a provider-signalled endpoint finalize the utterance.
	AllowProviderEnd bool
}

// Observation is the accumulated state the policy evaluates. All values are
// caller-supplied durations, so Decide reads no wall clock and is deterministic.
type Observation struct {
	HasSpeech       bool          // any speech detected so far this utterance
	SpeechSeen      time.Duration // cumulative speech duration
	TrailingSilence time.Duration // silence since the last speech
	Elapsed         time.Duration // since listening started
	ProviderEnd     bool          // the provider signalled an endpoint
	SourceFinal     bool          // a finite source reached EOF
}

// Decision is the result of evaluating a Policy against an Observation.
type Decision struct {
	Kind      DecisionKind
	Reason    string
	AudioSeen time.Duration
}

// Decide evaluates the policy against the current observation. Precedence:
// finite EOF, start timeout, max-utterance cap, provider endpoint, then trailing
// silence. A finished utterance shorter than MinSpeech is rejected as TooShort.
func (p Policy) Decide(o Observation) Decision {
	seen := o.SpeechSeen
	decide := func(k DecisionKind, reason string) Decision {
		return Decision{Kind: k, Reason: reason, AudioSeen: seen}
	}

	// 1. A finite source must end deterministically on EOF, not on silence.
	if o.SourceFinal {
		switch {
		case !o.HasSpeech:
			return decide(SourceExhausted, "source ended without speech")
		case p.FinalizeOnEOF && seen >= p.MinSpeech:
			return decide(Finalize, "source EOF after sufficient speech")
		case p.FinalizeOnEOF:
			return decide(TooShort, "source EOF but speech shorter than MinSpeech")
		default:
			return decide(SourceExhausted, "source ended")
		}
	}

	// 2. No speech yet: keep waiting, or time out the silence.
	if !o.HasSpeech {
		if p.StartTimeout > 0 && o.Elapsed >= p.StartTimeout {
			return decide(Timeout, "no speech before start timeout")
		}
		return decide(Continue, "waiting for speech")
	}

	// 3. Utterance length cap.
	if p.MaxUtterance > 0 && o.Elapsed >= p.MaxUtterance {
		if seen >= p.MinSpeech {
			return decide(Finalize, "max utterance reached")
		}
		return decide(TooShort, "max utterance reached but speech too short")
	}

	// 4. Provider-signalled endpoint, when trusted.
	if p.AllowProviderEnd && o.ProviderEnd {
		if seen >= p.MinSpeech {
			return decide(Finalize, "provider endpoint")
		}
		return decide(TooShort, "provider endpoint but speech too short")
	}

	// 5. Trailing silence past the threshold finishes the utterance.
	if p.MinSilence > 0 && o.TrailingSilence >= p.MinSilence {
		if seen >= p.MinSpeech {
			return decide(Finalize, "silence after speech")
		}
		return decide(TooShort, "silence after speech shorter than MinSpeech")
	}

	return decide(Continue, "speech in progress")
}

// Default minimum speech length when config provides none. Short enough to keep
// brief commands ("what time is it"), long enough to reject incidental blips.
const defaultMinSpeech = 200 * time.Millisecond

// FromConfig builds a Policy from existing config fields without mutating user
// config. finite selects the finite-source variant (fixtures and files): it
// finalizes on EOF and does not apply a no-speech start timeout, because the
// source's end — not a clock — bounds it.
func FromConfig(cfg *config.Config, finite bool) Policy {
	p := Policy{
		MinSpeech:    defaultMinSpeech,
		MinSilence:   secondsOr(cfg.VADSilenceDuration, 500*time.Millisecond),
		MaxUtterance: secondsOr(float64(cfg.PhraseTimeLimit), 30*time.Second),
		StartTimeout: secondsOr(float64(cfg.ListenTimeout), 10*time.Second),
	}
	if finite {
		p.FinalizeOnEOF = true
		p.StartTimeout = 0
	}
	return p
}

// secondsOr converts a seconds value to a Duration, falling back when not positive.
func secondsOr(seconds float64, fallback time.Duration) time.Duration {
	if seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds * float64(time.Second))
}
