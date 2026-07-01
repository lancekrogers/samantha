package config

import (
	"path/filepath"
	"strings"
	"testing"
)

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
