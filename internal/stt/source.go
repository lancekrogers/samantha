package stt

import "github.com/lancekrogers/samantha/internal/audio"

// audioSource is the legacy untyped capture contract: Read returns the next
// chunk of samples, or nil when none is available. It cannot tell a live source
// waiting for audio apart from a finite source that has ended — the ambiguity
// FrameSource replaces.
type audioSource interface {
	Read() []float32
}

type finiteAudioSource interface {
	Exhausted() bool
}

// FrameSource is the typed audio input contract STT providers migrate to. It
// disambiguates live "no frame yet" from finite end-of-input; see
// audio.FrameSource for the full error and lifecycle semantics.
type FrameSource = audio.FrameSource

func sourceExhausted(source audioSource) bool {
	finite, ok := source.(finiteAudioSource)
	return ok && finite.Exhausted()
}

// sourceKind classifies a legacy audioSource as a finite fixture or a live
// stream, so providers populating audio.Frame can label SourceKind correctly
// while sources are migrated to the frame contract.
func sourceKind(source audioSource) audio.SourceKind {
	if _, ok := source.(finiteAudioSource); ok {
		return audio.SourceFixture
	}
	return audio.SourceLive
}
