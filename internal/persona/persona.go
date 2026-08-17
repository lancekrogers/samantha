// Package persona loads festival-voice persona profiles (multi-agent support).
//
// Profiles live at:
//
//	<ConfigDir>/personas/<id>/persona.yaml
//
// Prompt bodies remain in the existing prompts catalog; profiles only
// reference them and carry display name + TTS voice.
package persona

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	"github.com/lancekrogers/samantha/pkg/voiceagent/prompts"
	managedqwen "github.com/lancekrogers/samantha/pkg/voiceagent/qwen"
)

// Schema identifies the persona profile document version.
const Schema = "festival-voice.persona.v1"

// DefaultID is the built-in samantha profile id.
const DefaultID = "samantha"

var idPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// Profile is one voice agent persona (not the system-prompt body).
//
// The json tags mirror the yaml paths so every machine-readable surface
// (persona show/edit/create --json, GET /v1/personas) speaks the same
// snake_case as persona list --json. Without them encoding/json emits Go
// field names, which no client should have to know. They deliberately carry
// no omitempty: a stable key set lets a decoder distinguish "inherit the app
// default" (empty string) from "field absent".
type Profile struct {
	Schema      string     `yaml:"schema" json:"schema"`
	ID          string     `yaml:"id" json:"id"`
	DisplayName string     `yaml:"display_name" json:"display_name"`
	Builtin     bool       `yaml:"builtin,omitempty" json:"builtin"`
	Brain       Brain      `yaml:"brain,omitempty" json:"brain"`
	TTS         TTS        `yaml:"tts" json:"tts"`
	Prompts     PromptRefs `yaml:"prompts" json:"prompts"`
	Path        string     `yaml:"-" json:"path"` // absolute path of persona.yaml when loaded
}

// Brain holds per-persona model routing.
//
//	brain:
//	  provider: ollama          # or claude, grok; empty = app default
//	  model: qwen2.5:14b        # provider-specific model id; empty = app default
//
// Profiles written before this field existed have the zero value and keep
// using the app-level brain keys until edited.
type Brain struct {
	Provider string `yaml:"provider,omitempty" json:"provider"`
	Model    string `yaml:"model,omitempty" json:"model"`
}

// TTS holds per-persona speech settings. Each persona may choose any supported
// TTS provider and a voice id valid for that provider.
//
//	tts:
//	  provider: kokoro      # or qwen3-tts, …
//	  voice: af_heart       # kokoro voice id, or Qwen preset (e.g. Vivian)
//	  tier: 1.7b            # qwen3-tts only: native model tier (0.6b / 1.7b)
//
// Empty provider leaves the app-level tts_provider unchanged and routes voice
// using the effective provider after Apply. Empty voice leaves voice keys alone.
type TTS struct {
	// Provider is the TTS backend (e.g. kokoro, qwen3-tts). Empty = keep app default.
	Provider string `yaml:"provider,omitempty" json:"provider"`
	// Voice is the speaker id for Provider (Kokoro voice name or Qwen preset).
	Voice string `yaml:"voice,omitempty" json:"voice"`
	// Tier selects the native Qwen3-TTS model tier for this persona (0.6b for
	// latency, 1.7b for quality). Empty inherits the app-level
	// qwen_tts_model_tier; only qwen3-tts reads it.
	Tier string `yaml:"tier,omitempty" json:"tier"`
}

// PromptRefs names documents in the prompts catalog.
//
//	persona: catalog name of the kind=persona system prompt (usually this
//	         profile's id; private file at prompts/persona/<id>.yaml).
//	turn:    optional catalog name of the kind=turn per-reply voice instruction
//	         appended on Claude/Grok paths. Empty = shared embedded default.
//	         This is NOT a conversation-turn counter and is independent of the
//	         TTS voice id (e.g. Uncle_Fu).
type PromptRefs struct {
	Persona string `yaml:"persona" json:"persona"`
	Turn    string `yaml:"turn,omitempty" json:"turn"`
}

// Validate checks structural invariants.
func (p *Profile) Validate() error {
	if p == nil {
		return fmt.Errorf("persona profile: nil")
	}
	if p.Schema != Schema {
		return fmt.Errorf("persona profile: schema %q, want %q", p.Schema, Schema)
	}
	if err := ValidateID(p.ID); err != nil {
		return err
	}
	if strings.TrimSpace(p.DisplayName) == "" {
		return fmt.Errorf("persona profile %q: missing display_name", p.ID)
	}
	if strings.TrimSpace(p.Prompts.Persona) == "" {
		return fmt.Errorf("persona profile %q: missing prompts.persona", p.ID)
	}
	if err := validateTier(p.TTS.Tier); err != nil {
		return fmt.Errorf("persona profile %q: %w", p.ID, err)
	}
	return nil
}

// canonicalTier normalizes a non-empty tier to its canonical spelling on
// persist, so the profile on disk matches the catalog ("1.7" never sits
// beside "1.7b" in the editor).
func canonicalTier(tier string) string {
	if strings.TrimSpace(tier) == "" {
		return ""
	}
	return managedqwen.NormalizeModelTier(tier)
}

