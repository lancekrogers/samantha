package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/spf13/viper"
)

// Config holds all application configuration.
type Config struct {
	// TTS
	TTSProvider    string  `mapstructure:"tts_provider"`
	TTSVoice       string  `mapstructure:"tts_voice"`
	SpeechSpeed    float64 `mapstructure:"speech_speed"`
	QwenTTSBinary  string  `mapstructure:"qwen_tts_binary"`
	QwenTTSModel   string  `mapstructure:"qwen_tts_model"`
	QwenTTSTimeout int     `mapstructure:"qwen_tts_timeout"`
	OutputDevice   string  `mapstructure:"output_device"`

	// STT
	STTProvider          string `mapstructure:"stt_provider"`
	InputDevice          string `mapstructure:"input_device"`
	STTMode              string `mapstructure:"stt_mode"`
	SherpaStreamingModel string `mapstructure:"sherpa_streaming_model"`
	WhisperModel         string `mapstructure:"whisper_model"`
	WhisperQuantized     bool   `mapstructure:"whisper_quantized"`
	WhisperCPPBinary     string `mapstructure:"whispercpp_binary"`
	WhisperCPPModel      string `mapstructure:"whispercpp_model"`
	WhisperCPPModelPath  string `mapstructure:"whispercpp_model_path"`

	// VAD
	VADEnabled           bool    `mapstructure:"vad_enabled"`
	VADSilenceDuration   float64 `mapstructure:"vad_silence_duration"`
	VADThreshold         float64 `mapstructure:"vad_threshold"`
	VADMinSpeechDuration float64 `mapstructure:"vad_min_speech_duration"`
	// VADPreRollMS is how much audio captured just before the VAD confirms
	// speech is prepended to the recognized segment. The Silero VAD only marks
	// a segment once ~MinSpeechDuration of speech has accrued, so onset audio
	// (the first word) is otherwise clipped before STT ever sees it.
	VADPreRollMS int `mapstructure:"vad_pre_roll_ms"`

	// VoiceFrontend runs local AEC/NS/AGC on the mic before STT. Disable to feed
	// raw audio to the recognizer (often more accurate for whisper).
	VoiceFrontendEnabled bool `mapstructure:"voice_frontend_enabled"`

	// Barge-in (interrupt TTS when the user starts speaking). Off by default
	// until the echo-canceller is strong enough to avoid self-interruption.
	BargeInEnabled bool `mapstructure:"barge_in_enabled"`

	// Brain
	BrainProvider     string `mapstructure:"brain_provider"`
	GrokModel         string `mapstructure:"grok_model"`
	OllamaModel       string `mapstructure:"ollama_model"`
	OllamaHost        string `mapstructure:"ollama_host"`
	VoiceToolsEnabled bool   `mapstructure:"voice_tools_enabled"`
	// RemoteToolsEnabled gates tool calls for turns triggered over the
	// network (samantha serve). Deliberately separate from
	// voice_tools_enabled: remote turns default-deny tools regardless of
	// the local flag.
	RemoteToolsEnabled bool `mapstructure:"remote_tools_enabled"`

	// Calibre (optional library catalog/resolver for audiobook input).
	// Off by default so non-users are unaffected.
	CalibreEnabled       bool   `mapstructure:"calibre_enabled"`
	CalibreLibraryPath   string `mapstructure:"calibre_library_path"`
	CalibredbBinary      string `mapstructure:"calibredb_binary"`
	CalibreConvertBinary string `mapstructure:"calibre_convert_binary"`
	CalibrePreferFormat  string `mapstructure:"calibre_prefer_format"`

	// Paths
	ModelsDir string `mapstructure:"models_dir"`

	// Agent
	AgentName string `mapstructure:"agent_name"`

	// Prompts
	Persona    string `mapstructure:"persona"`
	PromptsDir string `mapstructure:"prompts_dir"`

	// Skills (Agent Skills / SKILL.md). Opt-in; Ollama loads the catalog when
	// SkillsEnabled is true. See internal/skills.
	SkillsEnabled bool   `mapstructure:"skills_enabled"`
	SkillsDir     string `mapstructure:"skills_dir"`

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
	// mu guards v: the TUI reads config from tea.Cmd goroutines while the
	// Update goroutine writes via SetAndSave.
	mu sync.RWMutex
)

