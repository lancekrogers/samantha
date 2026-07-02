package stt

import (
	"context"

	"github.com/lancekrogers/samantha/internal/audio"
)

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

// asFrameSource returns src as an audio.FrameSource, using its native frame
// contract when implemented (live capture and fixtures now are) or adapting the
// legacy Read()/Exhausted() behavior otherwise.
func asFrameSource(src audioSource) audio.FrameSource {
	if fs, ok := src.(audio.FrameSource); ok {
		return fs
	}
	return newLegacyFrameSource(src)
}

// legacyFrameSource adapts a legacy Read()-based audioSource to the frame
// contract for any source that does not implement FrameSource natively. Finite
// sources report EOF as a Final frame via Exhausted; live sources surface an
// empty read as ErrNoFrameReady and never finalize.
type legacyFrameSource struct {
	src  audioSource
	kind audio.SourceKind
	seq  int64
}

func newLegacyFrameSource(src audioSource) *legacyFrameSource {
	return &legacyFrameSource{src: src, kind: sourceKind(src)}
}

func (l *legacyFrameSource) ReadFrame(ctx context.Context) (audio.Frame, error) {
	if err := ctx.Err(); err != nil {
		return audio.Frame{}, err
	}

	samples := l.src.Read()
	if len(samples) == 0 {
		if l.kind != audio.SourceLive && sourceExhausted(l.src) {
			return audio.Frame{SourceKind: l.kind, Final: true}, nil
		}
		return audio.Frame{}, audio.ErrNoFrameReady
	}

	l.seq++
	return audio.Frame{
		Samples:    samples,
		SampleRate: audio.SampleRate,
		Channels:   audio.Channels,
		Duration:   audio.SamplesDuration(len(samples)),
		Sequence:   l.seq,
		SourceKind: l.kind,
	}, nil
}

func (l *legacyFrameSource) Close() error { return nil }
