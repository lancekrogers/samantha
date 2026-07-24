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
	if cfg.TTSFallbackProvider != "kokoro" {
		t.Errorf("TTSFallbackProvider = %q, want kokoro", cfg.TTSFallbackProvider)
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

func TestOllamaDefaultsVoiceToolsOnWhenUnset(t *testing.T) {
	resetViper(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// brain_provider only — no voice_tools_enabled key.
	if err := os.WriteFile(path, []byte("brain_provider: ollama\nollama_model: llama3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := configFile
	configFile = path
	defer func() { configFile = orig }()
	t.Setenv("VOICE_TOOLS_ENABLED", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if !cfg.VoiceToolsEnabled {
		t.Fatal("ollama without explicit voice_tools_enabled must default tools on")
	}
}

func TestOllamaRespectsExplicitVoiceToolsFalse(t *testing.T) {
	resetViper(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("brain_provider: ollama\nvoice_tools_enabled: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := configFile
	configFile = path
	defer func() { configFile = orig }()
	t.Setenv("VOICE_TOOLS_ENABLED", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.VoiceToolsEnabled {
		t.Fatal("explicit voice_tools_enabled: false must win")
	}
}

func TestOllamaDefaultsSkillsOnWhenUnset(t *testing.T) {
	resetViper(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("brain_provider: ollama\nollama_model: llama3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := configFile
	configFile = path
	defer func() { configFile = orig }()
	t.Setenv("SKILLS_ENABLED", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if !cfg.SkillsEnabled {
		t.Fatal("ollama without explicit skills_enabled must default skills on")
	}
}

func TestOllamaRespectsExplicitSkillsFalse(t *testing.T) {
	resetViper(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("brain_provider: ollama\nskills_enabled: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := configFile
	configFile = path
	defer func() { configFile = orig }()
	t.Setenv("SKILLS_ENABLED", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.SkillsEnabled {
		t.Fatal("explicit skills_enabled: false must win")
	}
}

func TestToolCommandTimeoutEnvOverride(t *testing.T) {
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()

	setDefaults(v)
	t.Setenv("TOOL_COMMAND_TIMEOUT", "90")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.ToolCommandTimeout != 90 {
		t.Fatalf("ToolCommandTimeout = %d, want 90 from env", cfg.ToolCommandTimeout)
	}
}

func TestClampToolCommandTimeout(t *testing.T) {
	tests := []struct {
		in, want int
	}{
		{0, 30},
		{-1, 30},
		{1, 1},
		{30, 30},
		{120, 120},
		{121, 120},
		{86400, 120},
	}
	for _, tt := range tests {
		if got := ClampToolCommandTimeout(tt.in); got != tt.want {
			t.Fatalf("ClampToolCommandTimeout(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestApplyOllamaDefaultsAfterProviderSwitch(t *testing.T) {
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()
	setDefaults(v)
	// No explicit tools/skills keys in config file.
	cfg := &Config{BrainProvider: "claude"}
	ApplyOllamaDefaults(cfg)
	if cfg.VoiceToolsEnabled || cfg.SkillsEnabled {
		t.Fatal("claude provider must not auto-enable tools/skills")
	}
	cfg.BrainProvider = "ollama"
	ApplyOllamaDefaults(cfg)
	if !cfg.VoiceToolsEnabled || !cfg.SkillsEnabled {
		t.Fatalf("ollama unset keys should auto-enable tools/skills, got tools=%v skills=%v",
			cfg.VoiceToolsEnabled, cfg.SkillsEnabled)
	}
}

func TestSetAndSaveBrainProviderPersistsOllamaDefaults(t *testing.T) {
	t.Setenv("VOICE_TOOLS_ENABLED", "")
	t.Setenv("SKILLS_ENABLED", "")

	dir := t.TempDir()
	origDir, origFile := configDir, configFile
	configDir = dir
	configFile = filepath.Join(dir, "config.yaml")
	defer func() {
		configDir, configFile = origDir, origFile
	}()
	resetViper(t)

	cfg := &Config{BrainProvider: "claude"}
	if err := SetAndSaveBrainProvider(cfg, "ollama"); err != nil {
		t.Fatalf("SetAndSaveBrainProvider() error: %v", err)
	}
	if !cfg.VoiceToolsEnabled || !cfg.SkillsEnabled {
		t.Fatalf("live config should enable Ollama capabilities, got tools=%v skills=%v",
			cfg.VoiceToolsEnabled, cfg.SkillsEnabled)
	}

	// Recreate Viper to model the next process startup rather than relying on
	// the values still held by the current process.
	v = viper.New()
	setDefaults(v)
	reloaded, err := Load()
	if err != nil {
		t.Fatalf("Load() after provider switch: %v", err)
	}
	if !reloaded.VoiceToolsEnabled || !reloaded.SkillsEnabled {
		t.Fatalf("reloaded config should keep Ollama capabilities enabled, got tools=%v skills=%v",
			reloaded.VoiceToolsEnabled, reloaded.SkillsEnabled)
	}
}

func TestSetAndSaveBrainProviderEnablesOllamaToolsDespiteDumpedFalse(t *testing.T) {
	// WriteConfigAs often materializes voice_tools_enabled: false as a real key.
	// Switching to Ollama in Settings must re-enable tools so local models can
	// actually use list/read/write/run_command.
	t.Setenv("VOICE_TOOLS_ENABLED", "")
	t.Setenv("SKILLS_ENABLED", "")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("brain_provider: claude\nvoice_tools_enabled: false\nskills_enabled: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	origDir, origFile := configDir, configFile
	configDir, configFile = dir, path
	defer func() {
		configDir, configFile = origDir, origFile
	}()
	resetViper(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("initial Load(): %v", err)
	}
	if err := SetAndSaveBrainProvider(cfg, "ollama"); err != nil {
		t.Fatalf("SetAndSaveBrainProvider() error: %v", err)
	}
	if !cfg.VoiceToolsEnabled || !cfg.SkillsEnabled {
		t.Fatalf("switch to ollama should enable tools/skills, got tools=%v skills=%v",
			cfg.VoiceToolsEnabled, cfg.SkillsEnabled)
	}

	v = viper.New()
	setDefaults(v)
	reloaded, err := Load()
	if err != nil {
		t.Fatalf("Load() after switch: %v", err)
	}
	if !reloaded.VoiceToolsEnabled || !reloaded.SkillsEnabled {
		t.Fatalf("persisted ollama switch should keep tools/skills on, got tools=%v skills=%v",
			reloaded.VoiceToolsEnabled, reloaded.SkillsEnabled)
	}
}

func TestSetAndSaveBrainProviderHonorsEnvToolsOff(t *testing.T) {
	t.Setenv("VOICE_TOOLS_ENABLED", "false")
	t.Setenv("SKILLS_ENABLED", "false")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("brain_provider: claude\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	origDir, origFile := configDir, configFile
	configDir, configFile = dir, path
	defer func() {
		configDir, configFile = origDir, origFile
	}()
	resetViper(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if err := SetAndSaveBrainProvider(cfg, "ollama"); err != nil {
		t.Fatalf("SetAndSaveBrainProvider() error: %v", err)
	}
	if cfg.VoiceToolsEnabled || cfg.SkillsEnabled {
		t.Fatalf("env=false must keep tools/skills off, got tools=%v skills=%v",
			cfg.VoiceToolsEnabled, cfg.SkillsEnabled)
	}
}

func TestPromptConfigEnvOverrides(t *testing.T) {
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()

	setDefaults(v)
	t.Setenv("PERSONA", "work")
	t.Setenv("PROMPTS_DIR", "/tmp/samantha-prompts")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Persona != "work" {
		t.Errorf("Persona = %q, want work from env", cfg.Persona)
	}
	if cfg.PromptsDir != "/tmp/samantha-prompts" {
		t.Errorf("PromptsDir = %q, want /tmp/samantha-prompts from env", cfg.PromptsDir)
	}
	if got := PromptsDir(); got != "/tmp/samantha-prompts" {
		t.Errorf("PromptsDir() = %q, want /tmp/samantha-prompts", got)
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

	want := DefaultModelsDir()
	got := ModelsDir()
	if got != want {
		t.Errorf("ModelsDir() = %q, want %q", got, want)
	}
	if !strings.Contains(got, AppSlug) {
		t.Errorf("ModelsDir() = %q, want path containing AppSlug %q", got, AppSlug)
	}
}

func TestSkillsConfigDefaults(t *testing.T) {
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()

	setDefaults(v)
	t.Setenv("SKILLS_ENABLED", "")
	t.Setenv("SKILLS_DIR", "")
	t.Setenv("OLLAMA_EMBEDDING_MODEL", "")
	t.Setenv("SKILLS_SIMILARITY_THRESHOLD", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.SkillsEnabled {
		t.Error("SkillsEnabled default = true, want false")
	}
	if cfg.SkillsDir != "" {
		t.Errorf("SkillsDir field = %q, want empty default", cfg.SkillsDir)
	}
	if cfg.OllamaEmbeddingModel != "nomic-embed-text" {
		t.Errorf("OllamaEmbeddingModel = %q, want nomic-embed-text default", cfg.OllamaEmbeddingModel)
	}
	if cfg.SkillsSimilarityThreshold != 0.55 {
		t.Errorf("SkillsSimilarityThreshold = %v, want 0.55 default", cfg.SkillsSimilarityThreshold)
	}
	wantDir := filepath.Join(configDir, "skills")
	if got := SkillsDir(); got != wantDir {
		t.Errorf("SkillsDir() = %q, want %q", got, wantDir)
	}
}

func TestSkillsConfigEnvOverrides(t *testing.T) {
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()

	setDefaults(v)
	t.Setenv("SKILLS_ENABLED", "true")
	t.Setenv("SKILLS_DIR", "/tmp/samantha-skills")
	t.Setenv("OLLAMA_EMBEDDING_MODEL", "embeddinggemma")
	t.Setenv("SKILLS_SIMILARITY_THRESHOLD", "0.7")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if !cfg.SkillsEnabled {
		t.Error("SkillsEnabled = false, want true from env")
	}
	if cfg.SkillsDir != "/tmp/samantha-skills" {
		t.Errorf("SkillsDir = %q, want /tmp/samantha-skills from env", cfg.SkillsDir)
	}
	if cfg.OllamaEmbeddingModel != "embeddinggemma" {
		t.Errorf("OllamaEmbeddingModel = %q, want embeddinggemma from env", cfg.OllamaEmbeddingModel)
	}
	if cfg.SkillsSimilarityThreshold != 0.7 {
		t.Errorf("SkillsSimilarityThreshold = %v, want 0.7 from env", cfg.SkillsSimilarityThreshold)
	}
	if got := SkillsDir(); got != "/tmp/samantha-skills" {
		t.Errorf("SkillsDir() = %q, want /tmp/samantha-skills", got)
	}
}

func TestSkillsEnabledRoundTrip(t *testing.T) {
	dir := t.TempDir()
	origFile, origDir := configFile, configDir
	configDir = dir
	configFile = filepath.Join(dir, "config.yaml")
	defer func() {
		configFile = origFile
		configDir = origDir
	}()

	setDefaults(v)
	Set("skills_enabled", true)
	Set("skills_dir", filepath.Join(dir, "my-skills"))
	if err := Save(); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// Reset and reload from disk.
	setDefaults(v)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() after Save: %v", err)
	}
	if !cfg.SkillsEnabled {
		t.Error("SkillsEnabled did not round-trip true")
	}
	want := filepath.Join(dir, "my-skills")
	if cfg.SkillsDir != want {
		t.Errorf("SkillsDir = %q, want %q after round-trip", cfg.SkillsDir, want)
	}
	if got := SkillsDir(); got != want {
		t.Errorf("SkillsDir() = %q, want %q after round-trip", got, want)
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
	if err := downloadFile(t.Context(), path, server.URL, "ggml-base.en.bin", 0, "", nil); err != nil {
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
