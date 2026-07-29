// Package transcript appends conversation events to a JSONL file — the
// machine-readable session record consumed by the duet harness and useful on
// its own for field debugging (WI-dc9e33 PR-1). One JSON object per line; the
// first line is a header carrying the schema version.
package transcript

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lancekrogers/samantha/internal/events"
)

// queueCapacity bounds the writer queue. Bus handlers run synchronously on
// the emitting pipeline goroutine, so a slow disk must drop records rather
// than stall a turn; drops are counted and reported in the final record.
const queueCapacity = 1024

// Version is the JSONL schema version written in the header record.
const Version = 1

// record is the flat wire form for every line. Zero fields are omitted, so
// each type carries only its own keys.
type record struct {
	Type string `json:"type"`
	TS   string `json:"ts,omitempty"`
	Seq  int64  `json:"seq,omitempty"`

	V   int `json:"v,omitempty"`   // header
	PID int `json:"pid,omitempty"` // header

	Text        string `json:"text,omitempty"`
	Name        string `json:"name,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Preview     string `json:"preview,omitempty"`
	Stage       string `json:"stage,omitempty"`
	Message     string `json:"message,omitempty"`
	Phase       string `json:"phase,omitempty"`
	State       string `json:"state,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Outcome     string `json:"outcome,omitempty"`
	Degraded    bool   `json:"degraded,omitempty"`
	Interrupted bool   `json:"interrupted,omitempty"`
	IsError     bool   `json:"is_error,omitempty"`

	STTFinalS      float64 `json:"stt_s,omitempty"`
	ModelFirstS    float64 `json:"model_first_s,omitempty"`
	ModelS         float64 `json:"model_s,omitempty"`
	VoiceS         float64 `json:"voice_s,omitempty"`
	PlaybackStartS float64 `json:"playback_start_s,omitempty"`
	SpokeS         float64 `json:"spoke_s,omitempty"`
	BargeInS       float64 `json:"barge_in_s,omitempty"`

	Prefill int `json:"prefill,omitempty"`
	Gen     int `json:"gen,omitempty"`

	TurnsBefore int   `json:"turns_before,omitempty"`
	Dropped     int64 `json:"dropped,omitempty"`
}

// Writer subscribes to a bus and appends one record per event.
type Writer struct {
	f       *os.File
	ch      chan record
	done    chan struct{}
	seq     atomic.Int64
	dropped atomic.Int64
	closed  atomic.Bool
	once    sync.Once
}

// NewWriter opens (appending) the JSONL file and starts the writer goroutine.
func NewWriter(path string) (*Writer, error) {
	return newWriter(path, queueCapacity)
}

func newWriter(path string, capacity int) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open transcript log: %w", err)
	}
	w := &Writer{f: f, ch: make(chan record, capacity), done: make(chan struct{})}
	go w.run()
	w.enqueue(record{Type: "header", V: Version, PID: os.Getpid()})
	return w, nil
}

