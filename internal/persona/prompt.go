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
}

// CreateWithOpts is Create with an optional custom system prompt.
func CreateWithOpts(cfg *config.Config, opts CreateOpts) (*Profile, error) {
	p, err := Create(cfg, opts.DisplayName)
	if err != nil {
		return nil, err
	}
	if text := strings.TrimSpace(opts.SystemPrompt); text != "" {
		if err := WriteSystemPrompt(p.ID, text); err != nil {
			return p, err
		}
		p.Prompts.Persona = p.ID
		if strings.TrimSpace(p.Prompts.Turn) == "" || p.Prompts.Turn == DefaultID {
			// Keep turn instruction on the default unless the user later customizes it.
			p.Prompts.Turn = DefaultID
		}
		if err := Write(p, false); err != nil {
			return p, err
		}
	}
	return p, nil
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
// name (user dir first, then embedded default).
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
