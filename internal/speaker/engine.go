package speaker

import (
	"context"
	"time"
)

// Segment is a speech span with optional PCM for embedding or diarization.
type Segment struct {
	ID      string
	Start   time.Duration
	End     time.Duration
	Samples []float32 // 16 kHz mono; may be nil for metadata-only paths
	Source  Source
}

// Engine is the model-facing boundary (sherpa wrappers implement this).
// Implementations must be safe for use from a single worker goroutine.
type Engine interface {
	// Embed returns a speaker embedding for samples (16 kHz mono).
	Embed(ctx context.Context, samples []float32) ([]float32, error)
	// Identify returns a profile name or LabelUnknown when below threshold.
	Identify(ctx context.Context, embedding []float32, threshold float32) (label string, confidence float32, err error)
	// Verify checks whether embedding matches an enrolled name.
	Verify(ctx context.Context, name string, embedding []float32, threshold float32) (bool, error)
	// Diarize runs offline multi-speaker segmentation on a full buffer.
	Diarize(ctx context.Context, samples []float32, numSpeakers int) (Timeline, error)
	// Close releases native resources.
	Close() error
}