func init() {
	v = viper.New()
	setDefaults(v)
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("tts_provider", "kokoro")
	v.SetDefault("tts_voice", "af_heart")
	v.SetDefault("speech_speed", 0.95)
	v.SetDefault("qwen_tts_binary", "qwen3-tts-cli")
	v.SetDefault("qwen_tts_model", "")
	v.SetDefault("qwen_tts_timeout", 120)
	v.SetDefault("output_device", "")

	v.SetDefault("stt_provider", "sherpa")
	v.SetDefault("input_device", "")
	v.SetDefault("stt_mode", "")
	v.SetDefault("sherpa_streaming_model", "en-2023-06-26")
	v.SetDefault("whisper_model", "small")
	v.SetDefault("whisper_quantized", true)
	v.SetDefault("whispercpp_binary", "whisper-cli")
	v.SetDefault("whispercpp_model", "base.en")
	v.SetDefault("whispercpp_model_path", filepath.Join(homeDir(), ".cache", "samantha", "models", "whispercpp", "ggml-base.en.bin"))

	v.SetDefault("vad_enabled", true)
	v.SetDefault("vad_silence_duration", 0.8)
	v.SetDefault("vad_threshold", 0.6)
	v.SetDefault("vad_min_speech_duration", 0.25)
	v.SetDefault("vad_pre_roll_ms", 300)
	// Off by default: the frontend's noise suppressor over-suppresses normal-volume
	// speech (gates it below the VAD threshold — see voice_frontend_test.go), which
	// left voice mode stuck on "listening" until the user spoke loudly. Re-enabling
	// needs the suppressor re-tuned against real mic input. Its main consumer, AEC
	// for barge-in, is also off by default.
	v.SetDefault("voice_frontend_enabled", false)
	v.SetDefault("barge_in_enabled", false)

	v.SetDefault("brain_provider", "claude")
	v.SetDefault("grok_model", "")
	v.SetDefault("ollama_model", "")
	v.SetDefault("ollama_host", "http://localhost:11434")
	v.SetDefault("voice_tools_enabled", false)
	v.SetDefault("remote_tools_enabled", false)

	// Calibre is opt-in. Empty binaries are resolved by bundle-aware LookPath.
	v.SetDefault("calibre_enabled", false)
	v.SetDefault("calibre_library_path", "")
	v.SetDefault("calibredb_binary", "")
	v.SetDefault("calibre_convert_binary", "")
	v.SetDefault("calibre_prefer_format", "epub")

	v.SetDefault("agent_name", "Samantha")
	v.SetDefault("persona", "samantha")
	v.SetDefault("prompts_dir", "")
	v.SetDefault("skills_enabled", false)
	v.SetDefault("skills_dir", "")
	v.SetDefault("models_dir", filepath.Join(homeDir(), ".cache", "samantha", "models"))

	v.SetDefault("language", "en-US")
	v.SetDefault("max_history", 10)
	v.SetDefault("listen_timeout", 10)
	v.SetDefault("phrase_time_limit", 30)
}

