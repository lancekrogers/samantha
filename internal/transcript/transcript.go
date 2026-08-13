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
	"unicode/utf8"

	"github.com/lancekrogers/samantha/pkg/voiceagent/events"
)

// queueCapacity bounds the writer queue. Bus handlers run synchronously on
// the emitting pipeline goroutine, so a slow disk must drop records rather
// than stall a turn; drops are counted and reported in the final record.
const queueCapacity = 1024

// summaryPreviewMax caps ConversationCompacted / SpeakingStarted text so a
// single line cannot dominate the log.
const summaryPreviewMax = 240

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

	Prefill   int `json:"prefill,omitempty"`
	Gen       int `json:"gen,omitempty"`
	Threshold int `json:"threshold,omitempty"`

	TurnsBefore int   `json:"turns_before,omitempty"`
	Dropped     int64 `json:"dropped,omitempty"`
	WriteErrors int64 `json:"write_errors,omitempty"`

	// LeakLines is the voice gate's stripped-line count: per event on
	// voice_gate records, per turn on metrics records (WI-dc9e33 B4).
	LeakLines int `json:"leak_lines,omitempty"`
}

// Writer subscribes to a bus and appends one record per event.
//
// Concurrency: enqueue and Close are serialized on mu so a late bus Emit after
// Close never panics with "send on closed channel". The writer goroutine alone
// owns the file after Open.
type Writer struct {
	f           *os.File
	ch          chan record
	done        chan struct{}
	seq         atomic.Int64
	dropped     atomic.Int64
	writeErrors atomic.Int64
	mu          sync.Mutex // protects closed + channel send/rebuild
	closed      bool
	once        sync.Once
	warnedIO    atomic.Bool
}

// NewWriter opens (appending) the JSONL file and starts the writer goroutine.
func NewWriter(path string) (*Writer, error) {
	return newWriter(path, queueCapacity)
}

func newWriter(path string, capacity int) (*Writer, error) {
	// Conversation content may include secrets; match diagnostics log perms.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
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
// events) stays excluded by design. agent_delta is recorded but non-durable
// under back-pressure so terminal user/agent/error/metrics lines win.
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
		w.enqueue(record{Type: "speaking", State: "started", Text: previewText(e.Text, summaryPreviewMax)})
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
			LeakLines:      e.ToolLeakLines,
		})
	})
	events.Subscribe(bus, func(e events.VoiceGateStripped) {
		w.enqueue(record{Type: "voice_gate", LeakLines: e.Lines})
	})
	events.Subscribe(bus, func(e events.TokenUsage) {
		w.enqueue(record{Type: "tokens", Prefill: e.Prefill, Gen: e.Gen})
	})
	events.Subscribe(bus, func(e events.SessionWarning) {
		w.enqueue(record{Type: "session_warning", Prefill: e.PromptTokens, Threshold: e.Threshold})
	})
	events.Subscribe(bus, func(e events.ConversationCompacted) {
		w.enqueue(record{
			Type:        "compacted",
			TurnsBefore: e.TurnsBefore,
			Summary:     previewText(e.Summary, summaryPreviewMax),
		})
	})
	events.Subscribe(bus, func(events.ConversationCleared) {
		w.enqueue(record{Type: "cleared"})
	})
}

// isDurableRecord reports terminal / harness-critical record types that must
// outrank streaming noise under back-pressure (same idea as the TUI bridge).
func isDurableRecord(r record) bool {
	switch r.Type {
	case "header", "user", "agent", "error", "tool_call", "tool_result",
		"metrics", "tokens", "session_warning", "compacted", "cleared",
		"turn_interrupted", "dropped":
		return true
	default:
		return false
	}
}

// enqueue stamps and queues one record; it never blocks the emitting
// goroutine. The bus outlives conversations, so post-Close events are dropped
// silently rather than written to a closed file.
func (w *Writer) enqueue(r record) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	r.TS = time.Now().UTC().Format(time.RFC3339Nano)
	r.Seq = w.seq.Add(1)

	select {
	case w.ch <- r:
		return
	default:
	}

	// Channel full: prefer durable records over agent_delta / speaking / etc.
	// Mirror the TUI bridge — drop oldest non-durable first, then oldest durable.
	q := make([]record, 0, cap(w.ch)+1)
	for {
		select {
		case m := <-w.ch:
			q = append(q, m)
		default:
			goto drained
		}
	}
drained:
	q = append(q, r)
	before := len(q)
	q = fitTranscriptQueue(q, cap(w.ch))
	if dropped := before - len(q); dropped > 0 {
		w.dropped.Add(int64(dropped))
	}
	for _, m := range q {
		select {
		case w.ch <- m:
		default:
			// Should be impossible after fit; count rather than block.
			w.dropped.Add(1)
		}
	}
}

// fitTranscriptQueue keeps at most capacity records, dropping oldest
// non-durable first. If only durable events remain, the oldest durable drops.
func fitTranscriptQueue(q []record, capacity int) []record {
	if capacity <= 0 {
		return nil
	}
	for len(q) > capacity {
		drop := -1
		for i, m := range q {
			if !isDurableRecord(m) {
				drop = i
				break
			}
		}
		if drop < 0 {
			q = q[1:]
			continue
		}
		q = append(q[:drop], q[drop+1:]...)
	}
	return q
}

func (w *Writer) run() {
	defer close(w.done)
	for r := range w.ch {
		w.write(r)
	}
	if n := w.dropped.Load(); n > 0 || w.writeErrors.Load() > 0 {
		w.write(record{
			Type:        "dropped",
			TS:          time.Now().UTC().Format(time.RFC3339Nano),
			Seq:         w.seq.Add(1),
			Dropped:     n,
			WriteErrors: w.writeErrors.Load(),
		})
	}
}

// write appends and fsyncs one line. Event rate is human-conversation scale,
// so per-record durability is affordable; a torn final line after a crash is
// tolerated by readers. I/O failures are counted and warned once on stderr so
// a harness is not silently incomplete mid-run.
func (w *Writer) write(r record) {
	data, err := json.Marshal(r)
	if err != nil {
		w.noteWriteError(fmt.Errorf("marshal: %w", err))
		return
	}
	if _, err := w.f.Write(append(data, '\n')); err != nil {
		w.noteWriteError(fmt.Errorf("write: %w", err))
		return
	}
	if err := w.f.Sync(); err != nil {
		w.noteWriteError(fmt.Errorf("sync: %w", err))
	}
}

func (w *Writer) noteWriteError(err error) {
	w.writeErrors.Add(1)
	if w.warnedIO.CompareAndSwap(false, true) {
		fmt.Fprintf(os.Stderr, "%s samantha: transcript log write failed (further I/O errors counted, not repeated): %v\n",
			time.Now().UTC().Format(time.RFC3339), err)
	}
}

// Close stops accepting events, drains the queue, records drops, and closes
// the file. Safe to call more than once.
func (w *Writer) Close() error {
	var err error
	w.once.Do(func() {
		w.mu.Lock()
		w.closed = true
		close(w.ch)
		w.mu.Unlock()
		<-w.done
		err = w.f.Close()
	})
	return err
}

func previewText(s string, max int) string {
	if max <= 0 || s == "" {
		return s
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