// validateTier accepts an empty tier (inherit the app default) or any spelling
// that normalizes to a known native Qwen3-TTS tier.
func validateTier(tier string) error {
	if strings.TrimSpace(tier) == "" {
		return nil
	}
	switch managedqwen.NormalizeModelTier(tier) {
	case managedqwen.DefaultModelTier, managedqwen.Tier1_7B:
		return nil
	}
	return fmt.Errorf("tts.tier %q: unknown tier (use 0.6b or 1.7b)", tier)
}

// ValidateTier reports whether tier is an accepted Qwen3-TTS tier spelling.
// Exported so callers can reject a bad --tier before any write lands, rather
// than discovering it inside Write's validation halfway through an edit.
func ValidateTier(tier string) error {
	return validateTier(tier)
}

// ValidateID enforces lowercase kebab-case persona ids.
func ValidateID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("persona id: empty")
	}
	if !idPattern.MatchString(id) {
		return fmt.Errorf("persona id %q: must be lowercase kebab-case [a-z0-9-]+", id)
	}
	return nil
}

// Dir returns the personas root under the install config dir.
func Dir() string {
	return filepath.Join(config.ConfigDir(), "personas")
}

// ProfilePath returns the persona.yaml path for id.
func ProfilePath(id string) string {
	return filepath.Join(Dir(), id, "persona.yaml")
}

// LoadFile reads and validates a persona profile from path.
func LoadFile(path string) (*Profile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading persona profile %s: %w", path, err)
	}
	var p Profile
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parsing persona profile %s: %w", path, err)
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	// Directory name is authoritative when it disagrees (defensive).
	dirID := filepath.Base(filepath.Dir(path))
	if dirID != "" && dirID != "." && dirID != p.ID {
		return nil, fmt.Errorf("persona profile %s: id %q does not match directory %q", path, p.ID, dirID)
	}
	p.Path = path
	return &p, nil
}

// Load loads the profile for id from the personas directory.
func Load(id string) (*Profile, error) {
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	return LoadFile(ProfilePath(id))
}

// List returns profiles found under personas/, sorted by id.
func List() ([]*Profile, error) {
	root := Dir()
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing personas: %w", err)
	}
	var out []*Profile
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		if err := ValidateID(id); err != nil {
			continue
		}
		p, err := Load(id)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// Write writes profile to personas/<id>/persona.yaml. It does not overwrite
// when skipExisting is true and the file already exists.
func Write(p *Profile, skipExisting bool) error {
	if err := p.Validate(); err != nil {
		return err
	}
	path := ProfilePath(p.ID)
	if skipExisting {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("checking %s: %w", path, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating persona dir: %w", err)
	}
	// Encode without Path field (yaml:"-").
	data, err := yaml.Marshal(p)
	if err != nil {
		return fmt.Errorf("encoding persona profile: %w", err)
	}
	// Prefer a stable header for humans.
	header := "# yaml-language-server: $schema=festival-voice.persona.v1\n"
	body := append([]byte(header), data...)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	p.Path = path
	return nil
}

// FromConfig builds a samantha (or slug) profile from legacy single-agent keys.
func FromConfig(cfg *config.Config) *Profile {
	if cfg == nil {
		cfg = &config.Config{}
	}
	id := DefaultID
	promptName := strings.TrimSpace(cfg.Persona)
	if promptName == "" {
		promptName = DefaultID
	}
	// Prefer a valid persona prompt name as the profile id when it is a slug.
	if err := ValidateID(promptName); err == nil {
		id = promptName
	}
	display := strings.TrimSpace(cfg.AgentName)
	if display == "" {
		display = "Samantha"
	}
	provider := strings.TrimSpace(cfg.TTSProvider)
	voice := voiceForProvider(cfg, provider)
	return &Profile{
		Schema:      Schema,
		ID:          id,
		DisplayName: display,
		Builtin:     id == DefaultID,
		TTS: TTS{
			Provider: provider,
			Voice:    voice,
		},
		Prompts: PromptRefs{
			Persona: promptName,
			// Empty turn = shared embedded turn instruction.
		},
	}
}

// Apply overlays profile fields onto cfg (non-empty profile fields win).
// TTS provider and voice are per-persona: users may pick any supported backend
// and any voice valid for that backend on each profile.
func Apply(cfg *config.Config, p *Profile) {
	if cfg == nil || p == nil {
		return
	}
	if name := strings.TrimSpace(p.DisplayName); name != "" {
		cfg.AgentName = name
	}
	if ref := effectivePersonaRef(p); ref != "" {
		cfg.Persona = ref
	}
	// Turn prompt is independent of the persona system-prompt name. Empty
	// means the brain uses the shared embedded turn instruction.
	cfg.TurnPrompt = strings.TrimSpace(p.Prompts.Turn)
	if id := strings.TrimSpace(p.ID); id != "" {
		cfg.ActivePersona = id
	}
	applyBrain(cfg, p.Brain)
	applyTTS(cfg, p.TTS)
}

// effectivePersonaRef is the kind=persona catalog name the brain should resolve
// for p. The private prompts/persona/<id>.yaml wins over prompts.persona so a
// stale ref (the real Uncle_Fu case: prompts.persona held the TTS voice id)
// cannot hard-fail brain construction now that the resolver no longer falls back
// to the embedded samantha document. The editor heals the file on disk; this
// keeps the running process working before anyone opens it.
func effectivePersonaRef(p *Profile) string {
	ref := strings.TrimSpace(p.Prompts.Persona)
	id := strings.TrimSpace(p.ID)
	if id == "" || ref == id {
		return ref
	}
	// Only a real user document at prompts/persona/<id>.yaml wins. Checking via
	// LoadSystemPrompt would also match the embedded fallback, which would
	// override a deliberate shared ref on the samantha profile.
	entry, err := prompts.Describe(promptsDir(), prompts.KindPersona, id)
	if err != nil || entry.Source != prompts.SourceUser {
		return ref
	}
	return id
}

// applyBrain overlays per-persona model routing. Empty fields inherit the
// app-level keys, so pre-Brain profiles behave exactly as before.
func applyBrain(cfg *config.Config, b Brain) {
	provider := strings.TrimSpace(b.Provider)
	if provider != "" {
		cfg.BrainProvider = provider
		// Provider flips change what capabilities make sense (tools, skills);
		// re-run the ollama auto-enable rules against the new provider.
		config.ApplyOllamaDefaults(cfg)
	}
	model := strings.TrimSpace(b.Model)
	if model == "" {
		return
	}
	switch strings.ToLower(strings.TrimSpace(cfg.BrainProvider)) {
	case "ollama":
		cfg.OllamaModel = model
	case "grok":
		cfg.GrokModel = model
		// claude has no app-level model key today; a persona model for it is
		// recorded on the profile but not routed.
	}
}

// modelForProvider reads the app-level model key for a brain provider.
func modelForProvider(cfg *config.Config, provider string) string {
	if cfg == nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "ollama":
		return strings.TrimSpace(cfg.OllamaModel)
	case "grok":
		return strings.TrimSpace(cfg.GrokModel)
	default:
		return ""
	}
}

