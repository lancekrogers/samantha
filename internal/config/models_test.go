package config

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDownloadFileCancelledWhileBodyStalls(t *testing.T) {
	headersSent := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1048576")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(headersSent)
		// Stall the body until the client gives up.
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	path := filepath.Join(t.TempDir(), "model.bin")
	errCh := make(chan error, 1)
	go func() {
		errCh <- downloadFile(ctx, path, srv.URL, nil)
	}()

	select {
	case <-headersSent:
	case <-time.After(5 * time.Second):
		t.Fatal("server never sent headers")
	}
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("downloadFile() error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("downloadFile() did not return after context cancellation")
	}
}

func TestDownloadFileSucceedsWithContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("model-bytes"))
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "model.bin")
	if err := downloadFile(context.Background(), path, srv.URL, nil); err != nil {
		t.Fatalf("downloadFile() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got, want := string(data), "model-bytes"; got != want {
		t.Fatalf("downloaded content = %q, want %q", got, want)
	}
}

func TestSanitizeKokoroLexiconsRemovesUnsupportedMarks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lexicon-us-en.txt")
	if err := os.WriteFile(path, []byte("button b\u0329t\u0301n\n"), 0o640); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := sanitizeKokoroLexicons(dir); err != nil {
		t.Fatalf("sanitizeKokoroLexicons() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got, want := string(data), "button btn\n"; got != want {
		t.Fatalf("sanitized lexicon = %q, want %q", got, want)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o640); got != want {
		t.Fatalf("sanitized lexicon mode = %v, want %v", got, want)
	}
}

func TestSanitizeKokoroLexiconsAllowsMissingLexicons(t *testing.T) {
	if err := sanitizeKokoroLexicons(t.TempDir()); err != nil {
		t.Fatalf("sanitizeKokoroLexicons() error = %v", err)
	}
}

func TestEnsureRuntimeAssetsWarnsOnKokoroLexiconSanitizeFailure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MODELS_DIR", dir)

	for _, name := range []string{"model.onnx", "voices.bin", "tokens.txt", "espeak-ng-data"} {
		path := filepath.Join(dir, name)
		if name == "espeak-ng-data" {
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatalf("Mkdir() error = %v", err)
			}
			continue
		}
		if err := os.WriteFile(path, []byte("ok"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}

	if err := os.Mkdir(filepath.Join(dir, "lexicon-us-en.txt"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}

	oldStderr := os.Stderr
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe() error = %v", err)
	}
	os.Stderr = writePipe
	defer func() {
		os.Stderr = oldStderr
	}()

	err = EnsureRuntimeAssets(context.Background(), &Config{TTSProvider: "kokoro"}, AssetRequest{NeedTTS: true}, nil)
	if closeErr := writePipe.Close(); closeErr != nil {
		t.Fatalf("Close() error = %v", closeErr)
	}
	os.Stderr = oldStderr

	var stderr bytes.Buffer
	if _, copyErr := stderr.ReadFrom(readPipe); copyErr != nil {
		t.Fatalf("ReadFrom() error = %v", copyErr)
	}

	if err != nil {
		t.Fatalf("EnsureRuntimeAssets() error = %v, want nil", err)
	}
	if !strings.Contains(stderr.String(), "warning: could not sanitize kokoro lexicons") {
		t.Fatalf("stderr = %q, want kokoro lexicon warning", stderr.String())
	}
}
