// Package remote records meetings driven by a network client (phone, Watch)
// instead of the local microphone.
//
// The client owns its own audio session and ships PCM as sequenced,
// individually acknowledged segments; nothing here touches the serve
// WebSocket, the dispatcher's turn loop, or the remote-microphone claim.
// Transcription, diarization, and summarization all run after the recording
// stops, so the design optimizes for not losing audio rather than for latency.
//
// The bundle written here is byte-for-byte the desktop layout
// (meeting.md, .samantha/events.jsonl, audio.wav): downstream routing,
// rendering, and reprocessing cannot tell the two apart.
//
// Restart contract: sessions live in memory for the life of the serve
// process. Resilience covers *network* interruptions (client outbox +
// idempotent re-push + the janitor's interrupted path), not serve restarts —
// after a restart every meeting id answers 404. Bundles are closed on
// shutdown, so audio already delivered is preserved on disk and finishes
// through the desktop tooling (`samantha meeting route` / reprocess).
package remote

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lancekrogers/samantha/internal/meeting"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

// State is the lifecycle a client polls with GET /v1/meeting/{id}.
type State string

const (
	// StateRecording accepts segments and control events.
	StateRecording State = "recording"
	// StateProcessing means audio is finalized and the pipeline is running.
	StateProcessing State = "processing"
	// StateReady means the pipeline finished and results are final.
	StateReady State = "ready"
	// StateFailed means processing failed; the bundle is intact and the
	// meeting can be reprocessed from the desktop tooling.
	StateFailed State = "failed"
	// StateInterrupted means the client went quiet mid-recording. Audio so
	// far is preserved and the tail may still be re-pushed.
	StateInterrupted State = "interrupted"
)

// Defaults for a serve-hosted meeting. SegmentSeconds and OutboxCapSegments
// are handed to the client at start so the wire stays server-driven.
const (
	DefaultSegmentSeconds    = 5
	DefaultOutboxCapSegments = 120 // 10 minutes at 5 s per segment
	DefaultStallTimeout      = 5 * time.Minute
	DefaultSweepInterval     = 15 * time.Second
	DefaultRetention         = 24 * time.Hour
	DefaultProcessTimeout    = 2 * time.Hour
	DefaultMaxSessions       = 256

	// MaxSegmentBytes caps one upload well above a 5 s 16 kHz mono segment
	// (160 KB) while refusing a body that could exhaust memory.
	MaxSegmentBytes = 4 << 20

	// MaxSegmentSeq bounds the sequence space. Both the sequence number and
	// the stop request's last_seq are client-controlled and drive loops over
	// [0, seq], so an unbounded value is a denial of service — and no honest
	// client gets near this (roughly six days at 5 s per segment).
	MaxSegmentSeq = 100_000

	// maxReportedMissing caps the missing_seqs list in a status response: a
	// client needs to know it has gaps and where to start, not receive
	// thousands of integers on every poll.
	maxReportedMissing = 256
)

// Errors callers translate to HTTP status codes.
var (
	// ErrMeetingActive is a second start while one meeting is still live.
	ErrMeetingActive = errors.New("meeting: another meeting is already recording")
	// ErrNotFound is an unknown meeting id.
	ErrNotFound = errors.New("meeting: unknown meeting id")
	// ErrNotRecording is a segment, control, or stop against a meeting that
	// has already been finalized.
	ErrNotRecording = errors.New("meeting: meeting is no longer recording")
	// ErrProcessing is a stop while the pipeline is already running.
	ErrProcessing = errors.New("meeting: meeting is already processing")
	// ErrBadSegment is a malformed segment upload.
	ErrBadSegment = errors.New("meeting: malformed audio segment")
	// ErrBadControl is an unsupported control action.
	ErrBadControl = errors.New("meeting: unsupported control action")
	// ErrNoteText is a note control with no text. An empty note would still
	// bump the bundle's note counter and leave a blank marker in the
	// document, so it is refused instead of quietly recorded. Kept distinct
	// from ErrBadControl so the client reads why, not just that.
	ErrNoteText = errors.New("meeting: note requires text")
	// ErrBadStart is a start request with an unknown capture source.
	ErrBadStart = errors.New("meeting: unknown capture source")
	// ErrRoutePlanDestination is a route_plan with no destination id. The
	// plan is durable and drives an unattended delivery, so it must name
	// where the notes go before the recording starts.
	ErrRoutePlanDestination = errors.New("meeting: route_plan requires destination_id")
	// ErrRoutePlanBody is a route_plan with a body scope that is neither
	// notes nor full (empty means the configured meeting.route.body).
	ErrRoutePlanBody = errors.New("meeting: route_plan body must be notes or full")
	// ErrPipelineUnavailable means serve has no transcription pipeline
	// configured; the recording is kept, only the results are missing.
	ErrPipelineUnavailable = errors.New("meeting: no processing pipeline configured")
	// ErrNotRoutable is a route against a meeting that has no finished
	// summary yet — still recording, still processing, or failed.
	ErrNotRoutable = errors.New("meeting: meeting has no finished notes to route yet")
)

