package log

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Background diarization finishes after the bundle trailer lands; the
// analysis write must reopen the closed files instead of failing.
func TestWriteSpeakerAnalysisAfterCloseAppends(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "standup.meeting")
	w, err := CreateBundle(bundle, "Standup", "fake", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.AddNote("action item"); err != nil {
		t.Fatal(err)
	}
	summary, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}

	analysis := SpeakerAnalysis{
		Status: "complete",
		Segments: []SpeakerSegment{
			{ID: "s1", StartMS: 0, EndMS: 900, Label: "speaker-1"},
		},
		Utterances: []SpeakerUtterance{
			{
				TranscriptRecord: TranscriptRecord{ID: "u1", StartMS: 0, EndMS: 900, Text: "hello"},
				Speaker:          "speaker-1",
			},
		},
	}
	if err := w.WriteSpeakerAnalysis(analysis); err != nil {
		t.Fatalf("post-close analysis write: %v", err)
	}

	events, err := os.ReadFile(summary.JSONLFile)
	if err != nil {
		t.Fatal(err)
	}
	endIdx := strings.Index(string(events), `"type":"session_end"`)
	analysisIdx := strings.Index(string(events), `"type":"speaker_analysis"`)
	if endIdx < 0 || analysisIdx < 0 || analysisIdx < endIdx {
		t.Fatalf("analysis must append after session_end:\n%s", events)
	}
	if !strings.Contains(string(events), `"type":"speaker_utterance"`) {
		t.Fatalf("missing attributed utterance:\n%s", events)
	}
	doc, err := os.ReadFile(summary.File)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), "# Speaker analysis: complete") {
		t.Fatalf("missing analysis section:\n%s", doc)
	}

	// Writer stays closed and idempotent after the enrichment append.
	again, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}
	if again.Bundle != summary.Bundle {
		t.Fatalf("idempotent Close changed summary: %+v", again)
	}
}

func TestWriteRoutePlanLifecycle(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "planned.meeting")
	w, err := CreateBundle(bundle, "Planned", "fake", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteRoutePlan("", "dest"); err == nil {
		t.Fatal("empty destination must be rejected")
	}
	if err := w.WriteRoutePlan("camp:obey-campaign", "dest"); err != nil {
		t.Fatal(err)
	}
	summary, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}
	events, err := os.ReadFile(summary.JSONLFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(events), `"type":"route_plan"`) ||
		!strings.Contains(string(events), "camp:obey-campaign") {
		t.Fatalf("missing route_plan event:\n%s", events)
	}
	if err := w.WriteRoutePlan("camp:obey-campaign", "dest"); err == nil {
		t.Fatal("route plan after Close must be rejected")
	}
}
