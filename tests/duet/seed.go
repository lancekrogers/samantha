package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

// appDir is the config layout samantha resolves under a fresh $HOME
// (config.DefaultConfigDir); the same layout demos/persona-switch.tape seeds.
const appDir = ".obey/agents/voice/festival-voice"

// seedInstance builds a disposable $HOME for one instance: config.yaml,
// persona profile, and the persona prompt document.
func seedInstance(home string, s *Scenario, id string, inst *InstanceSpec) error {
	personaID := slugify(inst.Persona.DisplayName)
	base := filepath.Join(home, appDir)
	for _, dir := range []string{
		filepath.Join(base, "personas", personaID),
		filepath.Join(base, "prompts", "persona"),
		filepath.Join(base, "sessions"),
		filepath.Join(base, "logs"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	// Tools and skills both seed explicitly: ollama auto-enables them when
	// the keys are absent (applyOllamaDefaults), and a duet run must be
	// exactly what the scenario declares. models_dir points at the real
	// user cache — the default is $HOME-anchored and HOME is disposable
	// here, so TTS/STT scenarios would otherwise re-download models per run.
	cfg := map[string]any{
		"agent_name":          inst.Persona.DisplayName,
		"active_persona":      personaID,
		"persona":             personaID,
		"voice_tools_enabled": inst.Tools,
		"skills_enabled":      inst.Tools,
		"models_dir":          config.DefaultModelsDir(),
	}
	if inst.Brain.Provider != "" {
		cfg["brain_provider"] = inst.Brain.Provider
	}
	if inst.Brain.Provider == "ollama" {
		host := inst.Brain.Host
		if host == "" {
			host = "http://localhost:11434"
		}
		cfg["ollama_host"] = host
		if inst.Brain.Model != "" {
			cfg["ollama_model"] = inst.Brain.Model
		}
	}
	if inst.TTS.Provider != "" {
		cfg["tts_provider"] = inst.TTS.Provider
	}
	if inst.TTS.Voice != "" {
		cfg["tts_voice"] = inst.TTS.Voice
	}
	if err := writeYAML(filepath.Join(base, "config.yaml"), cfg); err != nil {
		return err
	}

	profile := map[string]any{
		"schema":       "festival-voice.persona.v1",
		"id":           personaID,
		"display_name": inst.Persona.DisplayName,
		"brain": map[string]any{
			"provider": inst.Brain.Provider,
			"model":    inst.Brain.Model,
		},
		"tts": map[string]any{
			"provider": inst.TTS.Provider,
			"voice":    inst.TTS.Voice,
		},
		"prompts": map[string]any{"persona": personaID},
	}
	if err := writeYAML(filepath.Join(base, "personas", personaID, "persona.yaml"), profile); err != nil {
		return err
	}

	identity, err := s.SystemPromptText(inst)
	if err != nil {
		return fmt.Errorf("instance %s: %w", id, err)
	}
	doc := map[string]any{
		"schema": "samantha.prompt.v1",
		"prompt": map[string]any{
			"name":          personaID,
			"kind":          "persona",
			"system_prompt": strings.TrimSpace(identity),
		},
		"metadata": map[string]any{
			"id":          personaID + "-duet",
			"version":     1,
			"description": "duet harness seeded persona prompt",
		},
	}
	return writeYAML(filepath.Join(base, "prompts", "persona", personaID+".yaml"), doc)
}

func writeYAML(path string, v any) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// slugify mirrors persona.Slugify closely enough for seeded ids: lowercase,
// spaces to dashes, drop everything else.
func slugify(s string) string {
	var b strings.Builder
	lastDash := true
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}
