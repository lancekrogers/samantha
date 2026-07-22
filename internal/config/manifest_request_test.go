package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestScopedAssetRequestNarrowsDefault proves scoping only masks kinds out of
// the default request — it never requests an asset the config does not need.
func TestScopedAssetRequestNarrowsDefault(t *testing.T) {
	full := &Config{STTProvider: "sherpa", TTSProvider: "kokoro", VADEnabled: true}
	bare := &Config{STTProvider: "none", TTSProvider: "none", VADEnabled: false}
	cases := []struct {
		name  string
		cfg   *Config
		scope AssetScope
		want  AssetRequest
	}{
		{"full scope matches default", full, AssetScope{STT: true, TTS: true, VAD: true}, DefaultAssetRequest(full)},
		{"tts scope drops stt and vad", full, AssetScope{TTS: true}, AssetRequest{NeedTTS: true}},
		{"stt and vad scope drops tts", full, AssetScope{STT: true, VAD: true}, AssetRequest{NeedSTT: true, NeedVAD: true}},
		{"scope cannot add unneeded assets", bare, AssetScope{STT: true, TTS: true, VAD: true}, AssetRequest{}},
		{"empty scope requests nothing", full, AssetScope{}, AssetRequest{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ScopedAssetRequest(tc.cfg, tc.scope); got != tc.want {
				t.Errorf("ScopedAssetRequest() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestManifestForBatchRenderingResolvesTTSOnly proves batch rendering pulls only
// the TTS asset — no STT or VAD providers are initialized.
func TestManifestForBatchRenderingResolvesTTSOnly(t *testing.T) {
	cfg := &Config{STTProvider: "sherpa", TTSProvider: "kokoro", VADEnabled: true}
	m, err := ManifestFor(cfg, AssetRequest{NeedTTS: true})
	if err != nil {
		t.Fatalf("ManifestFor(TTS-only) error = %v", err)
	}
	if len(m.Assets) != 1 {
		t.Fatalf("batch assets = %d, want 1 (Kokoro TTS only)", len(m.Assets))
	}
	if m.Assets[0].Kind != AssetKindTTS || m.Assets[0].Provider != "kokoro" {
		t.Fatalf("batch asset = %+v, want kokoro/tts", m.Assets[0])
	}
	for _, a := range m.Assets {
		if a.Kind == AssetKindSTT || a.Kind == AssetKindVAD {
			t.Fatalf("batch rendering resolved a %s asset, want TTS only", a.Kind)
		}
	}
}

// TestManifestForInteractiveStartupResolvesSTTAndVAD covers the interactive
// startup request shape: STT + VAD, no TTS.
func TestManifestForInteractiveStartupResolvesSTTAndVAD(t *testing.T) {
	cfg := &Config{STTProvider: "sherpa", WhisperModel: "base.en", VADEnabled: true}
	m, err := ManifestFor(cfg, AssetRequest{NeedSTT: true, NeedVAD: true})
	if err != nil {
		t.Fatalf("ManifestFor(STT+VAD) error = %v", err)
	}

	kinds := map[AssetKind]int{}
	for _, a := range m.Assets {
		kinds[a.Kind]++
	}
	if kinds[AssetKindVAD] != 1 || kinds[AssetKindSTT] != 1 || kinds[AssetKindTTS] != 0 {
		t.Fatalf("interactive kinds = %v, want 1 vad / 1 stt / 0 tts", kinds)
	}
	// VAD comes before STT so it downloads first, as the legacy path did.
	if m.Assets[0].Kind != AssetKindVAD {
		t.Errorf("first asset kind = %q, want vad (download order)", m.Assets[0].Kind)
	}
}

func TestManifestForMeetingSpeakerModels(t *testing.T) {
	cfg := &Config{}
	cfg.Speaker.Enabled = true
	cfg.Speaker.Meeting.Enabled = true
	m, err := ManifestFor(cfg, AssetRequest{NeedSpeaker: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Assets) != 2 {
		t.Fatalf("speaker assets = %d, want segmentation + embedding", len(m.Assets))
	}
	for _, asset := range m.Assets {
		if asset.Kind != AssetKindSpeaker || asset.Provider != "sherpa" {
			t.Fatalf("speaker asset = %+v", asset)
		}
	}
	files := m.ModelFiles()
	if len(files) != 1 || files[0].Name != "speaker/nemo_en_titanet_small.onnx" || files[0].SHA256 != speakerEmbeddingSHA256 || files[0].Size != speakerEmbeddingSize {
		t.Fatalf("embedding files = %+v", files)
	}
	archives := m.ModelArchives("/models")
	if len(archives) != 1 || archives[0].TargetDir != filepath.Join("/models", "speaker", "pyannote-segmentation-3.0") || archives[0].SHA256 != speakerSegmentationSHA256 {
		t.Fatalf("segmentation archives = %+v", archives)
	}
}

func TestManifestForMeetingSpeakerModelsHonorsOverridesAndDisabledGate(t *testing.T) {
	cfg := &Config{}
	if m, err := ManifestFor(cfg, AssetRequest{NeedSpeaker: true}); err != nil || len(m.Assets) != 0 {
		t.Fatalf("disabled speaker manifest = %+v, %v", m, err)
	}

	cfg.Speaker.Enabled = true
	cfg.Speaker.Meeting.Enabled = true
	cfg.Speaker.Models.Embedding = "/custom/embedding.onnx"
	cfg.Speaker.Models.Segmentation = "custom/segmentation.onnx"
	m, err := ManifestFor(cfg, AssetRequest{NeedSpeaker: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Assets) != 0 {
		t.Fatalf("custom model paths should not request managed assets: %+v", m.Assets)
	}
}

func TestManifestForWhisperCPPNestedFilePath(t *testing.T) {
	cases := map[string]string{
		"":         "whispercpp/ggml-base.en.bin",
		"base.en":  "whispercpp/ggml-base.en.bin",
		"small.en": "whispercpp/ggml-small.en.bin",
	}
	for model, wantPath := range cases {
		cfg := &Config{STTProvider: "whispercpp", WhisperCPPModel: model}
		m, err := ManifestFor(cfg, AssetRequest{NeedSTT: true})
		if err != nil {
			t.Fatalf("ManifestFor(whispercpp %q) error = %v", model, err)
		}
		files := m.ModelFiles()
		if len(files) != 1 {
			t.Fatalf("whispercpp %q files = %d, want 1", model, len(files))
		}
		if files[0].Name != wantPath {
			t.Errorf("whispercpp %q path = %q, want %q (nested under whispercpp/)", model, files[0].Name, wantPath)
		}
	}
}

func TestManifestForUnsupportedModelsError(t *testing.T) {
	streaming := &Config{STTProvider: "sherpa-streaming", SherpaStreamingModel: "bogus"}
	if _, err := ManifestFor(streaming, AssetRequest{NeedSTT: true}); err == nil ||
		!strings.Contains(err.Error(), "sherpa streaming") || !strings.Contains(err.Error(), "bogus") {
		t.Errorf("streaming unsupported error = %v, want it to name the provider and model", err)
	}

	whispercpp := &Config{STTProvider: "whispercpp", WhisperCPPModel: "bogus"}
	if _, err := ManifestFor(whispercpp, AssetRequest{NeedSTT: true}); err == nil ||
		!strings.Contains(err.Error(), "whisper.cpp") || !strings.Contains(err.Error(), "bogus") {
		t.Errorf("whisper.cpp unsupported error = %v, want it to name the provider and model", err)
	}

	offline := &Config{STTProvider: "sherpa", WhisperModel: "../evil"}
	if _, err := ManifestFor(offline, AssetRequest{NeedSTT: true}); err == nil ||
		!strings.Contains(err.Error(), "sherpa offline whisper") || !strings.Contains(err.Error(), "../evil") {
		t.Errorf("sherpa offline unsupported error = %v, want it to name the provider and model", err)
	}
}

// TestManifestResolvedPathsUnderModelsDir proves every resolved download target
// stays under the configured models dir (no escape via absolute or ../ paths).
func TestManifestResolvedPathsUnderModelsDir(t *testing.T) {
	const base = "/models"
	cfg := &Config{
		STTProvider: "sherpa-streaming", // nested TargetDir
		TTSProvider: "kokoro",
		VADEnabled:  true,
	}
	m, err := ManifestFor(cfg, AssetRequest{NeedSTT: true, NeedTTS: true, NeedVAD: true})
	if err != nil {
		t.Fatalf("ManifestFor() error = %v", err)
	}

	for _, f := range m.ModelFiles() {
		assertUnderDir(t, base, filepath.Join(base, f.Name))
	}
	for _, a := range m.ModelArchives(base) {
		target := a.TargetDir
		if target == "" {
			target = base // legacy: empty TargetDir extracts into the models root
		}
		assertUnderDir(t, base, target)
	}
}

func assertUnderDir(t *testing.T, base, path string) {
	t.Helper()
	rel, err := filepath.Rel(filepath.Clean(base), filepath.Clean(path))
	if err != nil {
		t.Errorf("path %q not relative to %q: %v", path, base, err)
		return
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Errorf("path %q escapes models dir %q (rel %q)", path, base, rel)
	}
}
