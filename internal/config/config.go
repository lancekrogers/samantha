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
	TTSProvider           string  `mapstructure:"tts_provider"`
	TTSFallbackProvider   string  `mapstructure:"voice_fallback_provider"`
	TTSVoice              string  `mapstructure:"tts_voice"`
	SpeechSpeed           float64 `mapstructure:"speech_speed"`
	QwenTTSBinary         string  `mapstructure:"qwen_tts_binary"`
	QwenTTSModel          string  `mapstructure:"qwen_tts_model"`
	QwenTTSTimeout        int     `mapstructure:"qwen_tts_timeout"`
	QwenTTSMode           string  `mapstructure:"qwen_tts_mode"`
	QwenTTSVoice          string  `mapstructure:"qwen_tts_voice"`
	QwenTTSLanguage       string  `mapstructure:"qwen_tts_language"`
	QwenTTSInstruction    string  `mapstructure:"qwen_tts_instruction"`
	QwenTTSReferenceAudio string  `mapstructure:"qwen_tts_reference_audio"`
	QwenTTSReferenceText  string  `mapstructure:"qwen_tts_reference_text"`
	QwenTTSConsent        bool    `mapstructure:"qwen_tts_consent"`
	OutputDevice          string  `mapstructure:"output_device"`

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
	BrainProvider string `mapstructure:"brain_provider"`
	GrokModel     string `mapstructure:"grok_model"`
	OllamaModel   string `mapstructure:"ollama_model"`
	// OllamaEmbeddingModel routes user prompts to relevant Agent Skills. Empty
	// disables harness-side semantic activation and leaves model-driven
	// read_skill available as a fallback.
	OllamaEmbeddingModel      string  `mapstructure:"ollama_embedding_model"`
	SkillsSimilarityThreshold float64 `mapstructure:"skills_similarity_threshold"`
	OllamaHost                string  `mapstructure:"ollama_host"`
	VoiceToolsEnabled         bool    `mapstructure:"voice_tools_enabled"`
	// ToolCommandTimeout bounds one local run_command invocation in seconds.
	// The brain turn timeout remains the outer bound for a complete turn.
	ToolCommandTimeout int `mapstructure:"tool_command_timeout"`
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

	// ActivePersona is the persona profile id under personas/<id>/.
	// On Load it drives agent_name, persona prompt name, and tts_voice overlays.
	ActivePersona string `mapstructure:"active_persona"`

	// Prompts
	Persona    string `mapstructure:"persona"`
	PromptsDir string `mapstructure:"prompts_dir"`

	// Skills (Agent Skills / SKILL.md). Ollama loads the catalog by default when
	// SkillsEnabled is not explicitly disabled. See internal/skills.
	SkillsEnabled bool   `mapstructure:"skills_enabled"`
	SkillsDir     string `mapstructure:"skills_dir"`

	// General
	Language        string `mapstructure:"language"`
	MaxHistory      int    `mapstructure:"max_history"`
	ListenTimeout   int    `mapstructure:"listen_timeout"`
	PhraseTimeLimit int    `mapstructure:"phrase_time_limit"`

	// Meeting notes routing (post-record export to campaign/file/Apple Notes).
	Meeting MeetingConfig `mapstructure:"meeting"`

	// Speaker analysis (optional; diarization + embeddings via sherpa).
	// Nested under "speaker" — see internal/speaker.Config.
	Speaker SpeakerConfig `mapstructure:"speaker"`
}

// MeetingConfig holds meeting storage + routing preferences.
type MeetingConfig struct {
	// Dir overrides the default meetings directory when non-empty.
	Dir   string             `mapstructure:"dir"`
	Route MeetingRouteConfig `mapstructure:"route"`
}

// MeetingRouteConfig controls post-meeting note routing.
type MeetingRouteConfig struct {
	// Mode is ask | auto | off (default ask).
	Mode string `mapstructure:"mode"`
	// Default is the destination ID used by auto and preselected by ask.
	Default string `mapstructure:"default"`
	// Body is notes | full (default notes).
	Body string `mapstructure:"body"`
	// Destinations are named route targets (manage in YAML for v1).
	Destinations []MeetingDestinationConfig `mapstructure:"destinations"`
}

