package config

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/viper"
)

// resetViper swaps in a fresh viper so earlier tests' Set overrides don't leak.
func resetViper(t *testing.T) {
	t.Helper()
	orig := v
	v = viper.New()
	setDefaults(v)
	t.Cleanup(func() { v = orig })
}

func TestLoadDefaults(t *testing.T) {
	// Point to a non-existent config file so defaults are used
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()

	// Re-init viper with fresh defaults
	setDefaults(v)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.TTSProvider != "kokoro" {
		t.Errorf("TTSProvider = %q, want kokoro", cfg.TTSProvider)
	}
	if cfg.TTSVoice != "af_heart" {
		t.Errorf("TTSVoice = %q, want af_heart", cfg.TTSVoice)
	}
	if cfg.STTProvider != "sherpa" {
		t.Errorf("STTProvider = %q, want sherpa", cfg.STTProvider)
	}
	if cfg.SherpaStreamingModel != "en-2023-06-26" {
		t.Errorf("SherpaStreamingModel = %q, want en-2023-06-26", cfg.SherpaStreamingModel)
	}
	if cfg.WhisperModel != "small" {
		t.Errorf("WhisperModel = %q, want small", cfg.WhisperModel)
	}
	if cfg.WhisperCPPBinary != "whisper-cli" {
		t.Errorf("WhisperCPPBinary = %q, want whisper-cli", cfg.WhisperCPPBinary)
	}
	if cfg.WhisperCPPModel != "base.en" {
		t.Errorf("WhisperCPPModel = %q, want base.en", cfg.WhisperCPPModel)
	}
	if cfg.MaxHistory != 10 {
		t.Errorf("MaxHistory = %d, want 10", cfg.MaxHistory)
	}
	if !cfg.VADEnabled {
		t.Error("VADEnabled = false, want true")
	}
	if cfg.VoiceToolsEnabled {
		t.Error("VoiceToolsEnabled = true, want false")
	}
	if cfg.BargeInEnabled {
		t.Error("BargeInEnabled = true, want false")
	}
	if cfg.VADThreshold != 0.6 {
		t.Errorf("VADThreshold = %v, want 0.6", cfg.VADThreshold)
	}
	if cfg.VADMinSpeechDuration != 0.25 {
		t.Errorf("VADMinSpeechDuration = %v, want 0.25", cfg.VADMinSpeechDuration)
	}
	if cfg.VoiceFrontendEnabled {
		t.Error("VoiceFrontendEnabled = true, want false (frontend over-suppresses normal speech)")
	}
}

