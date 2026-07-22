package persona

import (
	"fmt"
	"os"
	"strings"

	"github.com/lancekrogers/samantha/internal/config"
)

func init() {
	config.SetAfterLoad(EnsureAndApply)
}

// EnsureAndApply migrates a single-agent install when needed, then overlays
// the active persona profile onto cfg. Safe to call on every Load.
func EnsureAndApply(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("persona: config is nil")
	}

	if err := ensureMigrated(cfg); err != nil {
		return err
	}

	id := ActiveID(cfg)
	if err := ValidateID(id); err != nil {
		return fmt.Errorf("active_persona: %w", err)
	}

	p, err := Load(id)
	if err != nil {
		if os.IsNotExist(err) || isNotExist(err) {
			available, _ := List()
			var ids []string
			for _, x := range available {
				ids = append(ids, x.ID)
			}
			if len(ids) == 0 {
				return fmt.Errorf("active_persona %q not found (no personas under %s)", id, Dir())
			}
			return fmt.Errorf("active_persona %q not found (available: %s)", id, strings.Join(ids, ", "))
		}
		return err
	}

	Apply(cfg, p)
	return nil
}

func isNotExist(err error) bool {
	if err == nil {
		return false
	}
	// LoadFile wraps os.ErrNotExist.
	return os.IsNotExist(err) || strings.Contains(err.Error(), "no such file")
}

// ensureMigrated creates personas/<id>/persona.yaml from legacy config keys
// when the personas directory has no valid profiles yet.
func ensureMigrated(cfg *config.Config) error {
	existing, err := List()
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		// Still default active_persona when unset.
		if strings.TrimSpace(cfg.ActivePersona) == "" {
			cfg.ActivePersona = DefaultID
			// Prefer existing default id if present.
			for _, p := range existing {
				if p.ID == DefaultID {
					cfg.ActivePersona = DefaultID
					return nil
				}
			}
			cfg.ActivePersona = existing[0].ID
		}
		return nil
	}

	seed := FromConfig(cfg)
	if err := Write(seed, true); err != nil {
		return fmt.Errorf("migrating default persona: %w", err)
	}
	if strings.TrimSpace(cfg.ActivePersona) == "" {
		cfg.ActivePersona = seed.ID
	}
	return nil
}

// Use sets active_persona, persists it, and applies the profile to cfg.
func Use(cfg *config.Config, id string) error {
	if cfg == nil {
		return fmt.Errorf("persona: config is nil")
	}
	if err := ValidateID(id); err != nil {
		return err
	}
	p, err := Load(id)
	if err != nil {
		return err
	}
	if err := config.SetAndSave("active_persona", id); err != nil {
		return fmt.Errorf("saving active_persona: %w", err)
	}
	// Keep legacy keys in sync so older tools and config display match.
	_ = config.SetAndSave("agent_name", p.DisplayName)
	if ref := strings.TrimSpace(p.Prompts.Persona); ref != "" {
		_ = config.SetAndSave("persona", ref)
	}
	if voice := strings.TrimSpace(p.TTS.Voice); voice != "" {
		_ = config.SetAndSave("tts_voice", voice)
	}
	Apply(cfg, p)
	return nil
}
