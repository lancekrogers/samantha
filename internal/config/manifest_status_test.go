package config

import (
	"os"
	"path/filepath"
	"testing"
)

func touchFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// defaultStatusManifest is the interactive default set: VAD + sherpa offline +
// Kokoro TTS.
func defaultStatusManifest(t *testing.T) AssetManifest {
	t.Helper()
	cfg := &Config{STTProvider: "sherpa", WhisperModel: "base.en", TTSProvider: "kokoro", VADEnabled: true}
	m, err := ManifestFor(cfg, AssetRequest{NeedSTT: true, NeedTTS: true, NeedVAD: true})
	if err != nil {
		t.Fatalf("ManifestFor() error = %v", err)
	}
	return m
}

func statusByID(statuses []AssetStatus) map[string]AssetStatus {
	out := make(map[string]AssetStatus, len(statuses))
	for _, s := range statuses {
		out[s.ID] = s
	}
	return out
}

func TestManifestStatusReportsMissing(t *testing.T) {
	dir := t.TempDir()
	statuses := defaultStatusManifest(t).Status(dir)
	if len(statuses) != 3 {
		t.Fatalf("statuses = %d, want 3", len(statuses))
	}
	for _, s := range statuses {
		if s.Installed {
			t.Errorf("%s: Installed = true on empty dir, want false", s.ID)
		}
		if len(s.Missing) == 0 {
			t.Errorf("%s: Missing is empty, want the absent paths listed", s.ID)
		}
	}
}

func TestManifestStatusReportsPresent(t *testing.T) {
	dir := t.TempDir()
	m := defaultStatusManifest(t)

	// Create every required install path.
	for _, a := range m.Assets {
		for _, p := range a.installPaths(dir) {
			touchFile(t, p)
		}
	}

	for _, s := range m.Status(dir) {
		if !s.Installed {
			t.Errorf("%s: Installed = false after creating all files, missing %v", s.ID, s.Missing)
		}
	}
}

func TestManifestStatusReportsPartial(t *testing.T) {
	dir := t.TempDir()
	m := defaultStatusManifest(t)

	// Install only the VAD asset.
	for _, a := range m.Assets {
		if a.Kind != AssetKindVAD {
			continue
		}
		for _, p := range a.installPaths(dir) {
			touchFile(t, p)
		}
	}

	byID := statusByID(m.Status(dir))
	if !byID["vad.silero.v1"].Installed {
		t.Error("VAD should be installed")
	}
	for id, s := range byID {
		if id == "vad.silero.v1" {
			continue
		}
		if s.Installed {
			t.Errorf("%s: Installed = true, want false (not created)", id)
		}
	}
}

// TestManifestStatusIsReadOnly confirms Status never creates files.
func TestManifestStatusIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	_ = defaultStatusManifest(t).Status(dir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("Status created %d entries in the models dir, want 0 (read-only)", len(entries))
	}
}
