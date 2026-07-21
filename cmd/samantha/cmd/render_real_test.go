//go:build !integration

package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/render"
)

func chapteredManifest() render.RenderManifest {
	return render.RenderManifest{
		Schema: render.RenderSchema, Source: "book.epub", SourceFormat: render.FormatEPUB, SampleRate: 24000,
		Segments: []render.ManifestSegment{
			{Index: 1, ID: "ch1", Output: "001-one.wav", DurationMS: 10, Status: render.StatusComplete},
			{Index: 2, ID: "ch2", Output: "002-two.wav", DurationMS: 15, Status: render.StatusSkipped},
			{Index: 3, ID: "ch3", Status: render.StatusFailed},
		},
	}
}

func runFinishRender(t *testing.T, opts render.Options, renderErr error) (string, error) {
	t.Helper()
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	err := finishRender(cmd, opts, chapteredManifest(), nil, renderReport{outputKey: "output_dir", output: opts.OutDir}, renderErr, nil)
	return buf.String(), err
}

// TestFinishRenderChapteredPartialFailure locks failure-path persistence: a
// render error must still write the manifest (so the run is resumable) and be
// returned alongside the summary.
func TestFinishRenderChapteredPartialFailure(t *testing.T) {
	dir := t.TempDir()
	opts := render.Options{Input: "book.epub", OutDir: dir, Format: render.FormatEPUB, JSON: true}
	sentinel := errors.New("chapter failed")

	out, err := runFinishRender(t, opts, sentinel)
	if !errors.Is(err, sentinel) {
		t.Fatalf("finishRender() error = %v, want the render error passed through", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "manifest.json")); statErr != nil {
		t.Errorf("manifest not persisted on partial failure: %v", statErr)
	}
	var got map[string]any
	if jsonErr := json.Unmarshal([]byte(out), &got); jsonErr != nil {
		t.Fatalf("summary is not JSON: %v\n%s", jsonErr, out)
	}
	if got["failed"] != 1.0 || got["completed"] != 1.0 {
		t.Errorf("summary counts = failed %v completed %v, want 1/1", got["failed"], got["completed"])
	}
}

// TestFinishRenderChapteredJSONSummary locks the chaptered --json contract
// (key set and values) and the persisted manifest side effect: source_format
// survives and CreatedAt is stamped as RFC3339.
func TestFinishRenderChapteredJSONSummary(t *testing.T) {
	dir := t.TempDir()
	opts := render.Options{Input: "book.epub", OutDir: dir, Format: render.FormatEPUB, JSON: true}

	out, err := runFinishRender(t, opts, nil)
	if err != nil {
		t.Fatalf("finishRender() error = %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("summary is not JSON: %v\n%s", err, out)
	}
	want := map[string]any{
		"output_dir":  dir,
		"manifest":    filepath.Join(dir, "manifest.json"),
		"segments":    3.0,
		"completed":   1.0,
		"skipped":     1.0,
		"failed":      1.0,
		"encoded":     nil,
		"sample_rate": 24000.0,
		"duration_ms": 25.0,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("--json summary drifted.\n got: %#v\nwant: %#v", got, want)
	}

	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("read persisted manifest: %v", err)
	}
	var persisted render.RenderManifest
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("persisted manifest is not valid JSON: %v", err)
	}
	if persisted.SourceFormat != render.FormatEPUB || persisted.Source != "book.epub" {
		t.Errorf("persisted source = %q/%q, want book.epub/epub", persisted.Source, persisted.SourceFormat)
	}
	if _, err := time.Parse(time.RFC3339, persisted.CreatedAt); err != nil {
		t.Errorf("persisted created_at %q is not RFC3339: %v", persisted.CreatedAt, err)
	}
	if len(persisted.Segments) != 3 || persisted.Segments[0].Output != "001-one.wav" {
		t.Errorf("persisted segments = %+v, want the render manifest unchanged", persisted.Segments)
	}
}