// MeetingDestinationConfig is one named export target.
type MeetingDestinationConfig struct {
	ID       string   `mapstructure:"id"`
	Type     string   `mapstructure:"type"` // campaign | file | apple-notes
	Campaign string   `mapstructure:"campaign"`
	Capture  string   `mapstructure:"capture"` // intent | note
	Tags     []string `mapstructure:"tags"`
	Path     string   `mapstructure:"path"`
	Folder   string   `mapstructure:"folder"`
}

// SpeakerConfig mirrors speaker.Config for viper unmarshal (keeps config package
// free of importing speaker for package cycles).
type SpeakerConfig struct {
	Enabled       bool                 `mapstructure:"enabled"`
	Threshold     float32              `mapstructure:"threshold"`
	EnrollmentDir string               `mapstructure:"enrollment_dir"`
	Live          SpeakerLiveConfig    `mapstructure:"live"`
	Meeting       SpeakerMeetingConfig `mapstructure:"meeting"`
	Models        SpeakerModelsConfig  `mapstructure:"models"`
}

// SpeakerLiveConfig is the async conversation speaker path.
type SpeakerLiveConfig struct {
	Enabled   bool    `mapstructure:"enabled"`
	Mode      string  `mapstructure:"mode"`
	Threshold float32 `mapstructure:"threshold"`
	WindowMS  int     `mapstructure:"window_ms"`
}

// SpeakerMeetingConfig is offline meeting diarization (not notes routing).
type SpeakerMeetingConfig struct {
	Enabled     bool `mapstructure:"enabled"`
	RecordAudio bool `mapstructure:"record_audio"`
	NumSpeakers int  `mapstructure:"num_speakers"`
}

// SpeakerModelsConfig holds optional model paths under models_dir.
type SpeakerModelsConfig struct {
	Embedding    string `mapstructure:"embedding"`
	Segmentation string `mapstructure:"segmentation"`
}

var (
	configDir  = DefaultConfigDir()
	configFile = filepath.Join(configDir, "config.yaml")
	v          *viper.Viper
	// mu guards v: the TUI reads config from tea.Cmd goroutines while the
	// Update goroutine writes via SetAndSave.
	//
	// mu does NOT guard the fields of a *Config value returned by Load — that
	// struct is plain data with no internal synchronization. A *Config must not
	// be shared by pointer between a goroutine that mutates its fields (e.g. the
	// TUI Update loop) and one that reads them live (a pipeline/brain turn).
	// Hand concurrent readers an independent snapshot instead: a fresh Load, or a
	// value copy (next := *cfg) as serve/meeting/benchmark do before overriding a
	// field. The TUI honors this via conversationRuntimeBuilder's per-conversation
	// Load; see the App.cfg invariant in internal/tui/tui.go.
	mu sync.RWMutex
)

