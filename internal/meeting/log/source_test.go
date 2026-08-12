package log

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateBundleRecordsSource(t *testing.T) {
	w, err := CreateBundle(filepath.Join(t.TempDir(), "s.meeting"), "Standup", "fake", "watch")
	if err != nil {
		t.Fatal(err)
	}
	sum, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}
	doc, err := os.ReadFile(sum.File)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), "# Source: watch") {
		t.Fatalf("document missing source header:\n%s", doc)
	}
	events := readJSONL(t, sum.JSONLFile)
	if len(events) == 0 || events[0].Type != TypeSessionStart || events[0].Source != "watch" {
		t.Fatalf("session_start missing source: %+v", events)
	}
}

func TestCreateBundleOmitsEmptySource(t *testing.T) {
	w, err := CreateBundle(filepath.Join(t.TempDir(), "s.meeting"), "Standup", "fake", "")
	if err != nil {
		t.Fatal(err)
	}
	sum, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}
	doc, err := os.ReadFile(sum.File)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(doc), "# Source:") {
		t.Fatalf("empty source must not write a header:\n%s", doc)
	}
	events := readJSONL(t, sum.JSONLFile)
	if len(events) == 0 || events[0].Source != "" {
		t.Fatalf("empty source leaked into events: %+v", events)
	}
}
