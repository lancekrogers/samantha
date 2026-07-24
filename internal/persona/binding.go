package persona

import (
	"fmt"
	"strings"

	"github.com/lancekrogers/samantha/internal/config"
)

// SessionBinding is the identity snapshot a conversation session runs with:
// persona id, display/agent name, system prompt reference, and voice. It is
// resolved once at conversation start and never tracks later edits to persona
// profiles on disk or to the live global config — switching or editing
// persona B must not change the identity of an in-flight session bound to
// persona A. Restarting a conversation picks up disk changes by resolving a
// fresh binding.
type SessionBinding struct {
	PersonaID   string
	DisplayName string
	// AgentName is the effective assistant name after applying the profile
	// over the config (profiles with an empty display name keep the default).
	AgentName string
	// PromptRef is the system prompt doc name the brain resolves at build.
	PromptRef string
	TTS       TTS

	cfg config.Config
}

// ResolveBinding loads the persona profile (empty id resolves the configured
// active persona) and returns the identity snapshot plus a binding-derived
// config. The snapshot is the only config a session runtime should hold:
// later persona.Use / Apply / Settings writes mutate other Config values,
// never a resolved binding.
func ResolveBinding(cfg *config.Config, id string) (*SessionBinding, error) {
	if cfg == nil {
		return nil, fmt.Errorf("persona: config is nil")
	}
	if strings.TrimSpace(id) == "" {
		id = ActiveID(cfg)
	}
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	p, err := Load(id)
	if err != nil {
		return nil, err
	}

	snap := *cfg
	Apply(&snap, p)
	return &SessionBinding{
		PersonaID:   p.ID,
		DisplayName: p.DisplayName,
		AgentName:   snap.AgentName,
		PromptRef:   snap.Persona,
		TTS:         p.TTS,
		cfg:         snap,
	}, nil
}

// Config returns a fresh copy of the binding-derived config, so no caller can
// mutate the snapshot the session was bound to.
func (b *SessionBinding) Config() *config.Config {
	c := b.cfg
	return &c
}
