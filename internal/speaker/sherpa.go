package speaker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

const (
	defaultSegmentationModel = "speaker/pyannote-segmentation-3.0/model.int8.onnx"
	defaultEmbeddingModel    = "speaker/nemo_en_titanet_small.onnx"
	sherpaDiarizationRev     = "pyannote-segmentation-3.0-int8+nemo-titanet-small"
)

// SherpaModelPaths are the resolved files used by offline diarization.
type SherpaModelPaths struct {
	Segmentation string
	Embedding    string
}

// ResolveSherpaModelPaths applies the managed defaults and resolves relative
// overrides beneath modelsDir. Absolute overrides are preserved.
func ResolveSherpaModelPaths(cfg Config, modelsDir string) SherpaModelPaths {
	resolve := func(value, fallback string) string {
		value = strings.TrimSpace(value)
		if value == "" {
			value = fallback
		}
		if filepath.IsAbs(value) {
			return filepath.Clean(value)
		}
		return filepath.Join(modelsDir, filepath.Clean(value))
	}
	return SherpaModelPaths{
		Segmentation: resolve(cfg.Models.Segmentation, defaultSegmentationModel),
		Embedding:    resolve(cfg.Models.Embedding, defaultEmbeddingModel),
	}
}

type offlineDiarizer interface {
	Process([]float32) []sherpa.OfflineSpeakerDiarizationSegment
}

// SherpaEngine is the production meeting-diarization engine. The current
// product activation is deliberately meeting-only; live identification still
// requires enrollment storage and a score-bearing manager contract.
type SherpaEngine struct {
	mu       sync.Mutex
	diarizer offlineDiarizer
	native   *sherpa.OfflineSpeakerDiarization
	closed   bool
}

// NewSherpaEngine loads the local segmentation and embedding models. Callers
// should run config.EnsureRuntimeAssets first when managed defaults are used.
func NewSherpaEngine(cfg Config, modelsDir string) (*SherpaEngine, error) {
	paths := ResolveSherpaModelPaths(cfg, modelsDir)
	for _, model := range []struct{ label, path string }{
		{"segmentation", paths.Segmentation},
		{"embedding", paths.Embedding},
	} {
		label, path := model.label, model.path
		if info, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("speaker: %s model %s: %w", label, path, err)
		} else if info.IsDir() {
			return nil, fmt.Errorf("speaker: %s model %s is a directory", label, path)
		}
	}

	threads := runtime.NumCPU()
	if threads < 1 {
		threads = 1
	}
	if threads > 4 {
		threads = 4
	}
	native := sherpa.NewOfflineSpeakerDiarization(&sherpa.OfflineSpeakerDiarizationConfig{
		Segmentation: sherpa.OfflineSpeakerSegmentationModelConfig{
			Pyannote:   sherpa.OfflineSpeakerSegmentationPyannoteModelConfig{Model: paths.Segmentation},
			NumThreads: threads,
			Provider:   "cpu",
		},
		Embedding: sherpa.SpeakerEmbeddingExtractorConfig{
			Model:      paths.Embedding,
			NumThreads: threads,
			Provider:   "cpu",
		},
		Clustering: sherpa.FastClusteringConfig{
			NumClusters: cfg.Meeting.NumSpeakers,
			Threshold:   0.9,
		},
		MinDurationOn:  0.3,
		MinDurationOff: 0.5,
	})
	if native == nil {
		return nil, fmt.Errorf("speaker: initialize sherpa offline diarization")
	}
	return &SherpaEngine{diarizer: native, native: native}, nil
}

func (e *SherpaEngine) Embed(context.Context, []float32) ([]float32, error) {
	return nil, fmt.Errorf("speaker: live embedding is not enabled by the meeting diarization engine")
}

func (e *SherpaEngine) Identify(context.Context, []float32) (string, float32, error) {
	return LabelUnknown, 0, fmt.Errorf("speaker: live identification is not enabled by the meeting diarization engine")
}

func (e *SherpaEngine) Verify(context.Context, string, []float32, float32) (bool, error) {
	return false, fmt.Errorf("speaker: live verification is not enabled by the meeting diarization engine")
}

// Diarize maps sherpa's zero-based speaker ids to stable, human-facing
// speaker-1..N labels. The binding does not expose per-segment confidence, so
// confidence remains zero rather than inventing a score.
func (e *SherpaEngine) Diarize(ctx context.Context, samples []float32, numSpeakers int) (Timeline, error) {
	if err := ctx.Err(); err != nil {
		return Timeline{}, err
	}
	if len(samples) == 0 {
		return Timeline{}, nil
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.diarizer == nil {
		return Timeline{}, fmt.Errorf("speaker: sherpa engine closed")
	}
	if e.native != nil {
		e.native.SetConfig(&sherpa.OfflineSpeakerDiarizationConfig{
			Clustering: sherpa.FastClusteringConfig{NumClusters: numSpeakers, Threshold: 0.9},
		})
	}
	segments := e.diarizer.Process(samples)
	if err := ctx.Err(); err != nil {
		return Timeline{}, err
	}
	return timelineFromSherpa(segments), nil
}

func timelineFromSherpa(segments []sherpa.OfflineSpeakerDiarizationSegment) Timeline {
	observations := make([]Observation, 0, len(segments))
	for i, segment := range segments {
		start := int64(segment.Start * 1000)
		end := int64(segment.End * 1000)
		if end <= start {
			continue
		}
		observations = append(observations, Observation{
			SegmentID: fmt.Sprintf("diarization-%d", i+1),
			StartMS:   start,
			EndMS:     end,
			Label:     fmt.Sprintf("%s%d", LabelSpeakerPrefix, segment.Speaker+1),
			State:     StateStable,
			Source:    SourceRecording,
			ModelRev:  sherpaDiarizationRev,
		})
	}
	return Timeline{Observations: observations}
}

func (e *SherpaEngine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	if e.native != nil {
		sherpa.DeleteOfflineSpeakerDiarization(e.native)
	}
	e.native = nil
	e.diarizer = nil
	return nil
}
