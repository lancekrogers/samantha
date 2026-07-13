package netapi

import (
	"encoding/json"
	"time"

	"github.com/lancekrogers/samantha/internal/events"
)

// encodeEvent maps a bus event to its wire envelope: the eventType wire name
// as a "type" discriminator plus explicit lowercase fields. Durations are
// encoded as integer milliseconds — Go's default for time.Duration is
// nanoseconds, which no client wants.
func encodeEvent(e events.Event) map[string]any {
	env := map[string]any{"type": events.EventType(e)}

	switch e := e.(type) {
	case events.STTPhase:
		env["phase"] = e.Phase
		env["elapsed_ms"] = ms(e.Elapsed)
	case events.UserInput:
		env["text"] = e.Text
	case events.TranscriptPartial:
		env["text"] = e.Text
	case events.ThinkingComplete:
		env["response"] = e.Response
		env["elapsed_ms"] = ms(e.Elapsed)
	case events.TurnMetrics:
		env["outcome"] = e.Outcome
		env["interrupted"] = e.Interrupted
		env["stt_final_ms"] = ms(e.STTFinalElapsed)
		env["first_model_chunk_ms"] = ms(e.FirstModelChunkElapsed)
		env["model_complete_ms"] = ms(e.ModelCompleteElapsed)
		env["first_segment_ms"] = ms(e.FirstSegmentElapsed)
		env["first_audio_ready_ms"] = ms(e.FirstAudioReadyElapsed)
		env["playback_start_ms"] = ms(e.PlaybackStartElapsed)
		env["playback_complete_ms"] = ms(e.PlaybackCompleteElapsed)
		env["barge_in_ms"] = ms(e.BargeInElapsed)
	case events.ResponseStreamingStarted:
		env["elapsed_ms"] = ms(e.Elapsed)
	case events.SpeechSegmentReady:
		env["text"] = e.Text
	case events.GeneratingVoice:
		env["sentence"] = e.Sentence
	case events.VoiceGenerated:
		env["sentence"] = e.Sentence
		env["elapsed_ms"] = ms(e.Elapsed)
	case events.SpeakingStarted:
		env["text"] = e.Text
	case events.SpeakingComplete:
		env["elapsed_ms"] = ms(e.Elapsed)
		env["interrupted"] = e.Interrupted
	case events.SpeakingInterrupted:
		env["reason"] = e.Reason
	case events.TurnInterrupted:
		env["reason"] = e.Reason
	case events.ResponseReady:
		env["response"] = e.Response
		env["interrupted"] = e.Interrupted
	case events.Error:
		env["stage"] = e.Stage
		env["message"] = e.Message
	case events.Info:
		env["message"] = e.Message
	}
	// ThinkingStarted and ConversationCleared carry no fields.

	return env
}

func marshalEvent(e events.Event) ([]byte, error) {
	return json.Marshal(encodeEvent(e))
}

func ms(d time.Duration) int64 { return d.Milliseconds() }

// controlMessage is one client -> server message on /v1/stream.
type controlMessage struct {
	Type       string `json:"type"` // text_input | interrupt | clear_history | audio_output | voice_start | voice_end | audio_input
	Text       string `json:"text,omitempty"`
	Mode       string `json:"mode,omitempty"` // audio_output: "stream" | "local" | "off"
	Data       string `json:"data,omitempty"` // audio_input: base64 pcm_s16le mono @ 16 kHz
	SampleRate int    `json:"sample_rate,omitempty"`
}
