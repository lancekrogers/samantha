package meetinglog

import (
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
	w.OnUtterance(listen.Utterance{Text: "first point", At: time.Date(2026, 7, 10, 9, 30, 12, 0, time.Local)})
	w.OnTimeout()
	w.OnError(errors.New("session hiccup"))
	w.OnUtterance(listen.Utterance{Text: "second point", At: time.Date(2026, 7, 10, 9, 31, 2, 0, time.Local)})

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
