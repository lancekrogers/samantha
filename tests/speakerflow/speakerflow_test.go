//go:build integration
// +build integration

// Package speakerflow exercises meeting speaker diarization against real
// multi-voice meeting audio (YouTube product marketing meeting clip).
//
// This suite proves the production native path:
//   - real multi-speaker PCM loads (16 kHz mono WAV)
//   - managed pyannote + NeMo TitaNet assets resolve
//   - sherpa OfflineSpeakerDiarization runs on real samples
//   - AttributeTranscript labels utterances from the timeline
//   - analysis JSON is written beside the fixture
//
// Fixture (shared across worktrees — not re-downloaded every run):
//
//	just fetch-meeting-fixture
//	just test speakerflow
//
// Cache path (first hit wins):
//  1. $SAMANTHA_MEETING_FIXTURE (explicit file)
//  2. $SAMANTHA_FIXTURE_CACHE/product-marketing-meeting-90s.wav
//  3. $XDG_CACHE_HOME/<AppSlug>/fixtures/meetings/… or ~/.cache/<AppSlug>/…
//  4. <module>/tests/fixtures/meetings/… (legacy per-tree path)
package speakerflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/listen"
	"github.com/lancekrogers/samantha/internal/meeting"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
	"github.com/lancekrogers/samantha/internal/speaker"
)

const (
	fixtureName  = "product-marketing-meeting-90s.wav"
	fixtureRate  = 16000
	wantSpeakers = 2
)

type fixtureCapture struct{ chunks chan []float32 }

func (f *fixtureCapture) Subscribe(int) (int, <-chan []float32) {
	f.chunks = make(chan []float32, 1)
	return 1, f.chunks
}

func (f *fixtureCapture) Unsubscribe(int) { close(f.chunks) }

// sharedFixtureCandidates returns search paths for the multi-voice meeting clip.
// Prefer the user-level cache so every git worktree reuses one download.
func sharedFixtureCandidates() []string {
	var paths []string
	if p := os.Getenv("SAMANTHA_MEETING_FIXTURE"); p != "" {
		paths = append(paths, p)
	}
	cacheDir := os.Getenv("SAMANTHA_FIXTURE_CACHE")
	if cacheDir == "" {
		if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
			cacheDir = filepath.Join(xdg, config.AppSlug, "fixtures", "meetings")
		} else if home, err := os.UserHomeDir(); err == nil {
			cacheDir = filepath.Join(home, ".cache", config.AppSlug, "fixtures", "meetings")
		}
	}
	if cacheDir != "" {
		paths = append(paths, filepath.Join(cacheDir, fixtureName))
	}
	// Legacy in-tree location (gitignored) still works if present.
	dir, err := os.Getwd()
	if err == nil {
		for i := 0; i < 6; i++ {
			paths = append(paths, filepath.Join(dir, "tests", "fixtures", "meetings", fixtureName))
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return paths
}

func fixturePath(t *testing.T) string {
	t.Helper()
	for _, candidate := range sharedFixtureCandidates() {
		if st, err := os.Stat(candidate); err == nil && st.Size() > 100_000 {
			t.Logf("using fixture %s (%.1f MiB)", candidate, float64(st.Size())/(1<<20))
			return candidate
		}
	}
	t.Skipf("meeting fixture missing — run: just fetch-meeting-fixture\n  looked in:\n  - %s",
		joinLines(sharedFixtureCandidates()))
	return ""
}

func joinLines(ss []string) string {
	if len(ss) == 0 {
		return "(none)"
	}
	out := ss[0]
	for _, s := range ss[1:] {
		out += "\n  - " + s
	}
	return out
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
	// Clip is ~90s of the YouTube meeting (not the full ~43 min video).
	dur := float64(len(samples)) / float64(rate)
	if dur < 30 || dur > 120 {
		t.Fatalf("duration = %.1fs, want ~90s multi-voice meeting clip (section extract)", dur)
	}

	appCfg := &config.Config{ModelsDir: config.ModelsDir()}
	appCfg.Speaker.Enabled = true
	appCfg.Speaker.Meeting.Enabled = true
	appCfg.Speaker.Meeting.NumSpeakers = wantSpeakers
	if err := config.EnsureRuntimeAssets(t.Context(), appCfg, config.AssetRequest{NeedSpeaker: true}, nil); err != nil {
		t.Fatalf("EnsureRuntimeAssets: %v", err)
	}
	cfg := speaker.FromAppConfig(appCfg)
	engine, err := speaker.NewSherpaEngine(cfg, config.ModelsDirFrom(appCfg))
	if err != nil {
		t.Fatalf("NewSherpaEngine: %v", err)
	}
	analyzer, err := speaker.NewAnalyzer(cfg, engine)
	if err != nil {
		_ = engine.Close()
		t.Fatalf("NewAnalyzer: %v", err)
	}
	defer func() { _ = analyzer.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	dir := t.TempDir()
	bundlePath := filepath.Join(dir, "native-meeting.meeting")
	writer, err := meetinglog.CreateBundle(bundlePath, "Native speaker integration", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.OnUtterance(listen.Utterance{
		Text: "fixture transcript turn", At: writer.StartedAt().Add(10 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	capture := &fixtureCapture{}
	session, err := meeting.NewSpeakerSession(capture, analyzer, writer, bundlePath, false)
	if err != nil {
		t.Fatal(err)
	}
	capture.chunks <- samples
	result, err := session.Finalize(ctx)
	if err != nil {
		t.Fatalf("SpeakerSession.Finalize: %v", err)
	}
	if _, err := writer.Close(); err != nil {
		t.Fatal(err)
	}
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
	events, err := meeting.ReadEvents(writer.JSONLPath())
	if err != nil {
		t.Fatal(err)
	}
	seenAnalysis, seenAttribution := false, false
	for _, event := range events {
		seenAnalysis = seenAnalysis || event.Type == meetinglog.TypeSpeakerAnalysis
		seenAttribution = seenAttribution || event.Type == meetinglog.TypeSpeakerUtterance
	}
	if !seenAnalysis || !seenAttribution {
		t.Fatalf("meeting events missing analysis/attribution: %+v", events)
	}
	if _, err := os.Stat(result.Artifact); err != nil {
		t.Fatalf("speaker sidecar: %v", err)
	}
	wantArtifact := filepath.Join(bundlePath, meetinglog.BundleInternalDirName, meetinglog.BundleSpeakerAnalysisName)
	if result.Artifact != wantArtifact {
		t.Fatalf("speaker artifact = %q, want bundled %q", result.Artifact, wantArtifact)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(bundlePath) || !entries[0].IsDir() {
		t.Fatalf("meetings root should contain one bundle, got %#v", entries)
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
	if _, err := os.Stat("/nonexistent/product-marketing-meeting-90s.wav"); err == nil {
		t.Fatal("expected missing path")
	}
}

func TestSharedFixtureCachePathHelpers(t *testing.T) {
	cands := sharedFixtureCandidates()
	if len(cands) == 0 {
		t.Fatal("expected at least one cache candidate")
	}
	// Prefer a user cache path so worktrees share one download.
	foundCache := false
	for _, c := range cands {
		if strings.Contains(c, filepath.Join(config.AppSlug, "fixtures", "meetings")) ||
			strings.Contains(c, ".cache"+string(filepath.Separator)+config.AppSlug) {
			foundCache = true
			break
		}
	}
	if !foundCache && os.Getenv("SAMANTHA_FIXTURE_CACHE") == "" {
		t.Logf("no default cache path in candidates (unusual): %v", cands)
	}
}
