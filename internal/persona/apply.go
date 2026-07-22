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
//
// active_persona defaults to "samantha" via viper, so an empty/unset value is
// indistinguishable from that default. When seeding from a legacy non-default
// persona (e.g. persona: festival), the profile id is seed.ID — always point
// active_persona at the profile we just created. When profiles already exist
// but the default active id is missing, heal by selecting a real profile.
func ensureMigrated(cfg *config.Config) error {
	existing, err := List()
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		id := strings.TrimSpace(cfg.ActivePersona)
		if id == "" {
			id = DefaultID
		}
		if hasProfileID(existing, id) {
			cfg.ActivePersona = id
			return nil
		}
		// Heal: viper defaulted active_persona to samantha while migration
		// only created a legacy non-default profile (or samantha was deleted).
		if id == DefaultID {
			cfg.ActivePersona = pickFallbackID(existing)
			return nil
		}
		// Explicit unknown id — leave for Load to error with a clear message.
		cfg.ActivePersona = id
		return nil
	}

	seed := FromConfig(cfg)
	if err := Write(seed, true); err != nil {
		return fmt.Errorf("migrating default persona: %w", err)
	}
	// Always bind active to the seeded profile. seed.ID may be a legacy
	// persona slug (e.g. festival), not the viper default "samantha".
	cfg.ActivePersona = seed.ID
	return nil
}

func hasProfileID(profiles []*Profile, id string) bool {
	for _, p := range profiles {
		if p.ID == id {
			return true
		}
	}
	return false
}

// pickFallbackID prefers the built-in default id, else the first listed profile.
func pickFallbackID(profiles []*Profile) string {
	if len(profiles) == 0 {
		return DefaultID
	}
	for _, p := range profiles {
		if p.ID == DefaultID {
			return DefaultID
		}
	}
	return profiles[0].ID
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