// Load reads configuration from disk and environment.
func Load() (*Config, error) {
	mu.Lock()
	defer mu.Unlock()

	v.SetConfigFile(configFile)
	v.SetConfigType("yaml")

	// Explicit env bindings only — no AutomaticEnv, which would bind every
	// key to its bare upper-cased name and let unrelated vars like the
	// standard LANGUAGE leak into (and get persisted over) config values.
	bindings := map[string]string{
		"tts_provider":            "TTS_PROVIDER",
		"tts_voice":               "TTS_VOICE",
		"qwen_tts_binary":         "QWEN_TTS_BINARY",
		"qwen_tts_model":          "QWEN_TTS_MODEL",
		"qwen_tts_timeout":        "QWEN_TTS_TIMEOUT",
		"output_device":           "OUTPUT_DEVICE",
		"stt_provider":            "STT_PROVIDER",
		"input_device":            "INPUT_DEVICE",
		"stt_mode":                "STT_MODE",
		"sherpa_streaming_model":  "SHERPA_STREAMING_MODEL",
		"whisper_model":           "WHISPER_MODEL",
		"whispercpp_binary":       "WHISPERCPP_BINARY",
		"whispercpp_model":        "WHISPERCPP_MODEL",
		"whispercpp_model_path":   "WHISPERCPP_MODEL_PATH",
		"models_dir":              "MODELS_DIR",
		"brain_provider":          "BRAIN_PROVIDER",
		"grok_model":              "GROK_MODEL",
		"ollama_model":            "OLLAMA_MODEL",
		"ollama_host":             "OLLAMA_HOST",
		"voice_tools_enabled":     "VOICE_TOOLS_ENABLED",
		"persona":                 "PERSONA",
		"prompts_dir":             "PROMPTS_DIR",
		"skills_enabled":          "SKILLS_ENABLED",
		"skills_dir":              "SKILLS_DIR",
		"barge_in_enabled":        "BARGE_IN_ENABLED",
		"vad_threshold":           "VAD_THRESHOLD",
		"vad_min_speech_duration": "VAD_MIN_SPEECH_DURATION",
		"voice_frontend_enabled":  "VOICE_FRONTEND_ENABLED",
		"calibre_enabled":         "CALIBRE_ENABLED",
		"calibre_library_path":    "CALIBRE_LIBRARY_PATH",
		"calibredb_binary":        "CALIBREDB_BINARY",
		"calibre_convert_binary":  "CALIBRE_CONVERT_BINARY",
		"calibre_prefer_format":   "CALIBRE_PREFER_FORMAT",
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

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	applyOllamaToolsDefault(&cfg, v)
	return &cfg, nil
}

// applyOllamaToolsDefault enables local tool calling for Ollama when the user
// has not explicitly set voice_tools_enabled in the config file or
// VOICE_TOOLS_ENABLED env. Ollama's tools (list/read/write/run_command) are
// the only path to file I/O for local models; leaving them off by default
// made tool-capable models look broken. Remote serve remains default-deny via
// remote_tools_enabled. An explicit voice_tools_enabled: false still wins.
func applyOllamaToolsDefault(cfg *Config, v *viper.Viper) {
	if !strings.EqualFold(strings.TrimSpace(cfg.BrainProvider), "ollama") {
		return
	}
	if os.Getenv("VOICE_TOOLS_ENABLED") != "" {
		return
	}
	if v.InConfig("voice_tools_enabled") {
		return
	}
	cfg.VoiceToolsEnabled = true
}

// Get returns a config value by key.
func Get(key string) any {
	mu.RLock()
	defer mu.RUnlock()
	return v.Get(key)
}

// Set updates a config value.
func Set(key string, value any) {
	mu.Lock()
	defer mu.Unlock()
	v.Set(key, value)
}

// Save persists current config to disk.
func Save() error {
	mu.Lock()
	defer mu.Unlock()
	return save()
}

// save writes the config file. Callers must hold mu.
func save() error {
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	return v.WriteConfigAs(configFile)
}

// SetAndSave updates a value and persists to disk.
func SetAndSave(key string, value any) error {
	mu.Lock()
	defer mu.Unlock()
	v.Set(key, value)
	return save()
}

// ValidateAndSet coerces raw to the type of key's current effective value and
// persists it, rejecting unknown keys so a typo can't corrupt the config file.
func ValidateAndSet(key, raw string) error {
	key = strings.ToLower(key)
	keys := AllKeys()
	if !slices.Contains(keys, key) {
		sort.Strings(keys)
		return fmt.Errorf("unknown config key %q (valid keys: %s)", key, strings.Join(keys, ", "))
	}
	value, err := coerceValue(Get(key), raw)
	if err != nil {
		return fmt.Errorf("invalid value for %s: %w", key, err)
	}
	return SetAndSave(key, value)
}

// coerceValue converts raw to the type of current so persisted values keep
// unmarshalling into Config.
func coerceValue(current any, raw string) (any, error) {
	switch current.(type) {
	case bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("expected true or false, got %q", raw)
		}
		return b, nil
	case int, int64:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("expected an integer, got %q", raw)
		}
		return n, nil
	case float64:
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("expected a number, got %q", raw)
		}
		return f, nil
	default:
		return raw, nil
	}
}

// AllSettings returns all config as a map.
func AllSettings() map[string]any {
	mu.RLock()
	defer mu.RUnlock()
	return v.AllSettings()
}

// AllKeys returns all config keys.
func AllKeys() []string {
	mu.RLock()
	defer mu.RUnlock()
	return v.AllKeys()
}

// ConfigFile returns the config file path.
func ConfigFile() string {
	return configFile
}

// ModelsDir returns the model cache directory.
func ModelsDir() string {
	mu.RLock()
	defer mu.RUnlock()
	return v.GetString("models_dir")
}

// LanguageCode returns the lowercase language code of a locale tag
// ("en-US" -> "en"), falling back to "en" when empty.
func LanguageCode(lang string) string {
	lang = strings.TrimSpace(lang)
	if lang == "" {
		return "en"
	}
	parts := strings.Split(lang, "-")
	return strings.ToLower(parts[0])
}

// SessionsDir returns the sessions directory.
func SessionsDir() string {
	return filepath.Join(configDir, "sessions")
}

// MeetingsDir returns the meeting transcripts directory.
func MeetingsDir() string {
	return filepath.Join(configDir, "meetings")
}

// ConfigDir returns the config directory path.
func ConfigDir() string {
	return configDir
}

// PromptsDir returns the user prompt-documents directory: the configured
// prompts_dir when set, otherwise <config_dir>/prompts.
func PromptsDir() string {
	mu.RLock()
	defer mu.RUnlock()
	if d := v.GetString("prompts_dir"); d != "" {
		return d
	}
	return filepath.Join(configDir, "prompts")
}

// SkillsDir returns the Agent Skills directory: the configured skills_dir when
// set, otherwise <config_dir>/skills.
func SkillsDir() string {
	mu.RLock()
	defer mu.RUnlock()
	if d := v.GetString("skills_dir"); d != "" {
		return d
	}
	return filepath.Join(configDir, "skills")
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}