func TestSynthIdentityIncludesEffectiveVoiceAndSpeed(t *testing.T) {
	base := synthIdentityFor(&config.Config{
		TTSProvider: "kokoro",
		TTSVoice:    "af_heart",
		SpeechSpeed: 1,
	})
	if !strings.Contains(base, "voice=af_heart") || !strings.Contains(base, "speed=1") {
		t.Fatalf("identity = %q, want effective voice and speed", base)
	}

	revoice := synthIdentityFor(&config.Config{TTSProvider: "kokoro", TTSVoice: "af_bella", SpeechSpeed: 1})
	if revoice == base {
		t.Fatal("changing the effective config voice must change the synth identity")
	}

	respeed := synthIdentityFor(&config.Config{TTSProvider: "kokoro", TTSVoice: "af_heart", SpeechSpeed: 0.95})
	if respeed == base {
		t.Fatal("changing the effective config speed must change the synth identity")
	}
}

func TestSynthIdentityIncludesQwenModelAndBinary(t *testing.T) {
	base := synthIdentityFor(&config.Config{
		TTSProvider:   "qwen3-tts",
		TTSVoice:      "af_heart",
		SpeechSpeed:   0.95,
		QwenTTSModel:  "/models/qwen-a",
		QwenTTSBinary: "/bin/qwen3-tts-cli",
	})
	if !strings.Contains(base, "model=/models/qwen-a") || !strings.Contains(base, "binary=/bin/qwen3-tts-cli") {
		t.Fatalf("identity = %q, want Qwen model and binary", base)
	}
	if strings.Contains(base, "voice=") || strings.Contains(base, "speed=") {
		t.Fatalf("identity = %q, want unused Qwen voice/speed omitted", base)
	}

	modelChanged := synthIdentityFor(&config.Config{TTSProvider: "qwen3-tts", QwenTTSModel: "/models/qwen-b", QwenTTSBinary: "/bin/qwen3-tts-cli"})
	if modelChanged == base {
		t.Fatal("changing the Qwen model must change the synth identity")
	}

	binaryChanged := synthIdentityFor(&config.Config{TTSProvider: "qwen3-tts", QwenTTSModel: "/models/qwen-a", QwenTTSBinary: "/bin/other-qwen"})
	if binaryChanged == base {
		t.Fatal("changing the Qwen binary must change the synth identity")
	}
}

