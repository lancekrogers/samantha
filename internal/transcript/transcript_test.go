package transcript

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/events"
)

func readRecords(t *testing.T, path string) []record {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	var out []record
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("bad line %q: %v", sc.Text(), err)
		}
		out = append(out, r)
	}
	return out
}

func TestWriterRecordsConversationFlow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	bus := events.NewBus()
	w.Attach(bus)

	bus.Emit(events.UserInput{Text: "hello there"})
	bus.Emit(events.ThinkingStarted{})
	bus.Emit(events.ResponseDelta{Text: "hi "})
	bus.Emit(events.ToolCallStarted{Name: "run_command", Summary: "ls"})
	bus.Emit(events.ToolCallFinished{Name: "run_command", Err: "exit 1"})
	bus.Emit(events.Error{Stage: "brain", Message: "ollama stream: boom"})
	bus.Emit(events.ResponseReady{Response: "hi, recovered", Degraded: true})
	bus.Emit(events.SpeakingStarted{Text: "hi"})
	bus.Emit(events.SpeakingComplete{Elapsed: 1200 * time.Millisecond})
	bus.Emit(events.TurnMetrics{Outcome: "completed", Degraded: true, ModelCompleteElapsed: 19 * time.Second})

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	recs := readRecords(t, path)
	if len(recs) != 11 {
		t.Fatalf("got %d records, want 11:\n%+v", len(recs), recs)
	}
	if recs[0].Type != "header" || recs[0].V != Version {
		t.Fatalf("first record = %+v, want header v%d", recs[0], Version)
	}
	types := make([]string, 0, len(recs))
	lastSeq := int64(0)
	for _, r := range recs {
		types = append(types, r.Type)
		if r.Seq <= lastSeq {
			t.Fatalf("seq not monotonic: %+v", recs)
		}
		lastSeq = r.Seq
		if _, err := time.Parse(time.RFC3339Nano, r.TS); err != nil {
			t.Fatalf("bad ts on %+v: %v", r, err)
		}
	}
	want := []string{"header", "user", "thinking", "agent_delta", "tool_call", "tool_result", "error", "agent", "speaking", "speaking", "metrics"}
	for i, ty := range want {
		if types[i] != ty {
			t.Fatalf("record %d type = %q, want %q (all: %v)", i, types[i], ty, types)
		}
	}

	if r := recs[5]; !r.IsError || r.Message != "exit 1" {
		t.Fatalf("tool_result = %+v, want is_error + message", r)
	}
	if r := recs[6]; r.Stage != "brain" || r.Message != "ollama stream: boom" {
		t.Fatalf("error record = %+v", r)
	}
	if r := recs[7]; !r.Degraded || r.Text != "hi, recovered" {
		t.Fatalf("agent record = %+v", r)
	}
	if r := recs[10]; r.Outcome != "completed" || !r.Degraded || r.ModelS != 19.0 {
		t.Fatalf("metrics record = %+v", r)
	}
}

func TestWriterNeverBlocksAndCountsDrops(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Writer with a full queue and no drain goroutine yet: enqueue must
	// return immediately and count the drop.
	w := &Writer{f: f, ch: make(chan record, 1), done: make(chan struct{})}
	w.ch <- record{Type: "user", Text: "kept", TS: time.Now().UTC().Format(time.RFC3339Nano), Seq: 1}
	w.seq.Store(1)

	done := make(chan struct{})
	go func() {
		w.enqueue(record{Type: "user", Text: "dropped"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("enqueue blocked on a full queue")
	}

	go w.run()
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	recs := readRecords(t, path)
	if len(recs) != 2 {
		t.Fatalf("got %d records, want kept + dropped: %+v", len(recs), recs)
	}
	if recs[0].Text != "kept" {
		t.Fatalf("first record = %+v", recs[0])
	}
	if recs[1].Type != "dropped" || recs[1].Dropped != 1 {
		t.Fatalf("drop record = %+v", recs[1])
	}
}

func TestWriterIgnoresEventsAfterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	bus := events.NewBus()
	w.Attach(bus)
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// The bus outlives the conversation; late events must be dropped, not
	// panic on a closed channel or reopen the file.
	bus.Emit(events.UserInput{Text: "late"})
	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	recs := readRecords(t, path)
	if len(recs) != 1 || recs[0].Type != "header" {
		t.Fatalf("got %+v, want header only", recs)
	}
}

func TestNewWriterFailsOnUnwritablePath(t *testing.T) {
	if _, err := NewWriter(filepath.Join(t.TempDir(), "missing", "t.jsonl")); err == nil {
		t.Fatal("expected error for unwritable path — a harness must not run blind")
	}
}
