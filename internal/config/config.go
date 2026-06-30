package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

// Config holds all application configuration.
type Config struct {
	// TTS
	TTSProvider string  `mapstructure:"tts_provider"`
	TTSVoice    string  `mapstructure:"tts_voice"`
	SpeechSpeed float64 `mapstructure:"speech_speed"`

	// STT
	STTProvider          string `mapstructure:"stt_provider"`
	SherpaStreamingModel string `mapstructure:"sherpa_streaming_model"`
	WhisperModel         string `mapstructure:"whisper_model"`
	WhisperQuantized     bool   `mapstructure:"whisper_quantized"`
	WhisperCPPBinary     string `mapstructure:"whispercpp_binary"`
	WhisperCPPModel      string `mapstructure:"whispercpp_model"`
	WhisperCPPModelPath  string `mapstructure:"whispercpp_model_path"`

	// VAD
	VADEnabled         bool    `mapstructure:"vad_enabled"`
	VADSilenceDuration float64 `mapstructure:"vad_silence_duration"`

	// Barge-in (interrupt TTS when the user starts speaking). Off by default
	// until the echo-canceller is strong enough to avoid self-interruption.
	BargeInEnabled bool `mapstructure:"barge_in_enabled"`

	// Brain
	BrainProvider     string `mapstructure:"brain_provider"`
	OllamaModel       string `mapstructure:"ollama_model"`
	OllamaHost        string `mapstructure:"ollama_host"`
	VoiceToolsEnabled bool   `mapstructure:"voice_tools_enabled"`

	// Claude (legacy, kept for override)
	ClaudeModel        string `mapstructure:"claude_model"`
	ClaudeModelComplex string `mapstructure:"claude_model_complex"`

	// Fish Audio (optional)
	FishAPIKey     string `mapstructure:"fish_api_key"`
	FishVoiceModel string `mapstructure:"fish_voice_model_id"`

	// Paths
	ModelsDir string `mapstructure:"models_dir"`

	// Agent
	AgentName string `mapstructure:"agent_name"`

	// General
	Language        string `mapstructure:"language"`
	MaxHistory      int    `mapstructure:"max_history"`
	ListenTimeout   int    `mapstructure:"listen_timeout"`
	PhraseTimeLimit int    `mapstructure:"phrase_time_limit"`
}

var (
	configDir  = filepath.Join(homeDir(), ".obey", "agents", "voice", "samantha")
	configFile = filepath.Join(configDir, "config.yaml")
	v          *viper.Viper
)

func init() {
	v = viper.New()
	setDefaults(v)
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("tts_provider", "kokoro")
	v.SetDefault("tts_voice", "af_heart")
	v.SetDefault("speech_speed", 0.95)

	v.SetDefault("stt_provider", "sherpa")
	v.SetDefault("sherpa_streaming_model", "en-2023-06-26")
	v.SetDefault("whisper_model", "small")
	v.SetDefault("whisper_quantized", true)
	v.SetDefault("whispercpp_binary", "whisper-cli")
	v.SetDefault("whispercpp_model", "base.en")
	v.SetDefault("whispercpp_model_path", filepath.Join(homeDir(), ".cache", "samantha", "models", "whispercpp", "ggml-base.en.bin"))

	v.SetDefault("vad_enabled", true)
	v.SetDefault("vad_silence_duration", 0.5)
	v.SetDefault("barge_in_enabled", false)

	v.SetDefault("brain_provider", "claude")
	v.SetDefault("ollama_model", "")
	v.SetDefault("ollama_host", "http://localhost:11434")
	v.SetDefault("voice_tools_enabled", false)

	v.SetDefault("claude_model", "")
	v.SetDefault("claude_model_complex", "")

	v.SetDefault("fish_api_key", "")
	v.SetDefault("fish_voice_model_id", "474887f7949b4d1ab3e626cddf82613a")

	v.SetDefault("agent_name", "Samantha")
	v.SetDefault("models_dir", filepath.Join(homeDir(), ".cache", "samantha", "models"))

	v.SetDefault("language", "en-US")
	v.SetDefault("max_history", 10)
	v.SetDefault("listen_timeout", 10)
	v.SetDefault("phrase_time_limit", 30)
}

// Load reads configuration from disk and environment.
func Load() (*Config, error) {
	v.SetConfigFile(configFile)
	v.SetConfigType("yaml")

	// Environment variable overrides
	v.SetEnvPrefix("")
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	// Explicit env bindings
	bindings := map[string]string{
		"tts_provider":           "TTS_PROVIDER",
		"tts_voice":              "TTS_VOICE",
		"stt_provider":           "STT_PROVIDER",
		"sherpa_streaming_model": "SHERPA_STREAMING_MODEL",
		"whisper_model":          "WHISPER_MODEL",
		"whispercpp_binary":      "WHISPERCPP_BINARY",
		"whispercpp_model":       "WHISPERCPP_MODEL",
		"whispercpp_model_path":  "WHISPERCPP_MODEL_PATH",
		"fish_api_key":           "FISH_API_KEY",
		"models_dir":             "MODELS_DIR",
		"brain_provider":         "BRAIN_PROVIDER",
		"ollama_model":           "OLLAMA_MODEL",
		"ollama_host":            "OLLAMA_HOST",
		"voice_tools_enabled":    "VOICE_TOOLS_ENABLED",
		"barge_in_enabled":       "BARGE_IN_ENABLED",
	}
	for key, env := range bindings {
		_ = v.BindEnv(key, env)
	}

	// Read config file (missing file is OK — use defaults)
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("reading config: %w", err)
			}
		}
	}

	// Migrate old Python keys: voice_model_id -> fish_voice_model_id
	if v.IsSet("voice_model_id") {
		v.Set("fish_voice_model_id", v.GetString("voice_model_id"))
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	return &cfg, nil
}

// Get returns a config value by key.
func Get(key string) any {
	return v.Get(key)
}

// Set updates a config value.
func Set(key string, value any) {
	v.Set(key, value)
}

// Save persists current config to disk.
func Save() error {
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	return v.WriteConfigAs(configFile)
}

// SetAndSave updates a value and persists to disk.
func SetAndSave(key string, value any) error {
	Set(key, value)
	return Save()
}

// AllSettings returns all config as a map.
func AllSettings() map[string]any {
	return v.AllSettings()
}

// AllKeys returns all config keys.
func AllKeys() []string {
	return v.AllKeys()
}

// ConfigFile returns the config file path.
func ConfigFile() string {
	return configFile
}

// ModelsDir returns the model cache directory.
func ModelsDir() string {
	return v.GetString("models_dir")
}

// SessionsDir returns the sessions directory.
func SessionsDir() string {
	return filepath.Join(configDir, "sessions")
}

// ConfigDir returns the config directory path.
func ConfigDir() string {
	return configDir
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}