// StartRequest is the client's POST /v1/meeting/start body. Source names the
// capture surface (ios, mac, watch); empty means ios — phones were the only
// clients before the field existed, so absence keeps meaning what it always
// did.
//
// RoutePlan is the durable filing intent, written to the bundle before the
// first segment so a crash mid-meeting still leaves a plan the sweep can
// deliver. Campaign stays for the phone's post-stop route; when both are
// present RoutePlan owns the plan and Campaign is only remembered on Status.
type StartRequest struct {
	Title     string     `json:"title,omitempty"`
	Campaign  string     `json:"campaign,omitempty"`
	Source    string     `json:"source,omitempty"`
	RoutePlan *RoutePlan `json:"route_plan,omitempty"`
}

// RoutePlan names where a meeting's notes should be filed when it finishes.
// DestinationID is a configured meeting.route.destinations[] id or
// camp:<campaign> — the same vocabulary `meeting route --to` accepts. Body is
// notes, full, or empty for the configured meeting.route.body.
type RoutePlan struct {
	DestinationID string `json:"destination_id"`
	Body          string `json:"body,omitempty"`
}

// StartResponse tells the client how to chunk and buffer audio.
type StartResponse struct {
	MeetingID         string `json:"meeting_id"`
	SegmentSeconds    int    `json:"segment_seconds"`
	OutboxCapSegments int    `json:"outbox_cap_segments"`
}

// ControlRequest is the client's POST /v1/meeting/{id}/control body. OffsetMs
// is meeting-relative and authoritative: the client knows when the moment
// happened, the server only learns of it a network hop later.
// Text is required for the note action and optional everywhere else; Label is
// ignored for note.
type ControlRequest struct {
	Action   string `json:"action"`
	OffsetMs int64  `json:"offset_ms"`
	Label    string `json:"label,omitempty"`
	Text     string `json:"text,omitempty"`
}

// StopRequest declares the highest sequence number the client ever produced,
// which is what makes a missing-segment check possible at all.
type StopRequest struct {
	LastSeq int64 `json:"last_seq"`
}

// RouteRequest is the client's POST /v1/meeting/{id}/route body. Capture is
// optional; empty means the CI0009 meetings importer (the default the whole
// design routes through).
type RouteRequest struct {
	Campaign string `json:"campaign"`
	Capture  string `json:"capture,omitempty"`
}

// RouteReceipt is the route response. Destination echoes where the note
// landed so the phone can say "Filed to <campaign> notes/meetings" from the
// wire rather than assuming.
type RouteReceipt struct {
	Outcome     string `json:"outcome"`
	Detail      string `json:"detail,omitempty"`
	Destination string `json:"destination,omitempty"`
}

// Status is the GET /v1/meeting/{id} payload. MissingSeqs is truncated to
// maxReportedMissing entries; MissingCount is always the true total.
type Status struct {
	MeetingID string `json:"meeting_id"`
	State     State  `json:"state"`
	// Step names the pipeline stage while State is processing — transcribing,
	// filing ideas, diarizing — so the phone can show what is actually
	// happening instead of an anonymous spinner. Empty outside processing.
	Step         string              `json:"step,omitempty"`
	Bundle       string              `json:"bundle,omitempty"`
	Title        string              `json:"title,omitempty"`
	Campaign     string              `json:"campaign,omitempty"`
	StartedAt    time.Time           `json:"started_at"`
	MissingSeqs  []int64             `json:"missing_seqs,omitempty"`
	MissingCount int                 `json:"missing_count,omitempty"`
	Result       *meetinglog.Summary `json:"result,omitempty"`
	Error        string              `json:"error,omitempty"`
}