func TestSynthIdentityIncludesQwenVoiceControlsAndReferenceContent(t *testing.T) {
	ref := filepath.Join(t.TempDir(), "reference.wav")
	if err := os.WriteFile(ref, []byte("reference-a"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := synthIdentityFor(&config.Config{
		TTSProvider:           "qwen3-tts",
		QwenTTSModel:          "/models/qwen-a",
		QwenTTSBinary:         "/bin/qwen3-tts-cli",
		QwenTTSMode:           "customvoice",
		QwenTTSVoice:          "vivian",
		QwenTTSLanguage:       "English",
		QwenTTSInstruction:    "calm and precise",
		QwenTTSReferenceAudio: ref,
		QwenTTSReferenceText:  "reference transcript",
		QwenTTSConsent:        true,
	})
	for _, want := range []string{
		"mode=customvoice", "voice=vivian", "language=English",
		"instruction-sha256=", "reference-audio-sha256=", "reference-text-sha256=",
	} {
		if !strings.Contains(base, want) {
			t.Errorf("identity = %q, want %q", base, want)
		}
	}

	changed := []struct {
		name string
		cfg  config.Config
	}{
		{"mode", config.Config{TTSProvider: "qwen3-tts", QwenTTSModel: "/models/qwen-a", QwenTTSBinary: "/bin/qwen3-tts-cli", QwenTTSMode: "voicedesign"}},
		{"voice", config.Config{TTSProvider: "qwen3-tts", QwenTTSModel: "/models/qwen-a", QwenTTSBinary: "/bin/qwen3-tts-cli", QwenTTSVoice: "serena"}},
		{"language", config.Config{TTSProvider: "qwen3-tts", QwenTTSModel: "/models/qwen-a", QwenTTSBinary: "/bin/qwen3-tts-cli", QwenTTSLanguage: "Chinese"}},
		{"instruction", config.Config{TTSProvider: "qwen3-tts", QwenTTSModel: "/models/qwen-a", QwenTTSBinary: "/bin/qwen3-tts-cli", QwenTTSInstruction: "bright and warm"}},
		{"reference text", config.Config{TTSProvider: "qwen3-tts", QwenTTSModel: "/models/qwen-a", QwenTTSBinary: "/bin/qwen3-tts-cli", QwenTTSReferenceText: "different transcript"}},
		{"consent", config.Config{TTSProvider: "qwen3-tts", QwenTTSModel: "/models/qwen-a", QwenTTSBinary: "/bin/qwen3-tts-cli", QwenTTSConsent: true}},
	}
	for _, tc := range changed {
		if got := synthIdentityFor(&tc.cfg); got == base {
			t.Errorf("changing Qwen %s must change identity", tc.name)
		}
	}

	if err := os.WriteFile(ref, []byte("reference-b"), 0o600); err != nil {
		t.Fatal(err)
	}
	refConfig := &config.Config{
		TTSProvider: "qwen3-tts", QwenTTSModel: "/models/qwen-a", QwenTTSBinary: "/bin/qwen3-tts-cli", QwenTTSReferenceAudio: ref,
	}
	beforeRef := synthIdentityFor(refConfig)
	changedRef := synthIdentityFor(refConfig)
	if changedRef != beforeRef {
		t.Fatal("unchanged reference audio content changed identity")
	}
	if err := os.WriteFile(ref, []byte("reference-c"), 0o600); err != nil {
		t.Fatal(err)
	}
	changedRef = synthIdentityFor(refConfig)
	if changedRef == beforeRef {
		t.Fatal("changing reference audio content must change identity")
	}
}

func TestPopulateTTSMetadataUsesHashesForQwenReferences(t *testing.T) {
	ref := filepath.Join(t.TempDir(), "voice.wav")
	if err := os.WriteFile(ref, []byte("private audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		TTSProvider: "qwen3-tts", QwenTTSModel: "/models/customvoice", QwenTTSBinary: "qwen3-tts-cli",
		QwenTTSMode: "approved_clone", QwenTTSVoice: "speaker-a", QwenTTSLanguage: "English",
		QwenTTSInstruction: "private instruction", QwenTTSReferenceAudio: ref, QwenTTSReferenceText: "private transcript",
	}
	var opts render.Options
	populateTTSMetadata(&opts, cfg)
	if opts.TTSProvider != "qwen3-tts" || opts.TTSMode != "approved_clone" || opts.TTSVoice != "speaker-a" {
		t.Fatalf("metadata = %+v, want resolved Qwen fields", opts)
	}
	if opts.TTSInstructionSHA256 == "private instruction" || opts.TTSReferenceTranscriptSHA256 == "private transcript" || opts.TTSReferenceAudioSHA256 == "private audio" {
		t.Fatalf("metadata leaked private reference content: %+v", opts)
	}
}

// TestApplyVoiceOverridesRecordsEffectiveValues guards manifest auditability:
// a config-driven render (no CLI flags) must still end up with the effective
// voice/speed in opts, which is what manifests and resume keys record.
func TestApplyVoiceOverridesRecordsEffectiveValues(t *testing.T) {
	cfg := &config.Config{TTSVoice: "af_heart", SpeechSpeed: 1.1}
	opts := render.Options{}
	applyVoiceOverrides(cfg, &opts)
	if opts.Voice != "af_heart" || opts.Speed != 1.1 {
		t.Fatalf("opts = %q/%v, want config-derived af_heart/1.1", opts.Voice, opts.Speed)
	}

	cfg = &config.Config{TTSVoice: "af_heart", SpeechSpeed: 1.1}
	opts = render.Options{Voice: "bm_fable", Speed: 0.9}
	applyVoiceOverrides(cfg, &opts)
	if cfg.TTSVoice != "bm_fable" || cfg.SpeechSpeed != 0.9 {
		t.Fatalf("cfg = %q/%v, want CLI overrides applied", cfg.TTSVoice, cfg.SpeechSpeed)
	}
	if opts.Voice != "bm_fable" || opts.Speed != 0.9 {
		t.Fatalf("opts = %q/%v, want CLI values", opts.Voice, opts.Speed)
	}
}
