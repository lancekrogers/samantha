package log

import (
	"bufio"
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
	bundle := filepath.Join(t.TempDir(), "standup-20260710-093000.meeting")
	w, err := CreateBundle(bundle, "Standup", "sherpa (offline)")
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
	if err := w.AddNote("follow up with finance"); err != nil {
		t.Fatal(err)
	}
	if err := w.AddBookmark("important", "budget decision"); err != nil {
		t.Fatal(err)
	}

	sum, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}
	if sum.Utterances != 2 || sum.Errors != 1 || sum.Notes != 1 || sum.Bookmarks != 1 || sum.Description != "Standup" {
		t.Fatalf("summary = %+v", sum)
	}
	if sum.Bundle != bundle || sum.JSONLFile == "" || sum.File != w.Path() {
		t.Fatalf("paths: bundle=%q file=%q jsonl=%q", sum.Bundle, sum.File, sum.JSONLFile)
	}

	data, err := os.ReadFile(w.Path())
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{
		"# Meeting: Standup",
		"# STT: sherpa (offline)",
		"# JSONL:",
		"[09:30:12] first point",
		"[transcription error: session hiccup]",
		"[09:31:02] second point",
		"📝 note: follow up with finance",
		"★ IMPORTANT: budget decision",
		"2 utterances, 1 notes, 1 bookmarks, 1 errors",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("log missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "timeout") {
		t.Fatal("timeouts must not be written to the log")
	}

	// JSONL must contain typed events with offsets.
	events := readJSONL(t, sum.JSONLFile)
	kinds := map[string]int{}
	for _, e := range events {
		kinds[e.Type]++
		if e.Type != TypeSessionStart && e.Type != TypeSessionEnd && e.OffsetMs < 0 {
			t.Fatalf("negative offset on %+v", e)
		}
	}
	for _, want := range []string{TypeSessionStart, TypeUtterance, TypeNote, TypeBookmark, TypeError, TypeSessionEnd} {
		if kinds[want] == 0 {
			t.Fatalf("jsonl missing type %s: %v", want, kinds)
		}
	}
	if kinds[TypeUtterance] != 2 {
		t.Fatalf("utterance events = %d", kinds[TypeUtterance])
	}
}

func TestCreateBundleGroupsMeetingArtifacts(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "standup-20260722-090000.meeting")
	w, err := CreateBundle(bundle, "Standup", "fake")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.OnUtterance(listen.Utterance{Text: "hello team", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	summary, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}
	wantDocument := filepath.Join(bundle, BundleDocumentName)
	wantEvents := filepath.Join(bundle, BundleInternalDirName, BundleEventsName)
	if summary.Bundle != bundle || summary.File != wantDocument || summary.JSONLFile != wantEvents {
		t.Fatalf("bundle summary = %+v", summary)
	}
	entries, err := os.ReadDir(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name() != BundleInternalDirName || entries[1].Name() != BundleDocumentName {
		t.Fatalf("bundle entries = %#v", entries)
	}
	for _, path := range []string{bundle, filepath.Join(bundle, BundleInternalDirName)} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s mode = %04o", path, info.Mode().Perm())
		}
	}
	if _, err := CreateBundle(bundle, "collision", "fake"); err == nil {
		t.Fatal("existing bundle must not be reused")
	}
}

func readJSONL(t *testing.T, path string) []Event {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("jsonl line: %v", err)
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSummaryJSONIncludesDurationSeconds(t *testing.T) {
	w, err := CreateBundle(filepath.Join(t.TempDir(), "standup.meeting"), "Standup", "fake")
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
	if got["notes"] != float64(0) || got["bookmarks"] != float64(0) {
		t.Fatalf("notes/bookmarks missing: %v", got)
	}
}

func TestCreateBundleRefusesToOverwrite(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "x.meeting")
	if err := os.Mkdir(bundle, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(bundle, "keep.txt")
	if err := os.WriteFile(sentinel, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateBundle(bundle, "d", "stt"); err == nil {
		t.Fatal("expected O_EXCL collision error")
	}
	data, _ := os.ReadFile(sentinel)
	if string(data) != "existing" {
		t.Fatal("existing file must be untouched")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "once.meeting")
	w, err := CreateBundle(bundle, "Once", "fake")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.OnUtterance(listen.Utterance{Text: "hello", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	sum1, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}
	sum2, err := w.Close()
	if err != nil {
		t.Fatalf("second Close must be nil: %v", err)
	}
	if sum1.Utterances != 1 || sum2.Utterances != 1 {
		t.Fatalf("summaries = %+v / %+v", sum1, sum2)
	}
	// Must not double-append trailer.
	data, err := os.ReadFile(w.Path())
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), "# Ended:"); n != 1 {
		t.Fatalf("Ended trailer count = %d, want 1", n)
	}
}

func TestCreateBundleUsesOwnerOnlyPermissions(t *testing.T) {
	// Meeting transcripts are private (personal speech / credentials spoken
	// aloud). CreateBundle must not leave world-readable artifacts.
	bundle := filepath.Join(t.TempDir(), "private.meeting")
	w, err := CreateBundle(bundle, "Private", "fake")
	if err != nil {
		t.Fatal(err)
	}
	jsonl := w.JSONLPath()
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{w.Path(), jsonl} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		mode := st.Mode().Perm()
		if mode&0o077 != 0 {
			t.Fatalf("%s mode = %04o, want owner-only (no group/other bits)", p, mode)
		}
		if mode&0o600 != 0o600 {
			t.Fatalf("%s mode = %04o, want owner read+write", p, mode)
		}
	}
}

func TestWriterReportsFailedUtteranceWithoutCountingIt(t *testing.T) {
	w, err := CreateBundle(filepath.Join(t.TempDir(), "failed.meeting"), "Failure test", "fake")
	if err != nil {
		t.Fatal(err)
	}
	// Closing the descriptor simulates a filesystem write failure while the
	// recorder is active.
	if err := w.log.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.OnUtterance(listen.Utterance{Text: "must not be counted", At: time.Now()}); err == nil {
		t.Fatal("OnUtterance must return the persistence failure")
	}
	if w.utterances != 0 {
		t.Fatalf("utterances = %d, want 0 after failed write", w.utterances)
	}
}

func TestAddNoteEmptyIsNoop(t *testing.T) {
	w, err := CreateBundle(filepath.Join(t.TempDir(), "n.meeting"), "n", "fake")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.AddNote("  "); err != nil {
		t.Fatal(err)
	}
	if w.notes != 0 {
		t.Fatalf("notes = %d", w.notes)
	}
	_, _ = w.Close()
}
