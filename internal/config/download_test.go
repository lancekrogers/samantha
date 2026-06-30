package config

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// noLeftoverTemp asserts the target dir contains only the final file (no .part
// temp files left behind).
func noLeftoverTemp(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // dir may not exist on early failure
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".part") {
			t.Errorf("leftover temp file %q in %s", e.Name(), dir)
		}
	}
}

func TestDownloadFileNestedPathAtomicAndClean(t *testing.T) {
	const body = "model-bytes"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	base := t.TempDir()
	path := filepath.Join(base, "whispercpp", "ggml-base.en.bin")
	if err := downloadFile(path, srv.URL, "ggml-base.en.bin", 0, "", nil); err != nil {
		t.Fatalf("downloadFile() error = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != body {
		t.Fatalf("downloaded = %q err=%v, want %q", got, err, body)
	}
	noLeftoverTemp(t, filepath.Dir(path))
}

func TestDownloadFileHTTPErrorLeavesNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	base := t.TempDir()
	path := filepath.Join(base, "model.bin")
	err := downloadFile(path, srv.URL, "model.bin", 0, "", nil)
	if err == nil || !strings.Contains(err.Error(), "model.bin") {
		t.Fatalf("error = %v, want it to name the asset", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("final file should not exist after an HTTP error")
	}
	noLeftoverTemp(t, base)
}

func TestDownloadFileSizeMismatchLeavesNothing(t *testing.T) {
	const body = "twelve-bytes" // 12 bytes
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	base := t.TempDir()
	path := filepath.Join(base, "model.bin")
	err := downloadFile(path, srv.URL, "model.bin", 999, "", nil)
	if err == nil || !strings.Contains(err.Error(), "size mismatch") {
		t.Fatalf("error = %v, want a size mismatch", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("final file should not exist after a size mismatch")
	}
	noLeftoverTemp(t, base)
}

func TestDownloadFileChecksumVerified(t *testing.T) {
	const body = "checksum-me"
	sum := sha256.Sum256([]byte(body))
	good := hex.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	base := t.TempDir()
	okPath := filepath.Join(base, "ok.bin")
	if err := downloadFile(okPath, srv.URL, "ok.bin", 0, good, nil); err != nil {
		t.Fatalf("matching checksum should succeed, got %v", err)
	}

	badPath := filepath.Join(base, "bad.bin")
	err := downloadFile(badPath, srv.URL, "bad.bin", 0, "deadbeef", nil)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("error = %v, want a checksum mismatch", err)
	}
	if _, statErr := os.Stat(badPath); !os.IsNotExist(statErr) {
		t.Error("final file should not exist after a checksum mismatch")
	}
	noLeftoverTemp(t, base)
}

// interruptedHandler sends a Content-Length larger than the body it writes, then
// hijacks and closes the connection so the client sees an unexpected EOF.
func interruptedHandler(w http.ResponseWriter, r *http.Request) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "no hijack", http.StatusInternalServerError)
		return
	}
	conn, bufrw, err := hj.Hijack()
	if err != nil {
		return
	}
	_, _ = bufrw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\nshort")
	_ = bufrw.Flush()
	_ = conn.Close()
}

func TestDownloadFileInterruptedLeavesNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(interruptedHandler))
	defer srv.Close()

	base := t.TempDir()
	path := filepath.Join(base, "model.bin")
	err := downloadFile(path, srv.URL, "model.bin", 0, "", nil)
	if err == nil {
		t.Fatal("interrupted download should return an error")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("final file should not exist after an interrupted download")
	}
	noLeftoverTemp(t, base)
}

// TestDownloadFileRetryAfterFailureSucceeds proves a failed download leaves the
// target clean enough that a retry installs the file — a partial is never
// mistaken for an installed asset.
func TestDownloadFileRetryAfterFailureSucceeds(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "whispercpp", "ggml-base.en.bin")

	bad := httptest.NewServer(http.HandlerFunc(interruptedHandler))
	if err := downloadFile(path, bad.URL, "ggml-base.en.bin", 0, "", nil); err == nil {
		t.Fatal("first (interrupted) download should fail")
	}
	bad.Close()
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatal("a failed download must not leave a file that looks installed")
	}

	const body = "good-model"
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer good.Close()
	if err := downloadFile(path, good.URL, "ggml-base.en.bin", 0, "", nil); err != nil {
		t.Fatalf("retry after failure should succeed, got %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != body {
		t.Fatalf("retry result = %q err=%v, want %q", got, err, body)
	}
	noLeftoverTemp(t, filepath.Dir(path))
}
