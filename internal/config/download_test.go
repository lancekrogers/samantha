package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	if err := downloadFile(t.Context(), path, srv.URL, "ggml-base.en.bin", 0, "", nil); err != nil {
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
	err := downloadFile(t.Context(), path, srv.URL, "model.bin", 0, "", nil)
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
	err := downloadFile(t.Context(), path, srv.URL, "model.bin", 999, "", nil)
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
	if err := downloadFile(t.Context(), okPath, srv.URL, "ok.bin", 0, good, nil); err != nil {
		t.Fatalf("matching checksum should succeed, got %v", err)
	}

	badPath := filepath.Join(base, "bad.bin")
	err := downloadFile(t.Context(), badPath, srv.URL, "bad.bin", 0, "deadbeef", nil)
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
	err := downloadFile(t.Context(), path, srv.URL, "model.bin", 0, "", nil)
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
	if err := downloadFile(t.Context(), path, bad.URL, "ggml-base.en.bin", 0, "", nil); err == nil {
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
	if err := downloadFile(t.Context(), path, good.URL, "ggml-base.en.bin", 0, "", nil); err != nil {
		t.Fatalf("retry after failure should succeed, got %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != body {
		t.Fatalf("retry result = %q err=%v, want %q", got, err, body)
	}
	noLeftoverTemp(t, filepath.Dir(path))
}

// TestDownloadFileHonorsContextCancellation guards the download client
// migration: a bare http.Get could hang forever on a wedged host, so a
// canceled context must abort the request promptly and leave nothing behind.
func TestDownloadFileHonorsContextCancellation(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-release // hold the body open: only cancellation can unblock the client
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(t.Context())
	dir := t.TempDir()
	path := filepath.Join(dir, "model.bin")
	errCh := make(chan error, 1)
	go func() { errCh <- downloadFile(ctx, path, srv.URL, "model.bin", 0, "", nil) }()

	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("downloadFile() error = nil, want cancellation error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("downloadFile did not abort after context cancellation")
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Fatal("canceled download must not land at the target path")
	}
	noLeftoverTemp(t, dir)
}
