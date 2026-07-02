package config

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func testStatusManifest() AssetManifest {
	return AssetManifest{
		Schema: AssetSchema,
		Assets: []Asset{
			{
				ID:       "vad.test",
				Provider: "sherpa",
				Kind:     AssetKindVAD,
				Name:     "vad.onnx",
				Files:    []AssetFile{{Path: "vad.onnx"}},
			},
			{
				ID:         "tts.test",
				Provider:   "kokoro",
				Kind:       AssetKindTTS,
				Name:       "kokoro-tts",
				Archive:    &AssetArchive{URL: "https://example.invalid/kokoro.tar.bz2"},
				CheckFiles: []string{"model.onnx"},
			},
		},
	}
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
	m := testStatusManifest()

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
	m := testStatusManifest()

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
	if !byID["vad.test"].Installed {
		t.Error("VAD should be installed")
	}
	for id, s := range byID {
		if id == "vad.test" {
			continue
		}
		if s.Installed {
			t.Errorf("%s: Installed = true, want false (not created)", id)
		}
	}
}

func TestManifestStatusRejectsCorruptPinnedFile(t *testing.T) {
	const good = "verified-model"
	sum := sha256.Sum256([]byte(good))
	dir := t.TempDir()
	path := filepath.Join(dir, "model.bin")
	if err := os.WriteFile(path, []byte("corrupt-model!"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := AssetManifest{Schema: AssetSchema, Assets: []Asset{{
		ID:       "file.test",
		Provider: "whispercpp",
		Kind:     AssetKindSTT,
		Name:     "model.bin",
		Files:    []AssetFile{{Path: "model.bin", Size: int64(len(good)), SHA256: fmt.Sprintf("%x", sum)}},
	}}}

	st := m.Status(dir)[0]
	if st.Installed {
		t.Fatal("corrupt same-size pinned file reported installed")
	}
	if len(st.Missing) != 1 || st.Missing[0] != path {
		t.Fatalf("Missing = %v, want %s", st.Missing, path)
	}
}

func TestManifestStatusRejectsPinnedArchiveWithoutValidMarker(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "model.onnx"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := AssetManifest{Schema: AssetSchema, Assets: []Asset{{
		ID:         "archive.test",
		Provider:   "kokoro",
		Kind:       AssetKindTTS,
		Name:       "kokoro-tts",
		Archive:    &AssetArchive{URL: "https://example.invalid/kokoro.tar.bz2", SHA256: strings.Repeat("a", 64)},
		CheckFiles: []string{"model.onnx"},
	}}}

	st := m.Status(dir)[0]
	if st.Installed {
		t.Fatal("pinned archive without install marker reported installed")
	}

	if err := writeArchiveInstallMarker(dir, "archive.test", m.Assets[0].Archive.URL, m.Assets[0].Archive.SHA256, m.Assets[0].CheckFiles); err != nil {
		t.Fatalf("writeArchiveInstallMarker() error = %v", err)
	}
	if st := m.Status(dir)[0]; !st.Installed {
		t.Fatalf("archive with valid marker reported missing: %v", st.Missing)
	}

	if err := os.WriteFile(filepath.Join(dir, "model.onnx"), []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := m.Status(dir)[0]; st.Installed {
		t.Fatal("archive with corrupted check file reported installed")
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
