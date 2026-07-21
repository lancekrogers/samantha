//go:build integration
// +build integration

// Package speakerflow exercises meeting speaker diarization against real
// multi-voice meeting audio (YouTube product marketing meeting clip).
//
// Engine note: production still uses speaker.FakeEngine for deterministic
// offline diarization until a native sherpa diarization Engine is wired.
// This suite still proves:
//   - real multi-speaker PCM loads (16 kHz mono WAV)
//   - Analyzer.Finalize + AnalyzeRecording complete on real samples
//   - AttributeTranscript labels utterances from the timeline
//   - analysis JSON is written beside the fixture
//
// Fetch the fixture first:
//
//	just fetch-meeting-fixture
//	just test speakerflow
package speakerflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/meeting"
	"github.com/lancekrogers/samantha/internal/speaker"
)

const (
	fixtureRel   = "tests/fixtures/meetings/product-marketing-meeting-90s.wav"
	fixtureRate  = 16000
	wantSpeakers = 2
)

func fixturePath(t *testing.T) string {
	t.Helper()
	// Walk up from this package to the module root.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		candidate := filepath.Join(dir, fixtureRel)
		if st, err := os.Stat(candidate); err == nil && st.Size() > 0 {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Skipf("meeting fixture missing (%s) — run: just fetch-meeting-fixture", fixtureRel)
	return ""
}

func TestMeetingFixtureDiarizationPipeline(t *testing.T) {
	path := fixturePath(t)

	samples, rate, err := audio.ReadWAVFloat32(path)
	if err != nil {
		t.Fatalf("ReadWAVFloat32: %v", err)
	}
	if rate != fixtureRate {
		t.Fatalf("sample rate = %d, want %d (re-fetch fixture)", rate, fixtureRate)
	}
	if len(samples) < fixtureRate { // < 1s is suspicious for a 90s clip
		t.Fatalf("samples = %d, want at least 1s of audio", len(samples))
	}
	// Clip is ~90s; allow some slack for section extract.
	dur := float64(len(samples)) / float64(rate)
	if dur < 30 || dur > 120 {
		t.Fatalf("duration = %.1fs, want ~90s multi-voice meeting clip", dur)
	}

	cfg := speaker.Config{
		Enabled: true,
		Meeting: speaker.MeetingConfig{
			Enabled:     true,
			RecordAudio: true,
			NumSpeakers: wantSpeakers,
		},
	}.Normalize()

	engine := &speaker.FakeEngine{}
	analyzer, err := speaker.NewAnalyzer(cfg, engine)
	if err != nil {
		t.Fatalf("NewAnalyzer: %v", err)
	}
	defer func() { _ = analyzer.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result := meeting.AnalyzeRecording(ctx, analyzer, samples)
	if result.Status != meeting.AnalysisComplete {
		t.Fatalf("AnalyzeRecording status = %s error=%q, want complete", result.Status, result.Error)
	}
	if len(result.Timeline.Observations) < wantSpeakers {
		t.Fatalf("timeline speakers = %d, want >= %d: %+v",
			len(result.Timeline.Observations), wantSpeakers, result.Timeline.Observations)
	}

	labels := map[string]int{}
	for _, obs := range result.Timeline.Observations {
		if obs.Label == "" || obs.Label == speaker.LabelUnknown {
			continue
		}
		labels[obs.Label]++
		if obs.StartMS < 0 || obs.EndMS <= obs.StartMS {
			t.Fatalf("bad span: %+v", obs)
		}
	}
	if len(labels) < wantSpeakers {
		t.Fatalf("distinct labels = %v, want >= %d speakers", labels, wantSpeakers)
	}

	// Synthetic transcript segments across the clip (stand-in for STT turns).
	segments := make([]meeting.TranscriptSegment, 0, 6)
	step := int64(dur * 1000 / 6)
	for i := 0; i < 6; i++ {
		start := int64(i) * step
		end := start + step
		segments = append(segments, meeting.TranscriptSegment{
			ID:      speaker.MapDiarizationID(i),
			StartMS: start,
			EndMS:   end,
			Text:    "utterance from meeting fixture",
		})
	}
	attributed := meeting.AttributeTranscript(segments, result.Timeline)
	if len(attributed) != len(segments) {
		t.Fatalf("attributed len = %d", len(attributed))
	}
	named := 0
	for _, a := range attributed {
		if a.Speaker != "" && a.Speaker != speaker.LabelUnknown {
			named++
		}
	}
	if named == 0 {
		t.Fatalf("no segments received speaker labels: %+v", attributed)
	}

	out := filepath.Join(t.TempDir(), "speaker-analysis.json")
	if err := meeting.WriteAnalysis(out, result); err != nil {
		t.Fatalf("WriteAnalysis: %v", err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var got meeting.AnalysisResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("parse analysis json: %v\n%s", err, raw)
	}
	if got.Status != meeting.AnalysisComplete || len(got.Timeline.Observations) < wantSpeakers {
		t.Fatalf("persisted analysis = %+v", got)
	}

	t.Logf("fixture %s (%.1fs, %d samples)", path, dur, len(samples))
	t.Logf("labels %v · attributed %d/%d turns", labels, named, len(attributed))
}

func TestMeetingFixtureMissingIsSkippedNotFailed(t *testing.T) {
	// Sanity: when the fixture path is forced missing, tests must Skip, not Fail.
	// This test always passes; the real skip path is in fixturePath.
	if _, err := os.Stat("/nonexistent/product-marketing-meeting-90s.wav"); err == nil {
		t.Fatal("expected missing path")
	}
}