// Attach subscribes the writer to the events the transcript records.
// High-rate display traffic (AudioLevel, TranscriptPartial, per-segment voice
// events) stays excluded by design.
func (w *Writer) Attach(bus *events.Bus) {
	events.Subscribe(bus, func(e events.UserInput) {
		w.enqueue(record{Type: "user", Text: e.Text})
	})
	events.Subscribe(bus, func(e events.ResponseDelta) {
		w.enqueue(record{Type: "agent_delta", Text: e.Text})
	})
	events.Subscribe(bus, func(e events.ResponseReady) {
		w.enqueue(record{Type: "agent", Text: e.Response, Degraded: e.Degraded, Interrupted: e.Interrupted})
	})
	events.Subscribe(bus, func(e events.ToolCallStarted) {
		w.enqueue(record{Type: "tool_call", Name: e.Name, Summary: e.Summary})
	})
	events.Subscribe(bus, func(e events.ToolCallFinished) {
		w.enqueue(record{Type: "tool_result", Name: e.Name, Preview: e.Preview, Message: e.Err, IsError: e.Err != ""})
	})
	events.Subscribe(bus, func(e events.Error) {
		w.enqueue(record{Type: "error", Stage: e.Stage, Message: e.Message})
	})
	events.Subscribe(bus, func(e events.Info) {
		w.enqueue(record{Type: "info", Message: e.Message})
	})
	events.Subscribe(bus, func(e events.STTPhase) {
		w.enqueue(record{Type: "stt", Phase: e.Phase})
	})
	events.Subscribe(bus, func(events.ThinkingStarted) {
		w.enqueue(record{Type: "thinking"})
	})
	events.Subscribe(bus, func(e events.SpeakingStarted) {
		w.enqueue(record{Type: "speaking", State: "started"})
	})
	events.Subscribe(bus, func(e events.SpeakingComplete) {
		w.enqueue(record{Type: "speaking", State: "complete", Interrupted: e.Interrupted, SpokeS: e.Elapsed.Seconds()})
	})
	events.Subscribe(bus, func(e events.SpeakingInterrupted) {
		w.enqueue(record{Type: "speaking", State: "interrupted", Reason: e.Reason})
	})
	events.Subscribe(bus, func(e events.TurnInterrupted) {
		w.enqueue(record{Type: "turn_interrupted", Reason: e.Reason})
	})
	events.Subscribe(bus, func(e events.TurnMetrics) {
		w.enqueue(record{
			Type:           "metrics",
			Outcome:        e.Outcome,
			Degraded:       e.Degraded,
			Interrupted:    e.Interrupted,
			STTFinalS:      e.STTFinalElapsed.Seconds(),
			ModelFirstS:    e.FirstModelChunkElapsed.Seconds(),
			ModelS:         e.ModelCompleteElapsed.Seconds(),
			VoiceS:         e.FirstAudioReadyElapsed.Seconds(),
			PlaybackStartS: e.PlaybackStartElapsed.Seconds(),
			SpokeS:         e.PlaybackCompleteElapsed.Seconds(),
			BargeInS:       e.BargeInElapsed.Seconds(),
		})
	})
	events.Subscribe(bus, func(e events.TokenUsage) {
		w.enqueue(record{Type: "tokens", Prefill: e.Prefill, Gen: e.Gen})
	})
	events.Subscribe(bus, func(e events.SessionWarning) {
		w.enqueue(record{Type: "session_warning", Prefill: e.PromptTokens})
	})
	events.Subscribe(bus, func(e events.ConversationCompacted) {
		w.enqueue(record{Type: "compacted", TurnsBefore: e.TurnsBefore})
	})
	events.Subscribe(bus, func(events.ConversationCleared) {
		w.enqueue(record{Type: "cleared"})
	})
}

// enqueue stamps and queues one record; it never blocks the emitting
// goroutine. The bus outlives conversations, so post-Close events are dropped
// silently rather than written to a closed file.
func (w *Writer) enqueue(r record) {
	if w.closed.Load() {
		return
	}
	r.TS = time.Now().UTC().Format(time.RFC3339Nano)
	r.Seq = w.seq.Add(1)
	select {
	case w.ch <- r:
	default:
		w.dropped.Add(1)
	}
}

func (w *Writer) run() {
	defer close(w.done)
	for r := range w.ch {
		w.write(r)
	}
	if n := w.dropped.Load(); n > 0 {
		w.write(record{
			Type:    "dropped",
			TS:      time.Now().UTC().Format(time.RFC3339Nano),
			Seq:     w.seq.Add(1),
			Dropped: n,
		})
	}
}

// write appends and fsyncs one line. Event rate is human-conversation scale,
// so per-record durability is affordable; a torn final line after a crash is
// tolerated by readers.
func (w *Writer) write(r record) {
	data, err := json.Marshal(r)
	if err != nil {
		return
	}
	if _, err := w.f.Write(append(data, '\n')); err != nil {
		return
	}
	_ = w.f.Sync()
}

// Close stops accepting events, drains the queue, records drops, and closes
// the file. Safe to call more than once.
func (w *Writer) Close() error {
	var err error
	w.once.Do(func() {
		w.closed.Store(true)
		close(w.ch)
		<-w.done
		err = w.f.Close()
	})
	return err
}
