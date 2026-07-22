package speaker

import (
	"context"
	"path/filepath"
	"testing"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

type fakeOfflineDiarizer struct {
	segments []sherpa.OfflineSpeakerDiarizationSegment
}

func (f fakeOfflineDiarizer) Process([]float32) []sherpa.OfflineSpeakerDiarizationSegment {
	return f.segments
}

func TestResolveSherpaModelPaths(t *testing.T) {
	models := t.TempDir()
	got := ResolveSherpaModelPaths(Config{}, models)
	if got.Segmentation != filepath.Join(models, defaultSegmentationModel) ||
		got.Embedding != filepath.Join(models, defaultEmbeddingModel) {
		t.Fatalf("default paths = %+v", got)
	}

	abs := filepath.Join(t.TempDir(), "embedding.onnx")
	got = ResolveSherpaModelPaths(Config{Models: ModelsConfig{
		Segmentation: "custom/seg.onnx",
		Embedding:    abs,
	}}, models)
	if got.Segmentation != filepath.Join(models, "custom/seg.onnx") || got.Embedding != abs {
		t.Fatalf("override paths = %+v", got)
	}
}

func TestSherpaEngineMapsNativeSegments(t *testing.T) {
	engine := &SherpaEngine{diarizer: fakeOfflineDiarizer{segments: []sherpa.OfflineSpeakerDiarizationSegment{
		{Start: 0.25, End: 1.5, Speaker: 0},
		{Start: 1.6, End: 3.0, Speaker: 2},
		{Start: 4, End: 4, Speaker: 1}, // invalid span is rejected
	}}}
	timeline, err := engine.Diarize(context.Background(), []float32{0.1}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(timeline.Observations) != 2 {
		t.Fatalf("observations = %+v", timeline.Observations)
	}
	first, second := timeline.Observations[0], timeline.Observations[1]
	if first.Label != "speaker-1" || first.StartMS != 250 || first.EndMS != 1500 {
		t.Fatalf("first observation = %+v", first)
	}
	if second.Label != "speaker-3" || second.State != StateStable || second.ModelRev == "" {
		t.Fatalf("second observation = %+v", second)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Diarize(context.Background(), []float32{1}, 0); err == nil {
		t.Fatal("closed engine should reject diarization")
	}
}

func TestSherpaEngineHonorsCanceledContextBeforeNativeCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	engine := &SherpaEngine{diarizer: fakeOfflineDiarizer{}}
	if _, err := engine.Diarize(ctx, []float32{1}, 0); err == nil {
		t.Fatal("expected context cancellation")
	}
}
