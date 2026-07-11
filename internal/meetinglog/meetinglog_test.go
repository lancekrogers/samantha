package meetinglog

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/listen"
)

func TestWriterLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "standup-20260710-093000.log")
	w, err := Create(path, "Standup", "sherpa (offline)")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.OnUtterance(listen.Utterance{Text: "first point", At: time.Date(2026, 7, 10, 9, 30, 12, 0, time.Local)}); err != nil {
		t.Fatal(err)
	}
	if err := w.OnTimeout(); err != nil {
		t.Fatal(err)
	}
	if err := w.OnError(errors.New("session hiccup")); err != nil {
		t.Fatal(err)
	}
	if err := w.OnUtterance(listen.Utterance{Text: "second point", At: time.Date(2026, 7, 10, 9, 31, 2, 0, time.Local)}); err != nil {
		t.Fatal(err)
	}

	sum, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}
	if sum.Utterances != 2 || sum.Errors != 1 || sum.Description != "Standup" {
		t.Fatalf("summary = %+v", sum)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{
		"# Meeting: Standup",
		"# STT: sherpa (offline)",
		"[09:30:12] first point",
		"[transcription error: session hiccup]",
		"[09:31:02] second point",
		"2 utterances, 1 errors",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("log missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "timeout") {
		t.Fatal("timeouts must not be written to the log")
	}
}

func TestSummaryJSONIncludesDurationSeconds(t *testing.T) {
	w, err := Create(filepath.Join(t.TempDir(), "standup.log"), "Standup", "fake")
	if err != nil {
		t.Fatal(err)
	}
	w.started = time.Now().Add(-92 * time.Second)
	summary, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}

	data, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["duration_seconds"] != float64(92) {
		t.Fatalf("duration_seconds = %v, want 92", got["duration_seconds"])
	}
}

func TestCreateRefusesToOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.log")
	if err := os.WriteFile(path, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(path, "d", "stt"); err == nil {
		t.Fatal("expected O_EXCL collision error")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "existing" {
		t.Fatal("existing file must be untouched")
	}
}

func TestWriterReportsFailedUtteranceWithoutCountingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "failed.log")
	w, err := Create(path, "Failure test", "fake")
	if err != nil {
		t.Fatal(err)
	}
	// Closing the descriptor simulates a filesystem write failure while the
	// recorder is active.
	if err := w.f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.OnUtterance(listen.Utterance{Text: "must not be counted", At: time.Now()}); err == nil {
		t.Fatal("OnUtterance must return the persistence failure")
	}
	if w.utterances != 0 {
		t.Fatalf("utterances = %d, want 0 after failed write", w.utterances)
	}
}
