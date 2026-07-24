package persona

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/prompts"
)

// CreateOpts configures CreateAndUse / Create with optional system prompt text.
type CreateOpts struct {
	DisplayName  string
	SystemPrompt string // when set, writes prompts/persona/<id>.yaml and points the profile at it
	// Brain / TTS override the globals-clone Create seeds. Empty fields keep
	// the clone, so "(default)" selections still snapshot today's defaults.
	Brain Brain
	TTS   TTS
}

// CreateWithOpts is Create with an optional custom system prompt.
// Create already seeds prompts/persona/<id>.yaml and binds prompts.persona to
// the new id; a non-empty SystemPrompt overwrites that private document.
func CreateWithOpts(cfg *config.Config, opts CreateOpts) (*Profile, error) {
	p, err := Create(cfg, opts.DisplayName)
	if err != nil {
		return nil, err
	}
	if stackOverridden(opts) {
		if provider := strings.TrimSpace(opts.Brain.Provider); provider != "" {
			p.Brain = Brain{Provider: provider, Model: strings.TrimSpace(opts.Brain.Model)}
		} else if model := strings.TrimSpace(opts.Brain.Model); model != "" {
			p.Brain.Model = model // model for the cloned provider
		}
		if provider := strings.TrimSpace(opts.TTS.Provider); provider != "" {
			p.TTS = TTS{Provider: provider, Voice: strings.TrimSpace(opts.TTS.Voice)}
		} else if voice := strings.TrimSpace(opts.TTS.Voice); voice != "" {
			p.TTS.Voice = voice
		}
		if err := Write(p, false); err != nil {
			return p, err
		}
	}
	if text := strings.TrimSpace(opts.SystemPrompt); text != "" {
		if err := WriteSystemPrompt(p.ID, text); err != nil {
			return p, err
		}
		// Create already set Prompts.Persona = id; keep turn empty (shared default).
		p.Prompts.Persona = p.ID
		p.Prompts.Turn = ""
		if err := Write(p, false); err != nil {
			return p, err
		}
	}
	return p, nil
}

// stackOverridden reports whether opts carries any explicit brain/TTS choice.
func stackOverridden(opts CreateOpts) bool {
	return strings.TrimSpace(opts.Brain.Provider) != "" || strings.TrimSpace(opts.Brain.Model) != "" ||
		strings.TrimSpace(opts.TTS.Provider) != "" || strings.TrimSpace(opts.TTS.Voice) != ""
}

// CreateAndUseWithOpts creates a persona (with optional system prompt) and activates it.
func CreateAndUseWithOpts(cfg *config.Config, opts CreateOpts) (*Profile, error) {
	p, err := CreateWithOpts(cfg, opts)
	if err != nil {
		return nil, err
	}
	if err := Use(cfg, p.ID); err != nil {
		return p, err
	}
	return p, nil
}

// UpdateSystemPrompt rewrites the persona prompt document for id and points
// the profile at it. Builtin profiles may be overridden via the user prompts dir.
func UpdateSystemPrompt(id, systemPrompt string) (*Profile, error) {
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	text := strings.TrimSpace(systemPrompt)
	if text == "" {
		return nil, fmt.Errorf("system prompt is required")
	}
	p, err := Load(id)
	if err != nil {
		return nil, err
	}
	if err := WriteSystemPrompt(id, text); err != nil {
		return nil, err
	}
	p.Prompts.Persona = id
	if err := Write(p, false); err != nil {
		return nil, err
	}
	return p, nil
}

// UpdateDisplayName changes the display name on a persona profile.
func UpdateDisplayName(id, displayName string) (*Profile, error) {
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		return nil, fmt.Errorf("display name is required")
	}
	p, err := Load(id)
	if err != nil {
		return nil, err
	}
	p.DisplayName = displayName
	if err := Write(p, false); err != nil {
		return nil, err
	}
	return p, nil
}

