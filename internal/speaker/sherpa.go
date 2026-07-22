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
	sherpaLiveRev            = "nemo-titanet-small-live"
	// minLiveEmbedSamples is ~0.5s at 16 kHz — shorter clips are unreliable.
	minLiveEmbedSamples = 8000
	// defaultSearchThreshold is used for the embedding manager when config
	// threshold is unset/invalid. Cosine similarity style scores.
	defaultSearchThreshold = 0.55
	// matchConfidence is the raw score returned for a manager Search hit so
	// Analyzer.ApplyThreshold keeps the label (product threshold is separate).
	matchConfidence = 0.95
	// newSpeakerConfidence is used when auto-registering a provisional speaker.
	newSpeakerConfidence = 0.9
)

// SherpaModelPaths are the resolved files used by offline diarization and live
// embedding.
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

// SherpaEngine is the production engine for offline meeting diarization and
// live speaker embedding identification (TitaNet + embedding manager).
type SherpaEngine struct {
	mu sync.Mutex

	// Offline meeting diarization (optional when constructed live-only).
	diarizer offlineDiarizer
	native   *sherpa.OfflineSpeakerDiarization

	// Live identification (optional when constructed meeting-only).
	extractor *sherpa.SpeakerEmbeddingExtractor
	manager   *sherpa.SpeakerEmbeddingManager
	// searchThreshold is applied by the manager Search path.
	searchThreshold float32
	// nextSpeaker allocates speaker-N labels for unrecruited voices.
	nextSpeaker int

	closed bool
}

// NewSherpaEngine loads segmentation + embedding models for meeting diarization
// and enables live Embed/Identify via a separate embedding extractor/manager.
// Callers should run config.EnsureRuntimeAssets first when using managed defaults.
func NewSherpaEngine(cfg Config, modelsDir string) (*SherpaEngine, error) {
	paths := ResolveSherpaModelPaths(cfg, modelsDir)
	for _, model := range []struct{ label, path string }{
		{"segmentation", paths.Segmentation},
		{"embedding", paths.Embedding},
	} {
		if err := requireModelFile(model.label, model.path); err != nil {
			return nil, err
		}
	}

	threads := sherpaThreads()
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

	engine := &SherpaEngine{
		diarizer:        native,
		native:          native,
		searchThreshold: searchThreshold(cfg),
	}
	if err := engine.initLiveEmbedding(paths.Embedding, threads); err != nil {
		_ = engine.Close()
		return nil, err
	}
	return engine, nil
}

// NewSherpaLiveEngine loads only the embedding model for conversation live
// identification (no pyannote diarization). Prefer this for chat to avoid
// loading meeting-only models when live is the sole active path.
func NewSherpaLiveEngine(cfg Config, modelsDir string) (*SherpaEngine, error) {
	paths := ResolveSherpaModelPaths(cfg, modelsDir)
	if err := requireModelFile("embedding", paths.Embedding); err != nil {
		return nil, err
	}
	engine := &SherpaEngine{searchThreshold: searchThreshold(cfg)}
	if err := engine.initLiveEmbedding(paths.Embedding, sherpaThreads()); err != nil {
		return nil, err
	}
	return engine, nil
}

func (e *SherpaEngine) initLiveEmbedding(modelPath string, threads int) error {
	ex := sherpa.NewSpeakerEmbeddingExtractor(&sherpa.SpeakerEmbeddingExtractorConfig{
		Model:      modelPath,
		NumThreads: threads,
		Provider:   "cpu",
	})
	if ex == nil {
		return fmt.Errorf("speaker: initialize embedding extractor")
	}
	mgr := sherpa.NewSpeakerEmbeddingManager(ex.Dim())
	if mgr == nil {
		sherpa.DeleteSpeakerEmbeddingExtractor(ex)
		return fmt.Errorf("speaker: initialize embedding manager")
	}
	e.extractor = ex
	e.manager = mgr
	return nil
}

