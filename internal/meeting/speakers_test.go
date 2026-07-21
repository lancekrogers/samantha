package meeting

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lancekrogers/samantha/internal/speaker"
)

type fakeSpeakerAnalyzer struct {
	timeline speaker.Timeline
	err      error
}

func (f fakeSpeakerAnalyzer) Finalize(context.Context, []float32) (speaker.Timeline, error) {
	return f.timeline, f.err
}

func TestAnalyzeRecordingPreservesSourceAndWritesTimelineOnly(t *testing.T) {
	samples := []float32{1, 2, 3}
	timeline := speaker.Timeline{Observations: []speaker.Observation{{
		SegmentID: "one", StartMS: 0, EndMS: 1000, Label: "speaker-1", Confidence: .9, State: speaker.StateStable,
	}}}
	result := AnalyzeRecording(context.Background(), fakeSpeakerAnalyzer{timeline: timeline}, samples)
	if result.Status != AnalysisComplete || len(result.Timeline.Observations) != 1 {
		t.Fatalf("result = %+v, want complete timeline", result)
	}
	if len(samples) != 3 || samples[0] != 1 {
		t.Fatal("analysis mutated source samples")
	}
	path := filepath.Join(t.TempDir(), "speaker-analysis.json")
	if err := WriteAnalysis(path, result); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got AnalysisResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != AnalysisComplete || string(data) == "" {
		t.Fatalf("persisted result = %+v", got)
	}
}

func TestAnalyzeRecordingDisabledAndError(t *testing.T) {
	if got := AnalyzeRecording(context.Background(), nil, nil); got.Status != AnalysisDisabled {
		t.Fatalf("disabled result = %+v", got)
	}
	got := AnalyzeRecording(context.Background(), fakeSpeakerAnalyzer{err: errors.New("engine unavailable")}, []float32{1})
	if got.Status != AnalysisError || got.Error != "engine unavailable" {
		t.Fatalf("error result = %+v", got)
	}
}

func TestAttributeTranscriptOverlapRules(t *testing.T) {
	segments := []TranscriptSegment{
		{ID: "clean", StartMS: 100, EndMS: 300, Text: "hello"},
		{ID: "tie", StartMS: 300, EndMS: 400, Text: "ambiguous"},
		{ID: "silence", StartMS: 900, EndMS: 1000, Text: "quiet"},
		{ID: "overlap", StartMS: 500, EndMS: 700, Text: "two voices"},
	}
	timeline := speaker.Timeline{Observations: []speaker.Observation{
		{SegmentID: "a", StartMS: 0, EndMS: 400, Label: "alice", Confidence: .9, State: speaker.StateStable},
		{SegmentID: "b", StartMS: 300, EndMS: 500, Label: "bob", Confidence: .8, State: speaker.StateStable},
		{SegmentID: "c", StartMS: 500, EndMS: 700, Label: "alice", Confidence: .5, State: speaker.StateOverlap},
	}}
	got := AttributeTranscript(segments, timeline)
	if got[0].Speaker != "alice" || got[0].State != speaker.StateStable {
		t.Fatalf("clean attribution = %+v", got[0])
	}
	if got[1].Speaker != speaker.LabelUnknown || got[1].State != speaker.StateOverlap {
		t.Fatalf("tie attribution = %+v", got[1])
	}
	if got[2].Speaker != speaker.LabelUnknown || got[2].State != speaker.StateRejected {
		t.Fatalf("silence attribution = %+v", got[2])
	}
	if got[3].Speaker != speaker.LabelUnknown || got[3].State != speaker.StateOverlap {
		t.Fatalf("overlap attribution = %+v", got[3])
	}
}
