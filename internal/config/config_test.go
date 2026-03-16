package config

import (
	"os"
	"path/filepath"
	"testing"
)

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
	if cfg.WhisperModel != "small" {
		t.Errorf("WhisperModel = %q, want small", cfg.WhisperModel)
	}
	if cfg.MaxHistory != 10 {
		t.Errorf("MaxHistory = %d, want 10", cfg.MaxHistory)
	}
	if !cfg.VADEnabled {
		t.Error("VADEnabled = false, want true")
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
	t.Setenv("TTS_PROVIDER", "fish")
	t.Setenv("FISH_API_KEY", "sk-test-123")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.TTSProvider != "fish" {
		t.Errorf("TTSProvider = %q, want fish (from env)", cfg.TTSProvider)
	}
	if cfg.FishAPIKey != "sk-test-123" {
		t.Errorf("FishAPIKey = %q, want sk-test-123", cfg.FishAPIKey)
	}
}

func TestMigratePythonKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// Python version used voice_model_id instead of fish_voice_model_id
	content := `voice_model_id: old-model-id-123
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

	if cfg.FishVoiceModel != "old-model-id-123" {
		t.Errorf("FishVoiceModel = %q, want old-model-id-123 (migrated)", cfg.FishVoiceModel)
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