func init() {
	v = viper.New()
	setDefaults(v)
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("tts_provider", "kokoro")
	v.SetDefault("voice_fallback_provider", "kokoro")
	v.SetDefault("tts_voice", "af_heart")
	v.SetDefault("speech_speed", 0.95)
	// Empty selects Samantha's managed Qwen runtime. Set an explicit binary and
	// model path only for the advanced external-worker compatibility path.
	v.SetDefault("qwen_tts_binary", "")
	v.SetDefault("qwen_tts_model", "")
	v.SetDefault("qwen_tts_timeout", 120)
	v.SetDefault("qwen_tts_mode", "")
	v.SetDefault("qwen_tts_voice", "")
	v.SetDefault("qwen_tts_language", "")
	v.SetDefault("qwen_tts_instruction", "")
	v.SetDefault("qwen_tts_reference_audio", "")
	v.SetDefault("qwen_tts_reference_text", "")
	v.SetDefault("qwen_tts_consent", false)
	v.SetDefault("output_device", "")

	v.SetDefault("stt_provider", "sherpa")
	v.SetDefault("input_device", "")
	v.SetDefault("stt_mode", "")
	v.SetDefault("sherpa_streaming_model", "en-2023-06-26")
	v.SetDefault("whisper_model", "small")
	v.SetDefault("whisper_quantized", true)
	v.SetDefault("whispercpp_binary", "whisper-cli")
	v.SetDefault("whispercpp_model", "base.en")
	v.SetDefault("whispercpp_model_path", filepath.Join(DefaultModelsDir(), "whispercpp", "ggml-base.en.bin"))

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
	v.SetDefault("ollama_embedding_model", "nomic-embed-text")
	v.SetDefault("skills_similarity_threshold", 0.55)
	v.SetDefault("ollama_host", "http://localhost:11434")
	v.SetDefault("voice_tools_enabled", false)
	v.SetDefault("tool_command_timeout", 30)
	v.SetDefault("remote_tools_enabled", false)

	// Calibre is opt-in. Empty binaries are resolved by bundle-aware LookPath.
	v.SetDefault("calibre_enabled", false)
	v.SetDefault("calibre_library_path", "")
	v.SetDefault("calibredb_binary", "")
	v.SetDefault("calibre_convert_binary", "")
	v.SetDefault("calibre_prefer_format", "epub")

	v.SetDefault("agent_name", "Samantha")
	v.SetDefault("active_persona", "samantha")
	v.SetDefault("persona", "samantha")
	v.SetDefault("prompts_dir", "")
	v.SetDefault("skills_enabled", false)
	v.SetDefault("skills_dir", "")
	v.SetDefault("models_dir", DefaultModelsDir())

	v.SetDefault("language", "en-US")
	v.SetDefault("max_history", 10)
	v.SetDefault("listen_timeout", 10)
	v.SetDefault("phrase_time_limit", 30)

	// Meeting notes routing defaults.
	v.SetDefault("meeting.dir", "")
	v.SetDefault("meeting.route.mode", "ask")
	v.SetDefault("meeting.route.default", "")
	// Embed transcript by default so routed intents are self-contained.
	v.SetDefault("meeting.route.body", "full")
	v.SetDefault("meeting.route.destinations", []any{})

	// Speaker analysis off by default.
	v.SetDefault("speaker.enabled", false)
	v.SetDefault("speaker.threshold", 0.6)
	v.SetDefault("speaker.enrollment_dir", "")
	v.SetDefault("speaker.live.enabled", false)
	v.SetDefault("speaker.live.mode", "indicator")
	v.SetDefault("speaker.live.threshold", 0.0) // 0 → inherit speaker.threshold
	v.SetDefault("speaker.live.window_ms", 1500)
	v.SetDefault("speaker.meeting.enabled", false)
	v.SetDefault("speaker.meeting.record_audio", false)
	v.SetDefault("speaker.meeting.num_speakers", 0)
	v.SetDefault("speaker.models.embedding", "")
	v.SetDefault("speaker.models.segmentation", "")
}

// afterLoad is optional post-processing (persona ensure/apply). Set via
// SetAfterLoad; persona package registers itself from init.
var afterLoad func(*Config) error

// SetAfterLoad registers a hook invoked at the end of Load (after unlock).
// Used by the persona package to migrate/apply active persona profiles.
func SetAfterLoad(fn func(*Config) error) {
	afterLoad = fn
}

