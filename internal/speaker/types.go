// Package speaker provides optional speaker analysis (diarization, embedding
// identification) for meetings and live conversation.
//
// The package is designed so a disabled or missing engine never blocks capture,
// STT, TTS, or meeting recording. Production engines (sherpa-onnx) plug in
// behind Engine; tests use FakeEngine.
//
// Wire format: Observation.StartMS/EndMS are milliseconds from session start
// (not time.Duration nanoseconds). Label is a stable profile id or speaker-N;
// display casing is the caller's responsibility.
package speaker

import "time"

// State describes how trustworthy a label is.
type State string

const (
	// StateProvisional is a best-effort interim label that may change.
	StateProvisional State = "provisional"
	// StateStable is a final label from a successful engine path.
	StateStable State = "stable"
	// StateRevised replaces a prior provisional/stable observation.
	StateRevised State = "revised"
	// StateRejected means no usable analysis (disabled, no audio, closed).
	StateRejected State = "rejected"
)

// Source identifies where the audio came from.
type Source string

const (
	SourceLocalMic     Source = "local_mic"
	SourceRemoteClient Source = "remote_client"
	SourceRecording    Source = "recording"
	SourceFixture      Source = "fixture"
)

// EventKind is the streaming notification type.
type EventKind string

const (
	EventSpeechStarted     EventKind = "speech_started"
	EventSpeakerChanged    EventKind = "speaker_changed"
	EventSpeakerUpdated    EventKind = "speaker_updated"
	EventSpeechEnded       EventKind = "speech_ended"
	EventTimelineFinalized EventKind = "timeline_finalized"
)

// Labels for non-enrolled speakers.
const (
	LabelUnknown = "unknown"
	// LabelSpeakerPrefix is used for anonymous diarization labels: speaker-1, speaker-2, …
	LabelSpeakerPrefix = "speaker-"
)

// Observation is one labeled audio span (the reusable data product).
type Observation struct {
	SegmentID  string  `json:"segment_id,omitempty"`
	StartMS    int64   `json:"start_ms"` // ms from session/recording start
	EndMS      int64   `json:"end_ms"`
	Label      string  `json:"label"` // stable profile id | speaker-N | unknown
	Confidence float32 `json:"confidence"`
	State      State   `json:"state"`
	Source     Source  `json:"source,omitempty"`
	ModelRev   string  `json:"model_revision,omitempty"`
	EnrollRev  string  `json:"enrollment_revision,omitempty"`
}

// Event is a streaming notification for live/meeting adapters.
// For EventTimelineFinalized, Observation may be a summary row and Timeline
// holds the full result when non-nil.
type Event struct {
	Kind        EventKind   `json:"kind"`
	Observation Observation `json:"observation"`
	Timeline    *Timeline   `json:"timeline,omitempty"`
}

// Timeline is a finalized (or partial) ordered set of observations.
type Timeline struct {
	Observations []Observation `json:"observations"`
	FinalizedAt  time.Time     `json:"finalized_at,omitempty"`
}

// DurationMS returns EndMS-StartMS for an observation (clamped at zero).
func (o Observation) DurationMS() int64 {
	if o.EndMS <= o.StartMS {
		return 0
	}
	return o.EndMS - o.StartMS
}

// MS converts a duration to whole milliseconds.
func MS(d time.Duration) int64 {
	return d.Milliseconds()
}
