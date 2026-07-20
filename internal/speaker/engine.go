package speaker

import (
	"context"
	"time"
)

// Segment is a speech span with optional PCM for embedding or diarization.
type Segment struct {
	ID      string
	Start   time.Duration // session-relative
	End     time.Duration
	Samples []float32 // 16 kHz mono; may be nil for metadata-only paths
	Source  Source
}

// Engine is the model-facing boundary (sherpa wrappers implement this).
//
// Concurrency: Engine methods must only be called from one goroutine at a time
// (or the implementation must document its own locking). Analyzer serializes
// all engine calls on its engineMu.
//
// Threshold contract:
//   - Identify returns the best candidate label and a raw confidence score.
//     It does NOT apply product thresholding; empty/unknown label means no
//     candidate. Callers (Analyzer) own ApplyThreshold.
//   - Verify applies the given threshold (engine-side match test).
type Engine interface {
	// Embed returns a speaker embedding for samples (16 kHz mono).
	Embed(ctx context.Context, samples []float32) ([]float32, error)
	// Identify returns the best candidate and raw score (no threshold filter).
	// label may be empty or LabelUnknown when no candidate exists.
	Identify(ctx context.Context, embedding []float32) (label string, confidence float32, err error)
	// Verify checks whether embedding matches an enrolled name at threshold.
	Verify(ctx context.Context, name string, embedding []float32, threshold float32) (bool, error)
	// Diarize runs offline multi-speaker segmentation on a full buffer.
	Diarize(ctx context.Context, samples []float32, numSpeakers int) (Timeline, error)
	// Close releases native resources. Must not be called concurrently with
	// other Engine methods (Analyzer enforces this).
	Close() error
}