// WriteSystemPrompt writes a samantha.prompt.v1 persona document named `name`
// under the user prompts directory (prompts/persona/<name>.yaml).
func WriteSystemPrompt(name, identity string) error {
	if err := ValidateID(name); err != nil {
		return err
	}
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return fmt.Errorf("system prompt is empty")
	}
	// Ensure unresolved placeholders are not introduced accidentally: empty is fine.
	dir := filepath.Join(promptsDir(), string(prompts.KindPersona))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create prompts dir: %w", err)
	}
	doc := prompts.Document{
		Schema: prompts.Schema,
		Prompt: prompts.Prompt{
			Name: name,
			Kind: prompts.KindPersona,
			SystemPrompt: prompts.SystemPrompt{
				Identity: identity,
			},
		},
		Metadata: prompts.Metadata{
			ID:          name + "-user",
			Version:     1,
			Description: "User-authored persona prompt",
		},
	}
	if err := doc.Validate(); err != nil {
		return err
	}
	// Prefer block scalar for multi-line prompts.
	type wire struct {
		Schema string `yaml:"schema"`
		Prompt struct {
			Name         string `yaml:"name"`
			Kind         string `yaml:"kind"`
			SystemPrompt string `yaml:"system_prompt"`
		} `yaml:"prompt"`
		Metadata prompts.Metadata `yaml:"metadata"`
	}
	w := wire{Schema: prompts.Schema, Metadata: doc.Metadata}
	w.Prompt.Name = name
	w.Prompt.Kind = string(prompts.KindPersona)
	w.Prompt.SystemPrompt = identity
	data, err := yaml.Marshal(&w)
	if err != nil {
		return fmt.Errorf("encode prompt: %w", err)
	}
	header := "# yaml-language-server: $schema=samantha.prompt.v1\n"
	path := filepath.Join(dir, name+".yaml")
	if err := os.WriteFile(path, append([]byte(header), data...), 0o644); err != nil {
		return fmt.Errorf("write prompt %s: %w", path, err)
	}
	return nil
}

// LoadSystemPrompt returns the assembled identity text for a persona prompt
// name (user dir first, then embedded default when name matches it).
// A missing name does not silently return another persona's document.
func LoadSystemPrompt(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = DefaultID
	}
	doc, err := prompts.Resolver{UserDir: promptsDir()}.Resolve(prompts.KindPersona, name)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(doc.Assemble()), nil
}

// LoadSystemPromptForProfile returns the system prompt the Personas editor
// should show for p.
//
// Order:
//  1. prompts/persona/<id>.yaml when present (private doc for this profile)
//  2. prompts.persona catalog ref when it still resolves
//
// When the private id document exists but the profile still points at a stale
// ref (e.g. prompts.persona: Uncle_Fu while uncle-fu.yaml is on disk), the
// profile is healed to prompts.persona: <id> so runtime and the editor agree.
// Never substitutes the embedded samantha document for a different id.
func LoadSystemPromptForProfile(p *Profile) (string, error) {
	if p == nil {
		return "", fmt.Errorf("persona profile: nil")
	}
	if id := strings.TrimSpace(p.ID); id != "" {
		if text, err := LoadSystemPrompt(id); err == nil && strings.TrimSpace(text) != "" {
			if strings.TrimSpace(p.Prompts.Persona) != id {
				p.Prompts.Persona = id
				_ = Write(p, false) // best-effort heal; editor still shows the right text
			}
			return text, nil
		}
	}
	ref := strings.TrimSpace(p.Prompts.Persona)
	if ref != "" && ref != p.ID {
		if text, err := LoadSystemPrompt(ref); err == nil && strings.TrimSpace(text) != "" {
			return text, nil
		}
	}
	return "", fmt.Errorf("no system prompt document for persona %q", p.ID)
}

// DefaultSystemPrompt returns the embedded default persona identity (with
// {agent_name} placeholders intact).
func DefaultSystemPrompt() (string, error) {
	doc, err := prompts.Default(prompts.KindPersona)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(doc.Assemble()), nil
}

func promptsDir() string {
	return config.PromptsDir()
}
