package render

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderTextBuildsManifest(t *testing.T) {
	writeWAV := func(string, int, []float32) error { return nil }
	synth := &fakeSynth{rate: 24000}
	opts := Options{
		Stdin: true, Out: "out.wav", Format: FormatText,
		Voice: "af_heart", Speed: 1.0, Title: "Test Doc",
	}
	// Two paragraphs that exceed a tiny cap so we get multiple segments.
	text := "Alpha one. Alpha two.\n\nBeta one. Beta two."

	result, err := RenderText(context.Background(), opts, text, synth, writeWAV)
	if err != nil {
		t.Fatalf("RenderText() error = %v", err)
	}
	m := result.Manifest

	if m.Schema != RenderSchema {
		t.Errorf("schema = %q, want %q", m.Schema, RenderSchema)
	}
	if m.Source != "stdin" || m.SourceFormat != FormatText {
		t.Errorf("source = %q/%q, want stdin/text", m.Source, m.SourceFormat)
	}
	if m.Voice != "af_heart" || m.SpeechSpeed != 1.0 || m.Title != "Test Doc" || m.SampleRate != 24000 {
		t.Errorf("manifest header = %+v, want voice/speed/title/rate populated", m)
	}
	if len(m.Segments) == 0 {
		t.Fatal("manifest has no segments")
	}
	for i, s := range m.Segments {
		if s.Index != i+1 || s.ID == "" || s.TextSHA256 == "" || s.Status != StatusComplete || s.Output != "out.wav" {
			t.Errorf("segment %d = %+v, want index/id/hash/status/output populated", i, s)
		}
	}
	complete, _, failed := m.Counts()
	if complete != len(m.Segments) || failed != 0 {
		t.Errorf("counts: complete=%d failed=%d, want all complete", complete, failed)
	}
}

func TestRenderManifestCarriesNonSensitiveTTSMetadata(t *testing.T) {
	result, err := RenderText(context.Background(), Options{
		Stdin: true, Out: "out.wav", Format: FormatText,
		TTSProvider: "qwen3-tts", TTSModel: "/models/customvoice",
		TTSWorker: "qwen3-tts-cli", TTSMode: "voicedesign", TTSVoice: "vivian",
		TTSLanguage: "English", TTSInstructionSHA256: "instruction-hash",
		TTSReferenceAudioSHA256: "audio-hash", TTSReferenceTranscriptSHA256: "text-hash",
	}, "Hello world.", &fakeSynth{rate: 24000}, func(string, int, []float32) error { return nil })
	if err != nil {
		t.Fatalf("RenderText() error = %v", err)
	}
	m := result.Manifest
	if m.TTSProvider != "qwen3-tts" || m.TTSModel != "/models/customvoice" || m.TTSWorker != "qwen3-tts-cli" ||
		m.TTSMode != "voicedesign" || m.TTSVoice != "vivian" || m.TTSLanguage != "English" {
		t.Fatalf("TTS metadata = %+v, want provider/model/worker/mode/voice/language", m)
	}
	for _, value := range []string{m.TTSInstructionSHA256, m.TTSReferenceAudioSHA256, m.TTSReferenceTranscriptSHA256} {
		if value == "" {
			t.Fatal("TTS privacy metadata contains an empty hash")
		}
	}
	encoded, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "Hello world") {
		t.Fatal("manifest must not include source text in TTS metadata")
	}
}

func TestRenderManifestTextHashIsDeterministic(t *testing.T) {
	first, second := textHash("hello"), textHash("hello")
	if first != second {
		t.Error("textHash is not deterministic")
	}
	if textHash("hello") == textHash("world") {
		t.Error("textHash collides for different inputs")
	}
}

func TestWriteManifestRoundTrips(t *testing.T) {
	m := RenderManifest{
		Schema: RenderSchema, Source: "book.epub", SourceFormat: FormatEPUB,
		Voice: "af_bella", SpeechSpeed: 0.95, SampleRate: 24000,
		Segments: []ManifestSegment{
			{Index: 1, ID: "seg-001", TextSHA256: "abc", Output: "001.wav", DurationMS: 1000, Status: StatusComplete},
			{Index: 2, ID: "seg-002", TextSHA256: "def", Output: "002.wav", DurationMS: 500, Status: StatusFailed},
		},
	}
	path := filepath.Join(t.TempDir(), "nested", "manifest.json")
	if err := WriteManifest(path, m); err != nil {
		t.Fatalf("WriteManifest() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var got RenderManifest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	if got.Schema != RenderSchema || len(got.Segments) != 2 {
		t.Fatalf("round-tripped manifest = %+v, want schema + 2 segments", got)
	}
	if c, _, f := got.Counts(); c != 1 || f != 1 {
		t.Errorf("counts after round-trip: complete=%d failed=%d, want 1/1", c, f)
	}
	if got.TotalDurationMS() != 1500 {
		t.Errorf("total duration = %d, want 1500", got.TotalDurationMS())
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read manifest dir: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp") {
			t.Fatalf("leftover manifest temp file %q", entry.Name())
		}
	}
}

func TestOptionsManifestPath(t *testing.T) {
	if p := (Options{Out: "x.wav"}).ManifestPath(); p != "x.wav.manifest.json" {
		t.Errorf("single-file default = %q, want x.wav.manifest.json", p)
	}
	if p := (Options{Out: "x.wav", Manifest: "m.json"}).ManifestPath(); p != "m.json" {
		t.Errorf("explicit --manifest = %q, want m.json", p)
	}
	if p := (Options{OutDir: "out"}).ManifestPath(); p != filepath.Join("out", "manifest.json") {
		t.Errorf("multi-file default = %q, want out/manifest.json", p)
	}
}