// Pipeline turns a finalized bundle into a transcript and speaker analysis.
// It runs off the request path and appends to the bundle through the writer it
// is handed; implementations own STT, diarization, and summarization.
type Pipeline interface {
	Process(ctx context.Context, job Job) error
}

// Job is one unit of post-recording work.
type Job struct {
	// MeetingID is the session's wire id — the same one the client used in
	// /v1/meeting/{id} calls, so anything the pipeline files (idea intents)
	// links back with the identifier the phone already knows.
	MeetingID string
	// BundlePath is the .meeting directory.
	BundlePath string
	// AudioPath is the assembled 16 kHz mono WAV inside the bundle.
	AudioPath string
	// Writer is the open bundle writer. The pipeline appends utterances and
	// speaker analysis; the caller owns closing it.
	Writer *meetinglog.Writer
	// Title is the meeting description, for summary prompts.
	Title string
	// Step reports the current pipeline stage for status polls. Optional;
	// implementations call it as they move between stages.
	Step func(string)
}

// RoutePlanFunc delivers a finished meeting to the destination its start
// request planned. It is injected because routing needs camp discovery and
// config, which this package deliberately does not own.
type RoutePlanFunc func(ctx context.Context, summary meetinglog.Summary, destID, body string) (RouteReceipt, error)

// PipelineFunc adapts a plain function to Pipeline.
type PipelineFunc func(ctx context.Context, job Job) error

// Process implements Pipeline.
func (f PipelineFunc) Process(ctx context.Context, job Job) error { return f(ctx, job) }

// controlActions maps wire actions to the bundle event types they append.
var controlActions = map[string]string{
	"pause":      meetinglog.TypePause,
	"resume":     meetinglog.TypeResume,
	"bookmark":   meetinglog.TypeBookmark,
	"note":       meetinglog.TypeNote,
	"idea_start": meetinglog.TypeIdeaStart,
	"idea_end":   meetinglog.TypeIdeaEnd,
}

// campRoutePrefix is the destination-id vocabulary for "a campaign by name",
// shared with `meeting route --to` and the router's synthesized destinations.
const campRoutePrefix = "camp:"

// routePlanTimeout bounds one automatic plan delivery. Filing spawns camp (or
// osascript) and runs after the client's request is long gone, so it needs a
// deadline of its own — a wedged sink must not hold the session's goroutine
// for the life of the process.
const routePlanTimeout = 2 * time.Minute

// CampaignRouteKey is the RouteOnce key for filing a meeting into a campaign.
// The wire's POST /v1/meeting/{id}/route and a start-time route_plan naming
// camp:<campaign> produce the same key, so the two paths share one execution
// instead of filing the same notes twice — the importer does not dedupe.
func CampaignRouteKey(capture, campaign string) string {
	return meeting.NormalizeCampaignCapture(capture) + "\x00" + campaign
}

// routePlan is a validated start-time filing intent.
type routePlan struct {
	destID string
	body   string
}

// key maps the plan onto the RouteOnce key space. A camp:<name> plan lands on
// the same key a manual campaign route uses; anything else (file, apple-notes,
// a configured id) is keyed by the destination id, which the wire cannot name.
func (p routePlan) key() string {
	if campaign, ok := strings.CutPrefix(p.destID, campRoutePrefix); ok {
		return CampaignRouteKey("", campaign)
	}
	return "destination\x00" + p.destID
}

// normalizeRoutePlan validates a start request's plan. Shape only: whether the
// destination exists is a delivery-time question, because discovering it costs
// a camp subprocess on the request path and an unresolvable id must fail
// loudly as route_failed rather than block a recording.
func normalizeRoutePlan(plan *RoutePlan) (routePlan, error) {
	if plan == nil {
		return routePlan{}, nil
	}
	destID := strings.TrimSpace(plan.DestinationID)
	if destID == "" {
		return routePlan{}, ErrRoutePlanDestination
	}
	body := strings.TrimSpace(plan.Body)
	switch body {
	case "", meeting.BodyNotes, meeting.BodyFull:
	default:
		return routePlan{}, fmt.Errorf("%w (got %q)", ErrRoutePlanBody, body)
	}
	return routePlan{destID: destID, body: body}, nil
}