// applyTTS writes provider/voice/tier from the profile onto cfg.
func applyTTS(cfg *config.Config, t TTS) {
	if provider := strings.TrimSpace(t.Provider); provider != "" {
		cfg.TTSProvider = provider
	}
	if voice := strings.TrimSpace(t.Voice); voice != "" {
		// Route voice to the config key the selected provider reads.
		switch normalizeTTSProvider(cfg.TTSProvider) {
		case "qwen3-tts":
			cfg.QwenTTSVoice = voice
		default:
			// kokoro and any other voice-keyed providers use tts_voice.
			cfg.TTSVoice = voice
		}
	}
	// Tier picks the native model inside the multi-tier package; only qwen
	// reads it, and empty inherits the app-level qwen_tts_model_tier.
	if tier := strings.TrimSpace(t.Tier); tier != "" && normalizeTTSProvider(cfg.TTSProvider) == "qwen3-tts" {
		cfg.QwenTTSModelTier = managedqwen.NormalizeModelTier(tier)
	}
}

// voiceForProvider reads the voice field the given provider uses from cfg.
func voiceForProvider(cfg *config.Config, provider string) string {
	if cfg == nil {
		return ""
	}
	switch normalizeTTSProvider(provider) {
	case "qwen3-tts":
		if v := strings.TrimSpace(cfg.QwenTTSVoice); v != "" {
			return v
		}
		// Fall back to kokoro voice if qwen voice unset (legacy single-agent).
		return strings.TrimSpace(cfg.TTSVoice)
	default:
		return strings.TrimSpace(cfg.TTSVoice)
	}
}

func normalizeTTSProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

// PersistTTS writes the profile's TTS selection into the live config file so
// persona use and settings stay aligned.
func PersistTTS(p *Profile) error {
	if p == nil {
		return nil
	}
	if provider := strings.TrimSpace(p.TTS.Provider); provider != "" {
		if err := config.SetAndSave("tts_provider", provider); err != nil {
			return err
		}
	}
	voice := strings.TrimSpace(p.TTS.Voice)
	if voice == "" {
		return nil
	}
	// Need effective provider after optional profile provider.
	provider := strings.TrimSpace(p.TTS.Provider)
	if provider == "" {
		if v, ok := config.Get("tts_provider").(string); ok {
			provider = v
		}
	}
	switch normalizeTTSProvider(provider) {
	case "qwen3-tts":
		return config.SetAndSave("qwen_tts_voice", voice)
	default:
		return config.SetAndSave("tts_voice", voice)
	}
}

// ActiveID returns the configured active persona id, defaulting to samantha.
func ActiveID(cfg *config.Config) string {
	if cfg == nil {
		return DefaultID
	}
	id := strings.TrimSpace(cfg.ActivePersona)
	if id == "" {
		return DefaultID
	}
	return id
}
