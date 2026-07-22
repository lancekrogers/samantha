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

	"github.com/lancekrogers/samantha/internal/config"
)

// Schema identifies the persona profile document version.
const Schema = "festival-voice.persona.v1"

// DefaultID is the built-in samantha profile id.
const DefaultID = "samantha"

var idPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// Profile is one voice agent persona (not the system-prompt body).
type Profile struct {
	Schema      string         `yaml:"schema"`
	ID          string         `yaml:"id"`
	DisplayName string         `yaml:"display_name"`
	Builtin     bool           `yaml:"builtin,omitempty"`
	TTS         TTS            `yaml:"tts"`
	Prompts     PromptRefs     `yaml:"prompts"`
	Path        string         `yaml:"-"` // absolute path of persona.yaml when loaded
}

// TTS holds voice settings for a persona.
type TTS struct {
	Voice string `yaml:"voice,omitempty"`
	// Provider is reserved for P4+ (optional override).
	Provider string `yaml:"provider,omitempty"`
}

// PromptRefs names documents in the prompts catalog.
type PromptRefs struct {
	Persona string `yaml:"persona"`
	Turn    string `yaml:"turn,omitempty"`
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
	return nil
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
	voice := strings.TrimSpace(cfg.TTSVoice)
	return &Profile{
		Schema:      Schema,
		ID:          id,
		DisplayName: display,
		Builtin:     id == DefaultID,
		TTS:         TTS{Voice: voice},
		Prompts: PromptRefs{
			Persona: promptName,
			Turn:    promptName,
		},
	}
}

// Apply overlays profile fields onto cfg (non-empty profile fields win).
func Apply(cfg *config.Config, p *Profile) {
	if cfg == nil || p == nil {
		return
	}
	if name := strings.TrimSpace(p.DisplayName); name != "" {
		cfg.AgentName = name
	}
	if ref := strings.TrimSpace(p.Prompts.Persona); ref != "" {
		cfg.Persona = ref
	}
	if voice := strings.TrimSpace(p.TTS.Voice); voice != "" {
		cfg.TTSVoice = voice
	}
	if id := strings.TrimSpace(p.ID); id != "" {
		cfg.ActivePersona = id
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
