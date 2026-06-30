package config

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func noLeftoverExtractDirs(t *testing.T, dir string) {
	t.Helper()
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".extract-") || strings.Contains(e.Name(), ".part") {
			t.Errorf("leftover temp artifact %q in %s", e.Name(), dir)
		}
	}
}

// TestRecoveryFileDownloadThenRetry: a failed file download leaves the asset
// reported missing (no partial), and a retry installs it and reports installed.
func TestRecoveryFileDownloadThenRetry(t *testing.T) {
	dir := t.TempDir()
	var ok atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ok.Load() {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("model"))
	}))
	defer srv.Close()

	m := AssetManifest{Schema: AssetSchema, Assets: []Asset{{
		ID: "stt.whispercpp.base.en", Provider: "whispercpp", Mode: "cli", Kind: AssetKindSTT,
		Name:  "whispercpp-base.en",
		Files: []AssetFile{{Path: "whispercpp/ggml-base.en.bin", URL: srv.URL}},
	}}}

	if m.Status(dir)[0].Installed {
		t.Fatal("asset should start missing")
	}
	if err := ensureManifest(m, dir, nil); err == nil {
		t.Fatal("first attempt should fail")
	}
	if m.Status(dir)[0].Installed {
		t.Fatal("a failed download must leave the asset missing")
	}
	noLeftoverExtractDirs(t, dir)

	ok.Store(true)
	if err := ensureManifest(m, dir, nil); err != nil {
		t.Fatalf("retry should succeed, got %v", err)
	}
	if !m.Status(dir)[0].Installed {
		t.Fatal("retry should install the asset")
	}
}

// TestRecoveryArchiveDownloadFailureLeavesMissing: a failed archive download
// reports the asset missing and leaves no temp dirs.
func TestRecoveryArchiveDownloadFailureLeavesMissing(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	m := AssetManifest{Schema: AssetSchema, Assets: []Asset{{
		ID: "tts.kokoro", Provider: "kokoro", Kind: AssetKindTTS, Name: "kokoro-tts",
		Archive:    &AssetArchive{URL: srv.URL, StripPrefix: true},
		CheckFiles: []string{"model.onnx"},
	}}}

	if err := ensureManifest(m, dir, nil); err == nil {
		t.Fatal("archive download should fail")
	}
	if m.Status(dir)[0].Installed {
		t.Fatal("a failed archive must leave the asset missing")
	}
	noLeftoverExtractDirs(t, dir)
}

// TestRecoveryCorruptArchiveLeavesMissing: a 200 response with non-bzip2 bytes
// fails extraction and leaves the asset missing with no partial state.
func TestRecoveryCorruptArchiveLeavesMissing(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("this is not a valid tar.bz2 archive"))
	}))
	defer srv.Close()

	m := AssetManifest{Schema: AssetSchema, Assets: []Asset{{
		ID: "tts.kokoro", Provider: "kokoro", Kind: AssetKindTTS, Name: "kokoro-tts",
		Archive:    &AssetArchive{URL: srv.URL, StripPrefix: true},
		CheckFiles: []string{"model.onnx"},
	}}}

	if err := ensureManifest(m, dir, nil); err == nil {
		t.Fatal("corrupt archive should fail extraction")
	}
	if m.Status(dir)[0].Installed {
		t.Fatal("a corrupt archive must leave the asset missing")
	}
	noLeftoverExtractDirs(t, dir)
}
