// Package main validates the recorded utterance corpus manifest.
//
// The corpus feeds the committed latency baseline and the batch-vs-streaming
// STT accuracy comparison. Both read testdata/corpus/manifest.json, so a
// manifest that references a missing file or omits an expected transcript would
// silently shrink either measurement rather than failing it.
//
// Recording is a human step, so an empty corpus is a valid state and skips
// rather than fails. Whatever is present is validated strictly.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	manifestSchema = "samantha.corpus.v1"
	corpusRelPath  = "../../testdata/corpus"
)

type sample struct {
	Path     string `json:"path"`
	Expect   string `json:"expect"`
	Category string `json:"category"`
	Notes    string `json:"notes"`
}

type manifest struct {
	Schema      string            `json:"schema"`
	Description string            `json:"description"`
	Categories  map[string]string `json:"categories"`
	Samples     []sample          `json:"samples"`
}

func loadManifest(t *testing.T) manifest {
	t.Helper()
	path := filepath.Join(corpusRelPath, "manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading corpus manifest: %v", err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parsing corpus manifest: %v", err)
	}
	return m
}

// Error cases first: a malformed manifest must fail loudly rather than
// degrading the measurements that read it.

func TestManifestSchema(t *testing.T) {
	m := loadManifest(t)
	if m.Schema != manifestSchema {
		t.Errorf("schema = %q, want %q", m.Schema, manifestSchema)
	}
	for _, c := range []string{"short_command", "thoughtful_pause", "noisy_room"} {
		if strings.TrimSpace(m.Categories[c]) == "" {
			t.Errorf("category %q has no description", c)
		}
	}
}

func TestSamplesAreWellFormed(t *testing.T) {
	m := loadManifest(t)
	if len(m.Samples) == 0 {
		t.Skip("corpus not recorded yet — recording is a human step (just bench record)")
	}

	known := map[string]bool{"short_command": true, "thoughtful_pause": true, "noisy_room": true}
	seen := map[string]bool{}

	for i, s := range m.Samples {
		if strings.TrimSpace(s.Path) == "" {
			t.Errorf("sample %d: empty path", i)
			continue
		}
		if seen[s.Path] {
			t.Errorf("sample %d: duplicate path %q", i, s.Path)
		}
		seen[s.Path] = true

		// A referenced file that does not exist would shrink the measurement
		// silently, which is the failure this test exists to prevent.
		if _, err := os.Stat(filepath.Join(corpusRelPath, s.Path)); err != nil {
			t.Errorf("sample %d (%s): file missing: %v", i, s.Path, err)
		}
		// Accuracy is scored against this string; blank means unscoreable.
		if strings.TrimSpace(s.Expect) == "" {
			t.Errorf("sample %d (%s): empty expect", i, s.Path)
		}
		if !known[s.Category] {
			t.Errorf("sample %d (%s): unknown category %q", i, s.Path, s.Category)
		}
	}
}

// TestCategoryCoverage guards the corpus property that actually matters for
// R-L1: a corpus of only crisp short commands makes a shorter VAD silence
// window look safe, because nothing in it can be truncated mid-sentence.
func TestCategoryCoverage(t *testing.T) {
	m := loadManifest(t)
	if len(m.Samples) == 0 {
		t.Skip("corpus not recorded yet — recording is a human step (just bench record)")
	}

	counts := map[string]int{}
	for _, s := range m.Samples {
		counts[s.Category]++
	}
	for _, c := range []string{"short_command", "thoughtful_pause", "noisy_room"} {
		if counts[c] == 0 {
			t.Errorf("category %q has no samples; all three are required before the corpus gates a latency decision", c)
		}
	}
}
