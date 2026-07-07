// Package prompts defines the samantha.prompt.v1 YAML document format,
// its loader, deterministic assembly, and embedded defaults. Nothing
// consumes it yet; wiring into the brain happens in a follow-up change.
package prompts

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Schema identifies the prompt document schema version.
const Schema = "samantha.prompt.v1"

// Kind classifies what a prompt document provides.
type Kind string

const (
	KindPersona       Kind = "persona"
	KindSystem        Kind = "system"
	KindStyle         Kind = "style"
	KindPronunciation Kind = "pronunciation"
)

// Document is a versioned prompt document.
type Document struct {
	Schema   string   `yaml:"schema"`
	Prompt   Prompt   `yaml:"prompt"`
	Metadata Metadata `yaml:"metadata"`
}

// Prompt names the document and carries its system prompt content.
type Prompt struct {
	Name         string       `yaml:"name"`
	Kind         Kind         `yaml:"kind"`
	SystemPrompt SystemPrompt `yaml:"system_prompt"`
}

// SystemPrompt is the prompt body. In YAML it is either a plain string
// (used verbatim as the identity) or a mapping with an identity plus
// optional structured sections.
type SystemPrompt struct {
	Identity          string            `yaml:"identity"`
	ConversationStyle []string          `yaml:"conversation_style"`
	Guidance          []string          `yaml:"guidance"`
	Constraints       []string          `yaml:"constraints"`
	CoreConcepts      map[string]string `yaml:"core_concepts"`
}

// Metadata is optional descriptive information about the document.
type Metadata struct {
	ID          string `yaml:"id"`
	Version     int    `yaml:"version"`
	Description string `yaml:"description"`
}

// UnmarshalYAML accepts the string-or-object union: a scalar becomes the
// identity verbatim; a mapping is decoded strictly, rejecting unknown keys.
func (s *SystemPrompt) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		var text string
		if err := node.Decode(&text); err != nil {
			return fmt.Errorf("system_prompt: %w", err)
		}
		s.Identity = text
		return nil
	case yaml.MappingNode:
		for i := 0; i < len(node.Content); i += 2 {
			switch key := node.Content[i].Value; key {
			case "identity", "conversation_style", "guidance", "constraints", "core_concepts":
			default:
				return fmt.Errorf("system_prompt: unknown key %q", key)
			}
		}
		type plain SystemPrompt
		var p plain
		if err := node.Decode(&p); err != nil {
			return fmt.Errorf("system_prompt: %w", err)
		}
		*s = SystemPrompt(p)
		return nil
	default:
		return fmt.Errorf("system_prompt: must be a string or a mapping")
	}
}

// Validate checks the structural invariants of the document: the schema
// string, a non-empty name, a known kind, and a non-empty identity.
func (d *Document) Validate() error {
	if d.Schema != Schema {
		return fmt.Errorf("prompt document: schema %q, want %q", d.Schema, Schema)
	}
	if d.Prompt.Name == "" {
		return fmt.Errorf("prompt document: missing prompt.name")
	}
	switch d.Prompt.Kind {
	case KindPersona, KindSystem, KindStyle, KindPronunciation:
	default:
		return fmt.Errorf("prompt document %q: unknown kind %q (valid: persona, system, style, pronunciation)", d.Prompt.Name, d.Prompt.Kind)
	}
	if strings.TrimSpace(d.Prompt.SystemPrompt.Identity) == "" {
		return fmt.Errorf("prompt document %q: system_prompt missing identity", d.Prompt.Name)
	}
	return nil
}