// Load reads configuration from disk and environment.
func Load() (*Config, error) {
	cfg, err := loadLocked()
	if err != nil {
		return nil, err
	}
	if afterLoad != nil {
		if err := afterLoad(cfg); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

// loadLocked reads and unmarshals config while holding mu.
func loadLocked() (*Config, error) {
	mu.Lock()
	defer mu.Unlock()

	v.SetConfigFile(configFile)
	v.SetConfigType("yaml")

	// Explicit env bindings only — no AutomaticEnv, which would bind every
	// key to its bare upper-cased name and let unrelated vars like the
	// standard LANGUAGE leak into (and get persisted over) config values.
	bindings := map[string]string{
		"tts_provider":             "TTS_PROVIDER",
		"voice_fallback_provider":  "VOICE_FALLBACK_PROVIDER",
		"tts_voice":                "TTS_VOICE",
		"qwen_tts_binary":          "QWEN_TTS_BINARY",
		"qwen_tts_model":           "QWEN_TTS_MODEL",
		"qwen_tts_timeout":         "QWEN_TTS_TIMEOUT",
		"qwen_tts_mode":            "QWEN_TTS_MODE",
		"qwen_tts_voice":           "QWEN_TTS_VOICE",
		"qwen_tts_language":        "QWEN_TTS_LANGUAGE",
		"qwen_tts_instruction":     "QWEN_TTS_INSTRUCTION",
		"qwen_tts_reference_audio": "QWEN_TTS_REFERENCE_AUDIO",
		"qwen_tts_reference_text":  "QWEN_TTS_REFERENCE_TEXT",
		"qwen_tts_consent":         "QWEN_TTS_CONSENT",
		"output_device":            "OUTPUT_DEVICE",
		"stt_provider":             "STT_PROVIDER",
		"input_device":             "INPUT_DEVICE",
		"stt_mode":                 "STT_MODE",
		"sherpa_streaming_model":   "SHERPA_STREAMING_MODEL",
		"whisper_model":            "WHISPER_MODEL",
		"whispercpp_binary":        "WHISPERCPP_BINARY",
		"whispercpp_model":         "WHISPERCPP_MODEL",
		"whispercpp_model_path":    "WHISPERCPP_MODEL_PATH",
		"models_dir":               "MODELS_DIR",
		"brain_provider":           "BRAIN_PROVIDER",
		"grok_model":               "GROK_MODEL",
		"ollama_model":             "OLLAMA_MODEL",
		"ollama_embedding_model":   "OLLAMA_EMBEDDING_MODEL",
		"ollama_host":              "OLLAMA_HOST",
		"voice_tools_enabled":      "VOICE_TOOLS_ENABLED",
		"tool_command_timeout":     "TOOL_COMMAND_TIMEOUT",
		"persona":                  "PERSONA",
		"active_persona":           "ACTIVE_PERSONA",
		"prompts_dir":              "PROMPTS_DIR",
		"skills_enabled":           "SKILLS_ENABLED",
		"skills_dir":               "SKILLS_DIR",
		"barge_in_enabled":         "BARGE_IN_ENABLED",
		"vad_threshold":            "VAD_THRESHOLD",
		"vad_min_speech_duration":  "VAD_MIN_SPEECH_DURATION",
		"voice_frontend_enabled":   "VOICE_FRONTEND_ENABLED",
		"calibre_enabled":          "CALIBRE_ENABLED",
		"calibre_library_path":     "CALIBRE_LIBRARY_PATH",
		"calibredb_binary":         "CALIBREDB_BINARY",
		"calibre_convert_binary":   "CALIBRE_CONVERT_BINARY",
		"calibre_prefer_format":    "CALIBRE_PREFER_FORMAT",
	}
	for key, env := range bindings {
		_ = v.BindEnv(key, env)
	}
	_ = v.BindEnv("skills_similarity_threshold", "SKILLS_SIMILARITY_THRESHOLD")

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
	applyOllamaDefaults(&cfg, v)
	cfg.ToolCommandTimeout = ClampToolCommandTimeout(cfg.ToolCommandTimeout)
	return &cfg, nil
}

// applyOllamaDefaults enables local Ollama capabilities when the user has not
// explicitly configured them. An explicit false still wins, and remote serve
// remains default-deny via remote_tools_enabled.
//
// Skills auto-on discovers project/user SKILL.md into the Ollama system prompt
// when tools are also available; allowed-tools only constrain tools *after*
// activation. Pre-activation base tools remain full when voice_tools_enabled.
func applyOllamaDefaults(cfg *Config, v *viper.Viper) {
	if !strings.EqualFold(strings.TrimSpace(cfg.BrainProvider), "ollama") {
		return
	}

	if os.Getenv("VOICE_TOOLS_ENABLED") == "" && !v.InConfig("voice_tools_enabled") {
		// Ollama's tools (list/read/write/run_command) are the only path to
		// file I/O for local models; leaving them off makes tool-capable models
		// look broken.
		cfg.VoiceToolsEnabled = true
	}

	if os.Getenv("SKILLS_ENABLED") == "" && !v.InConfig("skills_enabled") {
		// Skills are instruction catalogs loaded into the prompt. Tool power is
		// still gated by voice_tools_enabled; allow-lists apply only after
		// read_skill activation.
		cfg.SkillsEnabled = true
	}
}

// ApplyOllamaDefaults re-applies Ollama auto-enable rules to cfg using the
// live viper state (explicit config/env still win). Call after switching
// brain_provider so Settings display matches the next conversation Load().
func ApplyOllamaDefaults(cfg *Config) {
	if cfg == nil {
		return
	}
	mu.RLock()
	defer mu.RUnlock()
	applyOllamaDefaults(cfg, v)
}

// SetAndSaveBrainProvider changes the brain provider and persists any
// provider defaults that were applied to cfg. This keeps the next Load in
// sync with the live TUI state when switching to Ollama: Viper defaults are
// written to config.yaml by WriteConfigAs, so auto-enabled capabilities must
// be set before saving. Explicit config and environment values still win.
func SetAndSaveBrainProvider(cfg *Config, provider string) error {
	if cfg == nil {
		return fmt.Errorf("config must not be nil")
	}

	mu.Lock()
	defer mu.Unlock()

	next := *cfg
	next.BrainProvider = provider
	applyOllamaDefaults(&next, v)

	v.Set("brain_provider", provider)
	if strings.EqualFold(strings.TrimSpace(provider), "ollama") {
		// Full config dumps often bake voice_tools_enabled: false into yaml as a
		// written default. That makes InConfig true and leaves Ollama tool-less.
		// Switching TO Ollama in Settings should turn tools/skills on unless the
		// environment explicitly forces them off (VOICE_TOOLS_ENABLED=false).
		if !envForcesFalse("VOICE_TOOLS_ENABLED") {
			v.Set("voice_tools_enabled", true)
			next.VoiceToolsEnabled = true
		}
		if !envForcesFalse("SKILLS_ENABLED") {
			v.Set("skills_enabled", true)
			next.SkillsEnabled = true
		}
	}

	if err := save(); err != nil {
		return err
	}
	*cfg = next
	return nil
}

// envForcesFalse reports whether env is an explicit false-ish value.
func envForcesFalse(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "0", "false", "no", "off":
		return true
	default:
		return false
	}
}

// ClampToolCommandTimeout bounds tool_command_timeout to 1–120 seconds.
// Zero/negative becomes the default 30.
func ClampToolCommandTimeout(seconds int) int {
	const (
		def = 30
		min = 1
		max = 120
	)
	if seconds <= 0 {
		return def
	}
	if seconds < min {
		return min
	}
	if seconds > max {
		return max
	}
	return seconds
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

// ModelsDirFrom resolves the model cache for an already-loaded Config without
// consulting mutable global viper state. Tests and TUI setup commands use this
// so an explicit models_dir remains isolated.
func ModelsDirFrom(cfg *Config) string {
	if cfg != nil && strings.TrimSpace(cfg.ModelsDir) != "" {
		return strings.TrimSpace(cfg.ModelsDir)
	}
	return ModelsDir()
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
// Honors meeting.dir when set; otherwise <configDir>/meetings.
func MeetingsDir() string {
	mu.RLock()
	defer mu.RUnlock()
	if d := strings.TrimSpace(v.GetString("meeting.dir")); d != "" {
		return expandHomePath(d)
	}
	return filepath.Join(configDir, "meetings")
}

// MeetingsDirFrom returns the meetings directory for an already-loaded Config
// without reading the global viper state (useful for tests and one-shot routes).
func MeetingsDirFrom(cfg *Config) string {
	if cfg != nil {
		if d := strings.TrimSpace(cfg.Meeting.Dir); d != "" {
			return expandHomePath(d)
		}
	}
	return filepath.Join(configDir, "meetings")
}

func expandHomePath(path string) string {
	if path == "~" {
		return homeDir()
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(homeDir(), path[2:])
	}
	return path
}

// ConfigDir returns the config directory path.
func ConfigDir() string {
	return configDir
}

// SetConfigDirForTest redirects ConfigDir (and the default config file path)
// for the duration of t. Used by persona and other packages that write under
// the install root.
func SetConfigDirForTest(t interface {
	Helper()
	Cleanup(func())
}, dir string) {
	t.Helper()
	origDir, origFile := configDir, configFile
	configDir = dir
	configFile = filepath.Join(dir, "config.yaml")
	t.Cleanup(func() {
		configDir, configFile = origDir, origFile
	})
}

// PersonasDir returns <configDir>/personas.
func PersonasDir() string {
	return filepath.Join(configDir, "personas")
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
