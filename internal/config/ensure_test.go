package config

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fileAssetManifest builds a one-file manifest pointing at url.
func fileAssetManifest(url string) AssetManifest {
	return AssetManifest{
		Schema: AssetSchema,
		Assets: []Asset{{
			ID: "stt.whispercpp.base.en", Provider: "whispercpp", Mode: "cli", Kind: AssetKindSTT,
			Name:  "whispercpp-base.en",
			Files: []AssetFile{{Path: "whispercpp/ggml-base.en.bin", URL: url}},
		}},
	}
}

func pinnedFileAssetManifest(url, sha string, size int64) AssetManifest {
	m := fileAssetManifest(url)
	m.Assets[0].Files[0].SHA256 = sha
	m.Assets[0].Files[0].Size = size
	return m
}

func TestEnsureManifestDownloadsMissingFile(t *testing.T) {
	const body = "fake-model-bytes"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	dir := t.TempDir()
	var progressed bool
	err := ensureManifest(t.Context(), fileAssetManifest(srv.URL), dir, func(name string, pct float64) {
		progressed = true
	})
	if err != nil {
		t.Fatalf("ensureManifest() error = %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "whispercpp", "ggml-base.en.bin"))
	if err != nil {
		t.Fatalf("downloaded file missing: %v", err)
	}
	if string(got) != body {
		t.Errorf("downloaded content = %q, want %q", got, body)
	}
	if !progressed {
		t.Error("progress callback was never invoked")
	}
}

func TestEnsureManifestSkipsPresentFile(t *testing.T) {
	// The server always fails; if ensureManifest tried to download it would
	// error. A pre-existing file must be detected and skipped.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "whispercpp", "ggml-base.en.bin")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("already-here"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ensureManifest(t.Context(), fileAssetManifest(srv.URL), dir, nil); err != nil {
		t.Fatalf("ensureManifest() error = %v, want nil (present file skipped)", err)
	}
}

func TestEnsureManifestRedownloadsCorruptPinnedFile(t *testing.T) {
	const good = "verified-model"
	sum := sha256.Sum256([]byte(good))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(good))
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "whispercpp", "ggml-base.en.bin")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupt-model!"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := pinnedFileAssetManifest(srv.URL, fmt.Sprintf("%x", sum), int64(len(good)))
	if err := ensureManifest(t.Context(), m, dir, nil); err != nil {
		t.Fatalf("ensureManifest() error = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != good {
		t.Fatalf("file content = %q, want redownloaded verified content", got)
	}
}

func TestEnsureManifestSkipsExtractedArchive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	dir := t.TempDir()
	// Pre-create the archive's check file so it is considered extracted.
	if err := os.WriteFile(filepath.Join(dir, "model.onnx"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := AssetManifest{
		Schema: AssetSchema,
		Assets: []Asset{{
			ID: "tts.kokoro", Provider: "kokoro", Kind: AssetKindTTS, Name: "kokoro-tts",
			Archive:    &AssetArchive{URL: srv.URL},
			CheckFiles: []string{"model.onnx"},
		}},
	}
	if err := ensureManifest(t.Context(), m, dir, nil); err != nil {
		t.Fatalf("ensureManifest() error = %v, want nil (extracted archive skipped)", err)
	}
}

func TestEnsureManifestAdoptsPinnedArchiveWithoutMarker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "model.onnx"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := AssetManifest{
		Schema: AssetSchema,
		Assets: []Asset{{
			ID: "tts.kokoro", Provider: "kokoro", Kind: AssetKindTTS, Name: "kokoro-tts",
			Archive:    &AssetArchive{URL: srv.URL, SHA256: strings.Repeat("a", 64)},
			CheckFiles: []string{"model.onnx"},
		}},
	}
	if err := ensureManifest(t.Context(), m, dir, nil); err != nil {
		t.Fatalf("ensureManifest() error = %v, want nil (markerless extracted archive adopted)", err)
	}
	if !archiveInstallMarkerValid(dir, "tts.kokoro", srv.URL, strings.Repeat("a", 64), []string{"model.onnx"}) {
		t.Fatal("ensureManifest() did not write a valid install marker for the extracted archive")
	}
}

func TestEnsureManifestDoesNotAdoptPinnedArchiveWithInvalidMarker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "model.onnx"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archiveInstallMarkerPath(dir, "tts.kokoro"), []byte(`{"id":"tts.kokoro","url":"wrong"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	m := AssetManifest{
		Schema: AssetSchema,
		Assets: []Asset{{
			ID: "tts.kokoro", Provider: "kokoro", Kind: AssetKindTTS, Name: "kokoro-tts",
			Archive:    &AssetArchive{URL: srv.URL, SHA256: strings.Repeat("a", 64)},
			CheckFiles: []string{"model.onnx"},
		}},
	}
	if err := ensureManifest(t.Context(), m, dir, nil); err == nil {
		t.Fatal("ensureManifest() error = nil, want pinned archive with invalid marker to redownload")
	}
}

func TestEnsureManifestFileDownloadErrorNamesAsset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	err := ensureManifest(t.Context(), fileAssetManifest(srv.URL), t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "ggml-base.en.bin") {
		t.Fatalf("error = %v, want it to name the failing asset file", err)
	}
}

func TestEnsureManifestArchiveChecksumMismatchLeavesMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not-the-expected-archive"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	m := AssetManifest{
		Schema: AssetSchema,
		Assets: []Asset{{
			ID: "tts.kokoro", Provider: "kokoro", Kind: AssetKindTTS, Name: "kokoro-tts",
			Archive:    &AssetArchive{URL: srv.URL, SHA256: "deadbeef"},
			CheckFiles: []string{"model.onnx"},
		}},
	}

	err := ensureManifest(t.Context(), m, dir, nil)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("error = %v, want archive checksum mismatch", err)
	}
	if m.Status(dir)[0].Installed {
		t.Fatal("a failed checksum must leave the archive asset missing")
	}
}
