// Package wire holds pure API/CLI value types shared by internal/netapi and
// cmd/samantha/cmd. It must stay free of cgo-heavy imports (no
// internal/meeting/remote, pkg/voiceagent/audio, etc.) so cmd/samantha/cmd
// files built with -tags integration (CGO_ENABLED=0) can depend on these
// shapes without pulling netapi's full dependency graph in.
package wire

import "time"

// SessionSummary is one row of GET /v1/sessions and `sessions list --json`.
// internal/netapi.SessionSummary is a type alias for this so the JSON
// contract stays identical on both paths.
type SessionSummary struct {
	ID        string    `json:"id"`
	Summary   string    `json:"summary"`
	Turns     int       `json:"turns"`
	UpdatedAt time.Time `json:"updated_at"`
}
