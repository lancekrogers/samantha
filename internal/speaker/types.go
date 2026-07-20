// Package speaker provides optional speaker analysis (diarization, embedding
// identification) for meetings and live conversation.
//
// The package is designed so a disabled or missing engine never blocks capture,
// STT, TTS, or meeting recording. Production engines (sherpa-onnx) plug in
// behind Engine; tests use FakeEngine.
package speaker

import (
	"time"
)

// State describes how trustworthy a label is.
type State string

const (
	StateProvisional State = "provisional"
	StateStable      State = "stable"
	StateRevised     State = "revised"
	StateRejected    State = "rejected"
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
	SegmentID  string        `json:"segment_id,omitempty"`
	Start      time.Duration `json:"start"`
	End        time.Duration `json:"end"`
	Label      string        `json:"label"` // profile id | speaker-N | unknown
	Confidence float32       `json:"confidence"`
	State      State         `json:"state"`
	Source     Source        `json:"source,omitempty"`
	ModelRev   string        `json:"model_revision,omitempty"`
	EnrollRev  string        `json:"enrollment_revision,omitempty"`
}

// Event is a streaming notification for live adapters.
type Event struct {
	Kind        EventKind   `json:"kind"`
	Observation Observation `json:"observation"`
}

// Timeline is a finalized (or partial) ordered set of observations.
type Timeline struct {
	Observations []Observation `json:"observations"`
	FinalizedAt  time.Time     `json:"finalized_at,omitempty"`
}

// Duration returns End-Start for an observation (clamped at zero).
func (o Observation) Duration() time.Duration {
	if o.End <= o.Start {
		return 0
	}
	return o.End - o.Start
}
