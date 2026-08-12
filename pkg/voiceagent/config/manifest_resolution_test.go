package config

import (
	"strings"
	"testing"
)

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
// resolves to the expected asset shape and provider, including the preferred
// stt_provider + stt_mode schema.
func TestManifestForEachProviderPath(t *testing.T) {
	cases := []struct {
		name        string
		provider    string
		mode        string
		wantProv    string
		wantMode    string
		wantArchive bool
	}{
		{"empty default", "", "", "sherpa", "offline", true},
		{"sherpa", "sherpa", "", "sherpa", "offline", true},
		{"sherpa-offline", "sherpa-offline", "", "sherpa", "offline", true},
		{"sherpa-streaming", "sherpa-streaming", "", "sherpa", "streaming", true},
		{"whispercpp", "whispercpp", "", "whispercpp", "cli", false},
		{"sherpa with offline mode", "sherpa", "offline", "sherpa", "offline", true},
		{"sherpa with streaming mode", "sherpa", "streaming", "sherpa", "streaming", true},
		{"whispercpp with cli mode", "whispercpp", "cli", "whispercpp", "cli", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{STTProvider: tc.provider, STTMode: tc.mode, WhisperModel: "base.en"}
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

// TestManifestForConflictingSTTModeErrors proves an invalid provider/mode
// combination surfaces where assets are resolved instead of silently picking
// the alias path.
func TestManifestForConflictingSTTModeErrors(t *testing.T) {
	tests := []struct {
		name            string
		provider        string
		mode            string
		wantErrContains string
	}{
		{name: "compound alias with conflicting mode", provider: "sherpa-streaming", mode: "offline", wantErrContains: "conflicts with stt_mode"},
		{name: "mode unsupported by provider", provider: "whispercpp", mode: "streaming", wantErrContains: "not supported by stt_provider"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{STTProvider: tt.provider, STTMode: tt.mode, WhisperModel: "base.en"}
			_, err := ManifestFor(cfg, AssetRequest{NeedSTT: true})
			if err == nil {
				t.Fatalf("ManifestFor(%q, %q) error = nil, want stt_mode error", tt.provider, tt.mode)
			}
			if !strings.Contains(err.Error(), tt.wantErrContains) {
				t.Fatalf("ManifestFor(%q, %q) error = %q, want containing %q", tt.provider, tt.mode, err, tt.wantErrContains)
			}
		})
	}
}

// TestManifestForUnsupportedProviderSkipsSTT preserves the historical no-op: an
// unrecognized provider yields no STT asset (and no error) rather than failing,
// with or without stt_mode set.
func TestManifestForUnsupportedProviderSkipsSTT(t *testing.T) {
	for _, cfg := range []*Config{
		{STTProvider: "bogus"},
		{STTProvider: "bogus", STTMode: "streaming"},
	} {
		m, err := ManifestFor(cfg, AssetRequest{NeedSTT: true})
		if err != nil {
			t.Fatalf("ManifestFor(unsupported) error = %v, want nil (silent skip)", err)
		}
		if len(m.Assets) != 0 {
			t.Fatalf("assets = %d, want 0 for an unsupported provider", len(m.Assets))
		}
	}
}

func TestModelArchivesPropagatesChecksum(t *testing.T) {
	m := AssetManifest{Schema: AssetSchema, Assets: []Asset{{
		ID: "tts.kokoro", Provider: "kokoro", Kind: AssetKindTTS, Name: "kokoro-tts",
		Archive:    &AssetArchive{URL: "https://example/archive.tar.bz2", SHA256: "abc123"},
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

// TestManifestForShipsChecksumsForBuiltInAssets guards the download hardening:
// every built-in asset must carry a pinned SHA256, or checksum enforcement
// silently degrades to HTTP-200 trust for exactly the defaults users install.
func TestManifestForShipsChecksumsForBuiltInAssets(t *testing.T) {
	cfg := &Config{STTProvider: "sherpa", TTSProvider: "kokoro", VADEnabled: true}
	m, err := ManifestFor(cfg, AssetRequest{NeedSTT: true, NeedTTS: true, NeedVAD: true})
	if err != nil {
		t.Fatalf("ManifestFor() error = %v", err)
	}
	for _, f := range m.ModelFiles() {
		if f.SHA256 == "" || f.Size == 0 {
			t.Errorf("file %s missing pinned SHA256/size", f.Name)
		}
	}
	for _, a := range m.ModelArchives("") {
		if a.SHA256 == "" {
			t.Errorf("archive %s missing pinned SHA256", a.Name)
		}
	}

	for _, model := range sherpaWhisperKnownModels {
		if sherpaWhisperArchiveSHA256[model] == "" {
			t.Errorf("known whisper model %s missing pinned SHA256", model)
		}
	}
	for _, name := range []string{"base.en", "small.en"} {
		a, err := WhisperCPPModelAsset(name)
		if err != nil {
			t.Fatalf("WhisperCPPModelAsset(%s) error = %v", name, err)
		}
		if a.SHA256 == "" || a.Size == 0 {
			t.Errorf("whisper.cpp model %s missing pinned SHA256/size", name)
		}
	}
	s, err := SherpaStreamingModel("")
	if err != nil {
		t.Fatalf("SherpaStreamingModel() error = %v", err)
	}
	if s.ArchiveSHA256 == "" {
		t.Error("streaming zipformer missing pinned SHA256")
	}
}