func TestBargeInEnvOverride(t *testing.T) {
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()

	setDefaults(v)
	t.Setenv("BARGE_IN_ENABLED", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if !cfg.BargeInEnabled {
		t.Error("BargeInEnabled = false, want true from env")
	}
}

func TestLoadValidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `tts_provider: edge
tts_voice: en-US-AriaNeural
speech_speed: 1.1
max_history: 20
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := configFile
	configFile = path
	defer func() { configFile = orig }()

	setDefaults(v)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.TTSProvider != "edge" {
		t.Errorf("TTSProvider = %q, want edge", cfg.TTSProvider)
	}
	if cfg.TTSVoice != "en-US-AriaNeural" {
		t.Errorf("TTSVoice = %q, want en-US-AriaNeural", cfg.TTSVoice)
	}
	if cfg.SpeechSpeed != 1.1 {
		t.Errorf("SpeechSpeed = %f, want 1.1", cfg.SpeechSpeed)
	}
	if cfg.MaxHistory != 20 {
		t.Errorf("MaxHistory = %d, want 20", cfg.MaxHistory)
	}
	// Defaults still applied for unset keys
	if cfg.STTProvider != "sherpa" {
		t.Errorf("STTProvider = %q, want sherpa (default)", cfg.STTProvider)
	}
}

func TestEnvOverride(t *testing.T) {
	dir := t.TempDir()
	orig := configFile
	configFile = filepath.Join(dir, "config.yaml")
	defer func() { configFile = orig }()

	setDefaults(v)
	t.Setenv("BRAIN_PROVIDER", "ollama")
	t.Setenv("OLLAMA_MODEL", "test-model-xyz")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.BrainProvider != "ollama" {
		t.Errorf("BrainProvider = %q, want ollama (from env)", cfg.BrainProvider)
	}
	if cfg.OllamaModel != "test-model-xyz" {
		t.Errorf("OllamaModel = %q, want test-model-xyz (from env)", cfg.OllamaModel)
	}
}

func TestGrokModelEnvOverride(t *testing.T) {
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()

	setDefaults(v)
	t.Setenv("BRAIN_PROVIDER", "grok")
	t.Setenv("GROK_MODEL", "grok-test-xyz")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.BrainProvider != "grok" {
		t.Errorf("BrainProvider = %q, want grok (from env)", cfg.BrainProvider)
	}
	if cfg.GrokModel != "grok-test-xyz" {
		t.Errorf("GrokModel = %q, want grok-test-xyz (from env)", cfg.GrokModel)
	}
}

func TestVoiceToolsEnvOverride(t *testing.T) {
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()

	setDefaults(v)
	t.Setenv("VOICE_TOOLS_ENABLED", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if !cfg.VoiceToolsEnabled {
		t.Error("VoiceToolsEnabled = false, want true from env")
	}
}

func TestModelsDirDefault(t *testing.T) {
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()

	setDefaults(v)
	_, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".cache", "samantha", "models")
	got := ModelsDir()
	if got != want {
		t.Errorf("ModelsDir() = %q, want %q", got, want)
	}
}

func TestModelsDirEnvOverride(t *testing.T) {
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()

	setDefaults(v)
	t.Setenv("MODELS_DIR", "/mnt/fast/models")

	_, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	got := ModelsDir()
	if got != "/mnt/fast/models" {
		t.Errorf("ModelsDir() = %q, want /mnt/fast/models", got)
	}
}

func TestDownloadFileCreatesNestedParentDir(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("model"))
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "whispercpp", "ggml-base.en.bin")
	if err := downloadFile(path, server.URL, nil); err != nil {
		t.Fatalf("downloadFile() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "model" {
		t.Fatalf("downloaded data = %q, want model", data)
	}
}

func TestSaveAndReload(t *testing.T) {
	dir := t.TempDir()
	origDir := configDir
	origFile := configFile
	configDir = dir
	configFile = filepath.Join(dir, "config.yaml")
	defer func() {
		configDir = origDir
		configFile = origFile
	}()

	setDefaults(v)

	if err := SetAndSave("tts_voice", "af_bella"); err != nil {
		t.Fatalf("SetAndSave() error: %v", err)
	}

	// Reload
	setDefaults(v)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.TTSVoice != "af_bella" {
		t.Errorf("TTSVoice = %q, want af_bella (saved)", cfg.TTSVoice)
	}
}

func TestValidateAndSet(t *testing.T) {
	tests := []struct {
		name string
		key  string
		raw  string
		want string
	}{
		{"string", "tts_voice", "af_nova", "af_nova"},
		{"bool", "vad_enabled", "false", "false"},
		{"int", "max_history", "25", "25"},
		{"float", "vad_threshold", "0.45", "0.45"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			origDir, origFile := configDir, configFile
			configDir, configFile = dir, filepath.Join(dir, "config.yaml")
			defer func() { configDir, configFile = origDir, origFile }()
			resetViper(t)

			if err := ValidateAndSet(tt.key, tt.raw); err != nil {
				t.Fatalf("ValidateAndSet() error: %v", err)
			}

			// Reload from disk with a fresh viper: the value must persist and
			// the config must still unmarshal.
			resetViper(t)
			if _, err := Load(); err != nil {
				t.Fatalf("Load() after ValidateAndSet error: %v", err)
			}
			if got := fmt.Sprint(Get(tt.key)); got != tt.want {
				t.Errorf("Get(%s) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestValidateAndSetRejects(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		raw     string
		wantErr string
	}{
		{"unknown key", "vad_treshold", "0.5", "unknown config key"},
		{"bad bool", "vad_enabled", "high", "expected true or false"},
		{"bad int", "max_history", "many", "expected an integer"},
		{"bad float", "vad_threshold", "high", "expected a number"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			origDir, origFile := configDir, configFile
			configDir, configFile = dir, filepath.Join(dir, "config.yaml")
			defer func() { configDir, configFile = origDir, origFile }()
			resetViper(t)

			err := ValidateAndSet(tt.key, tt.raw)
			if err == nil {
				t.Fatalf("ValidateAndSet(%s, %s) = nil, want error", tt.key, tt.raw)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
			if _, statErr := os.Stat(configFile); !os.IsNotExist(statErr) {
				t.Error("config file written despite invalid input")
			}
		})
	}
}

func TestLanguageCode(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"en-US", "en"},
		{"en", "en"},
		{"", "en"},
		{"C", "c"},
		{"pt-BR", "pt"},
		{" en-GB ", "en"},
	}
	for _, tt := range tests {
		if got := LanguageCode(tt.in); got != tt.want {
			t.Errorf("LanguageCode(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestLanguageEnvDoesNotLeak(t *testing.T) {
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()
	resetViper(t)

	t.Setenv("LANGUAGE", "en_US:en") // standard locale var, not a samantha setting
	t.Setenv("TTS_VOICE", "af_nova") // documented binding must still apply

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Language != "en-US" {
		t.Errorf("Language = %q, want en-US default (LANGUAGE env must not bind)", cfg.Language)
	}
	if cfg.TTSVoice != "af_nova" {
		t.Errorf("TTSVoice = %q, want af_nova from TTS_VOICE env", cfg.TTSVoice)
	}
}

func TestConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	origDir, origFile := configDir, configFile
	configDir, configFile = dir, filepath.Join(dir, "config.yaml")
	defer func() { configDir, configFile = origDir, origFile }()
	resetViper(t)

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(writer bool) {
			defer wg.Done()
			for range 25 {
				if writer {
					_ = SetAndSave("tts_voice", "af_bella")
				} else {
					_ = Get("tts_voice")
					_ = ModelsDir()
					_ = AllSettings()
					_ = AllKeys()
				}
			}
		}(i%2 == 0)
	}
	wg.Wait()
}
