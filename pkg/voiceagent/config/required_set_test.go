package config

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// liveCfg is a realistic installed config: sherpa offline STT, kokoro TTS,
// VAD on, speaker recognition on — the shape of the machine that lost its
// models on 2026-08-17.
func liveCfg() *Config {
	cfg := &Config{
		TTSProvider:          "kokoro",
		STTProvider:          "sherpa",
		STTMode:              STTModeOffline,
		SherpaStreamingModel: "en-2023-06-26",
		WhisperModel:         "small",
		WhisperCPPModel:      "base.en",
		VADEnabled:           true,
	}
	cfg.Speaker.Enabled = true
	cfg.Speaker.Live.Enabled = true
	return cfg
}

// qwenPersona is a persona pinned to the native Qwen3-TTS provider at tier.
func qwenPersona(id, tier string) PersonaAssets {
	return PersonaAssets{ID: id, Cfg: &Config{
		TTSProvider:      "qwen3-tts",
		QwenTTSModelTier: tier,
		STTProvider:      "none",
	}}
}

func TestRequiredAssetPathsFailsClosed(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	cases := []struct {
		name     string
		ctx      context.Context
		cfg      *Config
		personas []PersonaAssets
		wantErr  string
	}{
		{name: "cancelled context", ctx: cancelled, cfg: liveCfg(), wantErr: "context canceled"},
		{name: "nil config", ctx: context.Background(), cfg: nil, wantErr: "nil config"},
		{
			name: "unsupported sherpa_streaming_model names the key",
			ctx:  context.Background(),
			cfg: func() *Config {
				cfg := liveCfg()
				cfg.SherpaStreamingModel = "de-2099"
				return cfg
			}(),
			wantErr: "config sherpa_streaming_model",
		},
		{
			name:     "persona without an effective config",
			ctx:      context.Background(),
			cfg:      liveCfg(),
			personas: []PersonaAssets{{ID: "veronica"}},
			wantErr:  `persona "veronica": no effective config`,
		},
		{
			name:     "persona whose manifest cannot be built",
			ctx:      context.Background(),
			cfg:      liveCfg(),
			personas: []PersonaAssets{{ID: "veronica", Cfg: &Config{STTProvider: "sherpa", STTMode: "sideways"}}},
			wantErr:  `persona "veronica" manifest`,
		},
		{
			name: "unsupported global model",
			ctx:  context.Background(),
			cfg: func() *Config {
				cfg := liveCfg()
				cfg.STTMode = "sideways"
				return cfg
			}(),
			wantErr: "global manifest",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			touchFile(t, filepath.Join(dir, "stale.bin"))

			set, err := RequiredAssetPaths(tc.ctx, tc.cfg, dir, tc.personas)
			if err == nil {
				t.Fatalf("RequiredAssetPaths() error = nil, want %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("RequiredAssetPaths() error = %v, want it to mention %q", err, tc.wantErr)
			}
			if len(set.Protected) != 0 {
				t.Errorf("a failed resolve must return no partial set, got %d protected paths", len(set.Protected))
			}
		})
	}
}

