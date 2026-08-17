package config

// restartNotRequired lists the keys proven to take effect without restarting
// the agent. Everything absent from this map is assumed to need a restart:
// serve builds its pipeline once at start (cmd/samantha/cmd/serve.go
// buildServePipeline) and the TUI snapshots config at conversation entry
// (internal/tui/tui.go), so "no restart" is a claim that must be earned.
//
// Two traps justify the conservative default: newServeMeetingRouter(cfg) and
// newServeMeetingManager(cfg) both capture cfg at serve start, so meeting.* and
// speaker.meeting.* changes are invisible to a running serve despite looking
// like per-operation settings.
var restartNotRequired = map[string]bool{
	"tts_provider":      true, // applies to subsequent utterances
	"tui_mouse_enabled": true, // applies on next enter/leave of the conversation
	"active_persona":    true, // a new conversation re-resolves the binding
}

// restartVerified marks keys whose restart behaviour has been confirmed on
// hardware rather than inferred. Unverified keys still report
// restart_required=true — the marker only records what is still owed, and is
// never shown to users (an app that says "restart, we think" is worse than one
// that just restarts).
var restartVerified = map[string]bool{
	"tts_provider":           true,
	"tui_mouse_enabled":      true,
	"active_persona":         true,
	"voice_frontend_enabled": true, // verified: runtime rebuild required
	"barge_in_enabled":       true, // verified: runtime rebuild required
}

// RestartRequired reports whether changing key needs an agent restart to take
// effect. Unknown keys answer true: the honest default for a key nobody has
// characterised.
func RestartRequired(key string) bool {
	return !restartNotRequired[key]
}

// RestartVerified reports whether RestartRequired(key) is evidence rather than
// inference.
func RestartVerified(key string) bool {
	return restartVerified[key]
}
