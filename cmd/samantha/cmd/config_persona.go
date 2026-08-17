package cmd

// configPersonaPayload is the persona block of `config get --json`. It names
// the keys the active persona overrides rather than replacing their values, so
// a front end can badge a control without lying about what the file holds.
type configPersonaPayload struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"display_name"`
	Overrides   []string `json:"overrides"`
}

// personaOverlay reports the active persona and the keys it overrides, or nil
// when there is no persona, no profile on disk, or the profile is unreadable.
// A broken persona must never break Settings, so this reports nothing rather
// than failing.
func personaOverlay() *configPersonaPayload {
	// Filled in by the persona-overrides slice.
	return nil
}

// personaOverridesKey reports whether the active persona overrides one key.
func personaOverridesKey(string) bool {
	return false
}
