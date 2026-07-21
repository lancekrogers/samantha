// Package meeting is the domain package for meeting recording helpers and
// post-meeting note routing.
//
// Stop phrases are shared by CLI and TUI so match semantics cannot drift.
// Routing is additive and lossless: the original .log/.jsonl pair always stays
// in the meetings directory; a failed or skipped route never deletes local data.
// Dual-file writers live in package meeting/log (import as meetinglog).
package meeting

import (
	"context"
	"time"

	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

// Destination types supported in v1.
const (
	TypeCampaign   = "campaign"
	TypeFile       = "file"
	TypeAppleNotes = "apple-notes"
)

// Route modes control post-meeting behaviour.
const (
	ModeAsk  = "ask"
	ModeAuto = "auto"
	ModeOff  = "off"
)

// Body scopes control export shape. Both include the full transcript text so
// campaign intents and other sinks stay self-contained (not just local paths).
const (
	BodyNotes = "notes" // notes + bookmarks + metadata + full transcript
	BodyFull  = "full"  // same content as notes (compat alias)
)

// Outcome is the result of a route attempt.
const (
	OutcomeRouted    = "routed"
	OutcomeSkipped   = "skipped"
	OutcomeFailed    = "failed"
	OutcomeDelegated = "delegated"
)

// Destination is a named route target (config + picker identity).
type Destination struct {
	ID       string   // stable key for config + picker
	Type     string   // campaign | file | apple-notes
	Campaign string   // registry name or path (campaign)
	Capture  string   // intent | note (campaign); default intent
	Tags     []string // optional camp idea tags
	Path     string   // directory for file sink
	Folder   string   // Apple Notes folder
}

// RenderedNote is the single markdown artifact fed to every sink.
type RenderedNote struct {
	Title       string
	Body        string // full markdown document
	Summary     meetinglog.Summary
	BodyScope   string
	SourceJSONL string
	SourceLog   string
}

// Receipt is returned by a successful (or delegated) sink Route call.
type Receipt struct {
	DestinationID string
	Type          string
	Outcome       string
	Detail        string // human-readable path, intent id, etc.
	At            time.Time
}

// Sink exports a rendered note somewhere useful.
type Sink interface {
	Route(ctx context.Context, note RenderedNote) (Receipt, error)
}

// Config is the runtime routing configuration (from config.Config.Meeting).
type Config struct {
	Mode         string // ask | auto | off
	Default      string // destination ID for auto / ask preselect
	Body         string // notes | full
	Destinations []Destination
}

// Normalize fills defaults for zero-value fields.
func (c Config) Normalize() Config {
	if c.Mode == "" {
		c.Mode = ModeAsk
	}
	if c.Body == "" {
		c.Body = BodyNotes
	}
	for i := range c.Destinations {
		if c.Destinations[i].Type == TypeCampaign && c.Destinations[i].Capture == "" {
			c.Destinations[i].Capture = "intent"
		}
	}
	return c
}

// DestinationByID returns the destination with the given id, if any.
func (c Config) DestinationByID(id string) (Destination, bool) {
	for _, d := range c.Destinations {
		if d.ID == id {
			return d, true
		}
	}
	return Destination{}, false
}