func requireModelFile(label, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("speaker: %s model %s: %w", label, path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("speaker: %s model %s is a directory", label, path)
	}
	return nil
}

func sherpaThreads() int {
	threads := runtime.NumCPU()
	if threads < 1 {
		threads = 1
	}
	if threads > 4 {
		threads = 4
	}
	return threads
}

func searchThreshold(cfg Config) float32 {
	th := cfg.LiveThreshold()
	if th <= 0 || th > 1 {
		return defaultSearchThreshold
	}
	// Manager cosine thresholds are typically lower than product UI scores;
	// clamp into a usable band.
	if th > 0.85 {
		return 0.65
	}
	if th < 0.4 {
		return defaultSearchThreshold
	}
	return th
}

// Embed computes a speaker embedding for 16 kHz mono samples.
func (e *SherpaEngine) Embed(ctx context.Context, samples []float32) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(samples) < minLiveEmbedSamples {
		return nil, fmt.Errorf("speaker: need at least %d samples for live embedding (got %d)", minLiveEmbedSamples, len(samples))
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.extractor == nil {
		return nil, fmt.Errorf("speaker: live embedding unavailable")
	}

	stream := e.extractor.CreateStream()
	if stream == nil {
		return nil, fmt.Errorf("speaker: create embedding stream")
	}
	defer sherpa.DeleteOnlineStream(stream)

	stream.AcceptWaveform(16000, samples)
	stream.InputFinished()
	if !e.extractor.IsReady(stream) {
		return nil, fmt.Errorf("speaker: embedding extractor not ready for this clip")
	}
	emb := e.extractor.Compute(stream)
	if len(emb) == 0 {
		return nil, fmt.Errorf("speaker: empty embedding")
	}
	out := make([]float32, len(emb))
	copy(out, emb)
	return out, nil
}

// Identify finds the best enrolled speaker or auto-registers a new speaker-N
// label for indicator mode (no prior enrollment required).
func (e *SherpaEngine) Identify(ctx context.Context, embedding []float32) (string, float32, error) {
	if err := ctx.Err(); err != nil {
		return LabelUnknown, 0, err
	}
	if len(embedding) == 0 {
		return LabelUnknown, 0, nil
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.manager == nil {
		return LabelUnknown, 0, fmt.Errorf("speaker: live identification unavailable")
	}

	if name := strings.TrimSpace(e.manager.Search(embedding, e.searchThreshold)); name != "" {
		return name, matchConfidence, nil
	}

	// No match: allocate a stable provisional label and enroll it so subsequent
	// turns can re-identify the same voice without a separate enrollment UI.
	e.nextSpeaker++
	name := fmt.Sprintf("%s%d", LabelSpeakerPrefix, e.nextSpeaker)
	if !e.manager.Register(name, embedding) {
		return LabelUnknown, 0, fmt.Errorf("speaker: failed to register %s", name)
	}
	return name, newSpeakerConfidence, nil
}

// Verify checks whether embedding matches an enrolled name at threshold.
func (e *SherpaEngine) Verify(ctx context.Context, name string, embedding []float32, threshold float32) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	name = strings.TrimSpace(name)
	if name == "" || len(embedding) == 0 {
		return false, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.manager == nil {
		return false, fmt.Errorf("speaker: live verification unavailable")
	}
	if threshold <= 0 {
		threshold = e.searchThreshold
	}
	return e.manager.Verify(name, embedding, threshold), nil
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
		return Timeline{}, fmt.Errorf("speaker: sherpa diarization unavailable")
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
	if e.manager != nil {
		sherpa.DeleteSpeakerEmbeddingManager(e.manager)
		e.manager = nil
	}
	if e.extractor != nil {
		sherpa.DeleteSpeakerEmbeddingExtractor(e.extractor)
		e.extractor = nil
	}
	return nil
}

// LiveEnabled reports whether Embed/Identify are available.
func (e *SherpaEngine) LiveEnabled() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return !e.closed && e.extractor != nil && e.manager != nil
}
