package config

import "testing"

// TestManifestForDefaultResolvesExpectedAssets pins the default interactive
// asset set (VAD + sherpa offline whisper + Kokoro TTS) to the exact files the
// pre-manifest download path produced, so the manifest migration cannot drift
// the URLs, names, or check files.
func TestManifestForDefaultResolvesExpectedAssets(t *testing.T) {
	cfg := &Config{
		STTProvider:  "sherpa",
		WhisperModel: "base.en",
		TTSProvider:  "kokoro",
		VADEnabled:   true,
	}
	req := AssetRequest{NeedSTT: true, NeedTTS: true, NeedVAD: true}

	m, err := ManifestFor(cfg, req)
	if err != nil {
		t.Fatalf("ManifestFor() error = %v", err)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("default manifest failed validation: %v", err)
	}

	files := m.ModelFiles()
	if len(files) != 1 {
		t.Fatalf("ModelFiles() = %d, want 1 (silero VAD)", len(files))
	}
	if files[0].Name != "silero_vad.onnx" ||
		files[0].URL != "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/silero_vad.onnx" {
		t.Fatalf("VAD file = %+v, want silero_vad.onnx with the asr-models URL", files[0])
	}

	archives := m.ModelArchives("/models")
	if len(archives) != 2 {
		t.Fatalf("ModelArchives() = %d, want 2 (whisper offline, kokoro)", len(archives))
	}
	// Order is whisper STT then Kokoro TTS — matching the legacy download order.
	if archives[0].Name != "whisper-base.en" ||
		archives[0].URL != "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-whisper-base.en.tar.bz2" {
		t.Fatalf("offline archive = %+v, want whisper-base.en", archives[0])
	}
	if archives[0].TargetDir != "" {
		t.Errorf("offline archive TargetDir = %q, want empty (extracts to models root)", archives[0].TargetDir)
	}
	wantCheck := []string{"base.en-encoder.onnx", "base.en-decoder.onnx"}
	if !equalStrings(archives[0].CheckFiles, wantCheck) {
		t.Errorf("offline check files = %v, want %v", archives[0].CheckFiles, wantCheck)
	}
	if archives[1].Name != "kokoro-tts" ||
		archives[1].URL != "https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/kokoro-multi-lang-v1_0.tar.bz2" {
		t.Fatalf("kokoro archive = %+v, want kokoro-tts", archives[1])
	}
}

// TestManifestForEachProviderPath checks every recognized STT provider/mode
// resolves to the expected asset shape and provider.
func TestManifestForEachProviderPath(t *testing.T) {
	cases := []struct {
		name        string
		provider    string
		wantProv    string
		wantMode    string
		wantArchive bool
	}{
		{"empty default", "", "sherpa", "offline", true},
		{"sherpa", "sherpa", "sherpa", "offline", true},
		{"sherpa-offline", "sherpa-offline", "sherpa", "offline", true},
		{"sherpa-streaming", "sherpa-streaming", "sherpa", "streaming", true},
		{"whispercpp", "whispercpp", "whispercpp", "cli", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{STTProvider: tc.provider, WhisperModel: "base.en"}
			m, err := ManifestFor(cfg, AssetRequest{NeedSTT: true})
			if err != nil {
				t.Fatalf("ManifestFor(%q) error = %v", tc.provider, err)
			}
			if len(m.Assets) != 1 {
				t.Fatalf("assets = %d, want 1 STT asset", len(m.Assets))
			}
			a := m.Assets[0]
			if a.Provider != tc.wantProv || string(a.Mode) != tc.wantMode {
				t.Errorf("provider/mode = %s/%s, want %s/%s", a.Provider, a.Mode, tc.wantProv, tc.wantMode)
			}
			if a.Kind != AssetKindSTT {
				t.Errorf("kind = %q, want stt", a.Kind)
			}
			if a.IsArchive() != tc.wantArchive {
				t.Errorf("IsArchive() = %v, want %v", a.IsArchive(), tc.wantArchive)
			}
			if err := a.Validate(); err != nil {
				t.Errorf("resolved asset failed validation: %v", err)
			}
		})
	}
}

// TestManifestForUnsupportedProviderSkipsSTT preserves the historical no-op: an
// unrecognized provider yields no STT asset (and no error) rather than failing.
func TestManifestForUnsupportedProviderSkipsSTT(t *testing.T) {
	cfg := &Config{STTProvider: "bogus"}
	m, err := ManifestFor(cfg, AssetRequest{NeedSTT: true})
	if err != nil {
		t.Fatalf("ManifestFor(unsupported) error = %v, want nil (silent skip)", err)
	}
	if len(m.Assets) != 0 {
		t.Fatalf("assets = %d, want 0 for an unsupported provider", len(m.Assets))
	}
}

func TestModelArchivesPropagatesChecksum(t *testing.T) {
	m := AssetManifest{Schema: AssetSchema, Assets: []Asset{{
		ID: "tts.kokoro", Provider: "kokoro", Kind: AssetKindTTS, Name: "kokoro-tts",
		Archive:    &AssetArchive{URL: "https://example/archive.tar.bz2", SHA256: "abc123", StripPrefix: true},
		CheckFiles: []string{"model.onnx"},
	}}}

	archives := m.ModelArchives("/models")
	if len(archives) != 1 {
		t.Fatalf("ModelArchives() = %d, want 1", len(archives))
	}
	if archives[0].SHA256 != "abc123" {
		t.Fatalf("archive SHA256 = %q, want abc123", archives[0].SHA256)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
