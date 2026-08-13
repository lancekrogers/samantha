package persona

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"unicode"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

var nonSlugRunes = regexp.MustCompile(`[^a-z0-9]+`)

// Create builds a new user persona profile under personas/<id>/, cloning TTS
// and brain stack from the current config. Every new persona owns its own
// system-prompt document at prompts/persona/<id>.yaml (seeded from the active
// prompt or the embedded default) so the editor never silently shows another
// persona's identity by shared reference.
//
// prompts.turn is left empty: empty means the shared embedded turn instruction
// (short per-reply voice style), not "this is the samantha persona".
//
// It does not activate the persona; call Use after.
func Create(cfg *config.Config, displayName string) (*Profile, error) {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		return nil, fmt.Errorf("persona name is required")
	}
	id, err := uniqueID(Slugify(displayName))
	if err != nil {
		return nil, err
	}

	base := FromConfig(cfg)
	seed, err := seedSystemPrompt(cfg)
	if err != nil {
		return nil, err
	}

	p := &Profile{
		Schema:      Schema,
		ID:          id,
		DisplayName: displayName,
		Builtin:     false,
		// Clone the current global brain stack at creation time (a snapshot,
		// not a live link): editing global defaults later must not rewrite
		// existing personas.
		Brain: Brain{
			Provider: strings.TrimSpace(cfg.BrainProvider),
			Model:    modelForProvider(cfg, cfg.BrainProvider),
		},
		TTS: base.TTS,
		Prompts: PromptRefs{
			Persona: id,
			// Turn empty = shared embedded turn instruction (see PromptRefs).
		},
	}
	// Prefer an explicit app-level provider when FromConfig left it empty.
	if strings.TrimSpace(p.TTS.Provider) == "" && cfg != nil {
		p.TTS.Provider = strings.TrimSpace(cfg.TTSProvider)
	}
	if strings.TrimSpace(p.TTS.Voice) == "" && cfg != nil {
		p.TTS.Voice = voiceForProvider(cfg, p.TTS.Provider)
	}
	if err := Write(p, false); err != nil {
		return nil, err
	}
	// Seed the private document after the profile lands so a failed profile
	// write cannot leave an orphan prompts/persona/<id>.yaml behind.
	if err := WriteSystemPrompt(id, seed); err != nil {
		return nil, fmt.Errorf("seeding system prompt for %s: %w", id, err)
	}
	return p, nil
}

// seedSystemPrompt picks the body used to initialize a new persona's private
// prompt document: the active config's persona prompt when it resolves, else
// the embedded default. Always a copy — never a shared catalog ref.
func seedSystemPrompt(cfg *config.Config) (string, error) {
	if cfg != nil {
		if ref := strings.TrimSpace(cfg.Persona); ref != "" {
			if text, err := LoadSystemPrompt(ref); err == nil && strings.TrimSpace(text) != "" {
				return text, nil
			}
		}
	}
	return DefaultSystemPrompt()
}

// CreateAndUse creates a persona and makes it active.
func CreateAndUse(cfg *config.Config, displayName string) (*Profile, error) {
	p, err := Create(cfg, displayName)
	if err != nil {
		return nil, err
	}
	if err := Use(cfg, p.ID); err != nil {
		return p, err
	}
	return p, nil
}

// UniqueID returns a free persona id based on a preferred slug.
func UniqueID(preferred string) (string, error) {
	return uniqueID(preferred)
}

func uniqueID(preferred string) (string, error) {
	id := Slugify(preferred)
	if id == "" {
		id = "persona"
	}
	if err := ValidateID(id); err != nil {
		id = "persona"
	}
	candidate := id
	for n := 2; n < 1000; n++ {
		path := ProfilePath(candidate)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return candidate, nil
		} else if err != nil {
			return "", fmt.Errorf("checking persona id %q: %w", candidate, err)
		}
		candidate = fmt.Sprintf("%s-%d", id, n)
	}
	return "", fmt.Errorf("could not allocate a unique persona id from %q", preferred)
}

// Exists reports whether a persona profile is on disk for id.
func Exists(id string) bool {
	if err := ValidateID(id); err != nil {
		return false
	}
	_, err := os.Stat(ProfilePath(id))
	return err == nil
}

// Slugify turns a display name into a lowercase kebab-case persona id candidate.
func Slugify(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
		case r == ' ' || r == '_' || r == '-' || r == '.' || r == '/':
			b.WriteByte('-')
		}
	}
	s := nonSlugRunes.ReplaceAllString(b.String(), "-")
	s = strings.Trim(s, "-")
	// Collapse accidental doubles after trim.
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return s
}