func TestRequiredAssetPathsProtectsReferencedAssets(t *testing.T) {
	qwenRoot := filepath.Join("qwen3-tts")
	zipformer := "sherpa-onnx-streaming-zipformer-en-2023-06-26"

	cases := []struct {
		name string
		cfg  func() *Config
		// personas are the profiles' effective configs.
		personas []PersonaAssets
		// install lays down files (a trailing separator makes a directory).
		install []string
		// keptReasons maps a models-dir-relative path that must never be a
		// candidate to a substring its protected reason must carry.
		keptReasons map[string]string
		// wantCandidates are the models-dir-relative paths that must be listed.
		wantCandidates []string
	}{
		{
			name:     "persona pins qwen while the global provider is kokoro",
			cfg:      liveCfg,
			personas: []PersonaAssets{qwenPersona("veronica", "0.6b")},
			install: []string{
				filepath.Join(qwenRoot, "bin", "qwen3-tts-worker"),
				filepath.Join(qwenRoot, "models", "qwen3-tts-0.6b-f16.gguf"),
				"stale.bin",
			},
			keptReasons:    map[string]string{qwenRoot: "persona veronica: qwen3-tts tier 0.6b"},
			wantCandidates: []string{"stale.bin"},
		},
		{
			name:     "every tier in use is named",
			cfg:      liveCfg,
			personas: []PersonaAssets{qwenPersona("veronica", "0.6b"), qwenPersona("ada", "1.7b")},
			install: []string{
				filepath.Join(qwenRoot, "models", "qwen3-tts-1.7b-f16.gguf"),
			},
			keptReasons: map[string]string{qwenRoot: "persona ada: qwen3-tts tier 1.7b"},
		},
		{
			name: "configured streaming model survives offline mode",
			cfg:  liveCfg,
			install: []string{
				filepath.Join(zipformer, "encoder-epoch-99-avg-1-chunk-16-left-128.onnx"),
				filepath.Join(zipformer, "tokens.txt"),
				"stale.bin",
			},
			keptReasons:    map[string]string{zipformer: "config sherpa_streaming_model"},
			wantCandidates: []string{"stale.bin"},
		},
		{
			name: "kokoro fallback pack survives a qwen global provider",
			cfg: func() *Config {
				cfg := liveCfg()
				cfg.TTSProvider = "qwen3-tts"
				cfg.TTSFallbackProvider = "kokoro"
				return cfg
			},
			install: []string{
				filepath.Join(KokoroV1Subdir, "model.onnx"),
				"stale.bin",
			},
			keptReasons:    map[string]string{KokoroV1Subdir: "config voice_fallback_provider kokoro"},
			wantCandidates: []string{"stale.bin"},
		},
		{
			name: "junk is still a candidate",
			cfg:  liveCfg,
			install: []string{
				filepath.Join(".extract-1234", "leftover.onnx"),
				".archive-99.tar.bz2.part",
				filepath.Join(qwenRoot, "bin", "qwen3-tts-worker"),
			},
			personas:       []PersonaAssets{qwenPersona("veronica", "0.6b")},
			wantCandidates: []string{".archive-99.tar.bz2.part", ".extract-1234"},
		},
		{
			name: "speaker models are kept when no mode currently loads them",
			cfg: func() *Config {
				// speaker.enabled with both modes off: the global manifest
				// requests no speaker asset, the config key still names one.
				cfg := liveCfg()
				cfg.Speaker.Live.Enabled = false
				return cfg
			},
			install: []string{
				filepath.Join("speaker", "nemo_en_titanet_small.onnx"),
				"stale.bin",
			},
			keptReasons:    map[string]string{filepath.Join("speaker", "nemo_en_titanet_small.onnx"): "config speaker.enabled"},
			wantCandidates: []string{"stale.bin"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, rel := range tc.install {
				touchFile(t, filepath.Join(dir, rel))
			}

			set, err := RequiredAssetPaths(context.Background(), tc.cfg(), dir, tc.personas)
			if err != nil {
				t.Fatalf("RequiredAssetPaths() error = %v", err)
			}
			candidates, err := set.CleanCandidates(context.Background())
			if err != nil {
				t.Fatalf("CleanCandidates() error = %v", err)
			}
			got := candidatePaths(t, dir, candidates)
			want := append([]string(nil), tc.wantCandidates...)
			sort.Strings(want)
			if len(want) == 0 {
				want = []string{}
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("candidates = %v, want %v", got, want)
			}
			for rel, reason := range tc.keptReasons {
				assertProtected(t, set, filepath.Join(dir, rel), reason)
			}
		})
	}
}

func TestRequiredAssetPathsKeepsProtectedPathsInsideModelsDir(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "elsewhere", "embedding.onnx")
	cfg := liveCfg()
	cfg.Speaker.Models.Embedding = outside

	set, err := RequiredAssetPaths(context.Background(), cfg, dir, nil)
	if err != nil {
		t.Fatalf("RequiredAssetPaths() error = %v", err)
	}
	for _, p := range set.Protected {
		if !strings.HasPrefix(p.Path, dir+string(filepath.Separator)) {
			t.Errorf("protected path %q is outside the models dir %q", p.Path, dir)
		}
	}
}

func TestRequiredAssetPathsProtectedIsSortedAndStable(t *testing.T) {
	dir := t.TempDir()
	touchFile(t, filepath.Join(dir, "qwen3-tts", "bin", "qwen3-tts-worker"))

	set, err := RequiredAssetPaths(context.Background(), liveCfg(), dir,
		[]PersonaAssets{qwenPersona("veronica", "0.6b"), qwenPersona("ada", "1.7b")})
	if err != nil {
		t.Fatalf("RequiredAssetPaths() error = %v", err)
	}
	if !sort.SliceIsSorted(set.Protected, func(i, j int) bool {
		if set.Protected[i].Path == set.Protected[j].Path {
			return set.Protected[i].Reason < set.Protected[j].Reason
		}
		return set.Protected[i].Path < set.Protected[j].Path
	}) {
		t.Errorf("protected list is not sorted: %+v", set.Protected)
	}
	root := filepath.Join(dir, "qwen3-tts")
	owners := 0
	for _, p := range set.Protected {
		if p.Path == root {
			owners++
		}
	}
	if owners != 2 {
		t.Errorf("qwen native root has %d protected rows, want one per persona owner", owners)
	}
}

// assertProtected fails unless path is protected with a reason containing want
// and is absent from the candidate list.
func assertProtected(t *testing.T, set RequiredSet, path, want string) {
	t.Helper()
	for _, p := range set.Protected {
		if p.Path == path && strings.Contains(p.Reason, want) {
			return
		}
	}
	t.Errorf("protected list has no %q with reason %q:\n%+v", path, want, set.Protected)
}
