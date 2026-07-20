package speaker

import (
	"context"
	"fmt"
	"sync"
)

// FakeEngine is a deterministic Engine for tests (no native models).
// It is safe for concurrent use (extra vs production contract) so race tests
// of the Analyzer do not need a single-threaded fake.
type FakeEngine struct {
	mu sync.Mutex

	// EmbedDim is the length of returned embeddings (default 4).
	EmbedDim int
	// NextEmbed is returned by Embed when non-nil; otherwise a zero vector.
	NextEmbed []float32
	// Identities maps a simple hash of first embedding dim to a label.
	// Confidence is always 0.95 for hits and 0.1 for misses (raw scores;
	// Analyzer applies threshold).
	Identities map[string]string
	// Diarization is returned by Diarize when non-nil.
	Diarization Timeline
	// FailEmbed / FailDiarize inject errors.
	FailEmbed   error
	FailDiarize error
	// SlowDiarize blocks Diarize until the channel receives or context cancels.
	SlowDiarize <-chan struct{}
	Closed      bool
}

func (f *FakeEngine) dim() int {
	if f.EmbedDim > 0 {
		return f.EmbedDim
	}
	return 4
}

func (f *FakeEngine) Embed(ctx context.Context, samples []float32) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Closed {
		return nil, fmt.Errorf("speaker: fake engine closed")
	}
	if f.FailEmbed != nil {
		return nil, f.FailEmbed
	}
	if f.NextEmbed != nil {
		out := make([]float32, len(f.NextEmbed))
		copy(out, f.NextEmbed)
		return out, nil
	}
	out := make([]float32, f.dim())
	if len(samples) > 0 {
		out[0] = samples[0]
	}
	return out, nil
}

// Identify returns the best candidate and raw score — no threshold filtering.
func (f *FakeEngine) Identify(ctx context.Context, embedding []float32) (string, float32, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Closed {
		return "", 0, fmt.Errorf("speaker: fake engine closed")
	}
	if len(embedding) == 0 || f.Identities == nil {
		return LabelUnknown, 0, nil
	}
	key := fmt.Sprintf("%.4f", embedding[0])
	if name, ok := f.Identities[key]; ok {
		return name, 0.95, nil
	}
	return LabelUnknown, 0.1, nil
}

func (f *FakeEngine) Verify(ctx context.Context, name string, embedding []float32, threshold float32) (bool, error) {
	label, conf, err := f.Identify(ctx, embedding)
	if err != nil {
		return false, err
	}
	if LabelsEqual(label, name) && conf >= threshold {
		return true, nil
	}
	return false, nil
}

func (f *FakeEngine) Diarize(ctx context.Context, samples []float32, numSpeakers int) (Timeline, error) {
	if err := ctx.Err(); err != nil {
		return Timeline{}, err
	}
	if f.SlowDiarize != nil {
		select {
		case <-ctx.Done():
			return Timeline{}, ctx.Err()
		case <-f.SlowDiarize:
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Closed {
		return Timeline{}, fmt.Errorf("speaker: fake engine closed")
	}
	if f.FailDiarize != nil {
		return Timeline{}, f.FailDiarize
	}
	if f.Diarization.Observations != nil {
		return f.Diarization, nil
	}
	n := numSpeakers
	if n <= 0 {
		n = 2
	}
	if n > 2 {
		n = 2
	}
	if len(samples) == 0 {
		return Timeline{}, nil
	}
	totalMS := int64(float64(len(samples)) / 16000.0 * 1000.0)
	var obs []Observation
	span := totalMS / int64(n)
	for i := 0; i < n; i++ {
		start := int64(i) * span
		end := start + span
		if i == n-1 {
			end = totalMS
		}
		obs = append(obs, Observation{
			SegmentID:  fmt.Sprintf("seg-%d", i),
			StartMS:    start,
			EndMS:      end,
			Label:      MapDiarizationID(i),
			Confidence: 0.9,
			State:      StateStable,
			Source:     SourceRecording,
		})
	}
	return Timeline{Observations: obs}, nil
}

func (f *FakeEngine) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Closed = true
	return nil
}
