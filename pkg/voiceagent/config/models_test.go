package config

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/pkg/voiceagent/qwen"
)

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
	m, err := ManifestFor(&Config{TTSProvider: "kokoro"}, AssetRequest{NeedTTS: true})
	if err != nil {
		t.Fatalf("ManifestFor() error = %v", err)
	}
	if err := writeArchiveInstallMarker(dir, m.Assets[0].ID, m.Assets[0].Archive.URL, m.Assets[0].Archive.SHA256, m.Assets[0].CheckFiles); err != nil {
		t.Fatalf("writeArchiveInstallMarker() error = %v", err)
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

	err = EnsureRuntimeAssets(t.Context(), &Config{TTSProvider: "kokoro"}, AssetRequest{NeedTTS: true}, nil)
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

// --- AssetProgress (MDL-A1) ---

func TestClampPct(t *testing.T) {
	cases := []struct {
		in, want float64
	}{
		{-5, 0}, {0, 0}, {50, 50}, {100, 100}, {104.7, 100}, {-0.1, 0},
	}
	for _, c := range cases {
		if got := clampPct(c.in); got != c.want {
			t.Errorf("clampPct(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestPctOfBytesUnknownTotal(t *testing.T) {
	if got := pctOfBytes(500, 0); got != 0 {
		t.Errorf("pctOfBytes(500, 0) = %v, want 0", got)
	}
	if got := pctOfBytes(500, -1); got != 0 {
		t.Errorf("pctOfBytes(500, -1) = %v, want 0", got)
	}
	if got := pctOfBytes(50, 100); got != 50 {
		t.Errorf("pctOfBytes(50, 100) = %v, want 50", got)
	}
	// A total smaller than the bytes written (a lying Content-Length) must
	// still clamp to 100, never overshoot.
	if got := pctOfBytes(150, 100); got != 100 {
		t.Errorf("pctOfBytes(150, 100) = %v, want 100 (clamped)", got)
	}
}

// TestEnsureManifestProgressFilePhasesInOrder is the config-level test spec
// 55-models.md §6 asks for: AssetProgress phases in order for a file asset
// against a fake HTTP server, with real Bytes/Total when Content-Length is
// present.
func TestEnsureManifestProgressFilePhasesInOrder(t *testing.T) {
	const body = "0123456789abcdef"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body)) // single Write: net/http sets Content-Length.
	}))
	defer srv.Close()

	dir := t.TempDir()
	var ticks []AssetProgress
	err := ensureManifestProgress(t.Context(), fileAssetManifest(srv.URL), dir, func(p AssetProgress) {
		ticks = append(ticks, p)
	})
	if err != nil {
		t.Fatalf("ensureManifestProgress() error = %v", err)
	}
	if len(ticks) < 3 {
		t.Fatalf("got %d ticks, want at least start/download/verify/done", len(ticks))
	}
	if got, want := ticks[0].Phase, assetPhaseStart; got != want {
		t.Errorf("first phase = %q, want %q", got, want)
	}
	last := ticks[len(ticks)-1]
	if last.Phase != assetPhaseDone {
		t.Errorf("last phase = %q, want %q", last.Phase, assetPhaseDone)
	}
	if last.Pct != 100 {
		t.Errorf("last pct = %v, want 100", last.Pct)
	}
	if ticks[len(ticks)-2].Phase != assetPhaseVerify {
		t.Errorf("second-to-last phase = %q, want %q", ticks[len(ticks)-2].Phase, assetPhaseVerify)
	}
	var sawDownload bool
	for _, p := range ticks {
		if p.Asset != "whispercpp/ggml-base.en.bin" {
			t.Errorf("tick asset = %q, want %q", p.Asset, "whispercpp/ggml-base.en.bin")
		}
		if p.Pct < 0 || p.Pct > 100 {
			t.Errorf("tick pct = %v, out of [0,100]", p.Pct)
		}
		if p.Phase == assetPhaseDownload {
			sawDownload = true
			if p.Total != int64(len(body)) {
				t.Errorf("download tick total = %d, want %d (Content-Length present)", p.Total, len(body))
			}
			if p.Bytes <= 0 {
				t.Errorf("download tick bytes = %d, want > 0", p.Bytes)
			}
		}
	}
	if !sawDownload {
		t.Fatal("expected at least one download tick")
	}
}

// TestEnsureManifestProgressFileNoContentLengthReportsZeroTotal covers the
// "0 when absent" half of the Bytes/Total contract: a chunked response with
// no Content-Length must report Total == 0 while Bytes stays real.
func TestEnsureManifestProgressFileNoContentLengthReportsZeroTotal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		_, _ = w.Write([]byte("part-one-"))
		flusher.Flush() // forces chunked transfer: no Content-Length is sent.
		_, _ = w.Write([]byte("part-two"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	var sawDownload bool
	err := ensureManifestProgress(t.Context(), fileAssetManifest(srv.URL), dir, func(p AssetProgress) {
		if p.Phase != assetPhaseDownload {
			return
		}
		sawDownload = true
		if p.Total != 0 {
			t.Errorf("download tick total = %d, want 0 (no Content-Length)", p.Total)
		}
		if p.Bytes <= 0 {
			t.Errorf("download tick bytes = %d, want > 0", p.Bytes)
		}
	})
	if err != nil {
		t.Fatalf("ensureManifestProgress() error = %v", err)
	}
	if !sawDownload {
		t.Fatal("expected at least one download tick")
	}
}

// TestEnsureManifestProgressSkipsAlreadyPresentFile covers the "skipped"
// already-installed-shortcut emitter: pct is forced to 100 even though no
// bytes moved.
func TestEnsureManifestProgressSkipsAlreadyPresentFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "must not be called", http.StatusInternalServerError)
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

	var ticks []AssetProgress
	err := ensureManifestProgress(t.Context(), fileAssetManifest(srv.URL), dir, func(p AssetProgress) {
		ticks = append(ticks, p)
	})
	if err != nil {
		t.Fatalf("ensureManifestProgress() error = %v", err)
	}
	if len(ticks) != 1 || ticks[0].Phase != assetPhaseSkipped || ticks[0].Pct != 100 {
		t.Fatalf("ticks = %+v, want a single skipped/pct-100 tick", ticks)
	}
}

// makeBzip2Tar shells out to the system bzip2 binary to compress a tar built
// from entries — the stdlib's compress/bzip2 package only offers a reader,
// not a writer, so this is the only way to produce a real archive fixture.
func makeBzip2Tar(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	path, err := exec.LookPath("bzip2")
	if err != nil {
		t.Skip("bzip2 binary not available on PATH")
	}
	cmd := exec.Command(path, "-z", "-c")
	cmd.Stdin = bytes.NewReader(makeTar(t, entries))
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("bzip2 compress: %v", err)
	}
	return out.Bytes()
}

// TestEnsureManifestProgressArchivePhasesInOrder is the archive-asset half of
// the §6 phase-order test: start, download(s), verify, extract, install, done.
func TestEnsureManifestProgressArchivePhasesInOrder(t *testing.T) {
	archiveBytes := makeBzip2Tar(t, []tarEntry{
		{name: "pack/", typeflag: tar.TypeDir},
		{name: "pack/model.onnx", typeflag: tar.TypeReg, body: "onnx-bytes"},
	})
	sum := sha256.Sum256(archiveBytes)
	sha := hex.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archiveBytes)
	}))
	defer srv.Close()

	dir := t.TempDir()
	manifest := AssetManifest{
		Schema: AssetSchema,
		Assets: []Asset{{
			ID: "tts.kokoro", Provider: "kokoro", Kind: AssetKindTTS, Name: "kokoro-tts",
			Archive:    &AssetArchive{URL: srv.URL, SHA256: sha},
			CheckFiles: []string{"model.onnx"},
		}},
	}

	var ticks []AssetProgress
	err := ensureManifestProgress(t.Context(), manifest, dir, func(p AssetProgress) {
		ticks = append(ticks, p)
	})
	if err != nil {
		t.Fatalf("ensureManifestProgress() error = %v", err)
	}

	wantOrder := []string{assetPhaseStart, assetPhaseVerify, assetPhaseExtract, assetPhaseInstall, assetPhaseDone}
	var gotPhases []string
	var sawDownload bool
	for _, p := range ticks {
		if p.Asset != "kokoro-tts" {
			t.Errorf("tick asset = %q, want %q", p.Asset, "kokoro-tts")
		}
		if p.Pct < 0 || p.Pct > 100 {
			t.Errorf("tick pct = %v, out of [0,100]", p.Pct)
		}
		if p.Phase == assetPhaseDownload {
			sawDownload = true
			continue
		}
		gotPhases = append(gotPhases, p.Phase)
	}
	if !sawDownload {
		t.Error("expected at least one download tick")
	}
	if strings.Join(gotPhases, ",") != strings.Join(wantOrder, ",") {
		t.Errorf("non-download phases = %v, want %v", gotPhases, wantOrder)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "model.onnx")); err != nil || string(got) != "onnx-bytes" {
		t.Errorf("extracted model.onnx = %q err=%v, want %q", got, err, "onnx-bytes")
	}
}

// TestEnsureManifestLegacyAdapterReceivesNameAndClampedPct guards the
// EnsureRuntimeAssets(name, pct) adapter: it must still see the asset name
// (not a phase label) and a terminal pct of 100, same as before this file's
// AssetProgress plumbing landed.
func TestEnsureManifestLegacyAdapterReceivesNameAndClampedPct(t *testing.T) {
	const body = "abcdefgh"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	dir := t.TempDir()
	var names []string
	var pcts []float64
	err := ensureManifest(t.Context(), fileAssetManifest(srv.URL), dir, func(name string, pct float64) {
		names = append(names, name)
		pcts = append(pcts, pct)
	})
	if err != nil {
		t.Fatalf("ensureManifest() error = %v", err)
	}
	if len(pcts) == 0 {
		t.Fatal("expected at least one progress tick")
	}
	for _, name := range names {
		if name != "whispercpp/ggml-base.en.bin" {
			t.Errorf("progress name = %q, want the asset name %q", name, "whispercpp/ggml-base.en.bin")
		}
	}
	for _, pct := range pcts {
		if pct < 0 || pct > 100 {
			t.Errorf("pct = %v, out of [0,100]", pct)
		}
	}
	if final := pcts[len(pcts)-1]; final != 100 {
		t.Errorf("final pct = %v, want 100", final)
	}
}

// TestEnsureQwenTTSAssetsProgressTicksAndShortcut exercises the qwen-native
// emitter (the third of the three emitters §6 asks to see clamped): a real
// install through the real EnsureNative, then a second call against the same
// modelsDir hitting the already-installed shortcut.
func TestEnsureQwenTTSAssetsProgressTicksAndShortcut(t *testing.T) {
	modelsDir := t.TempDir()
	cfg := &Config{
		TTSProvider:      qwen.ProviderName,
		ModelsDir:        modelsDir,
		QwenTTSModelTier: "0.6b",
		QwenTTSNativeURL: writeFakeQwenNativeTar(t, t.TempDir(), "0.6b"),
	}

	var first []AssetProgress
	if err := ensureQwenTTSAssetsProgress(t.Context(), cfg, func(p AssetProgress) {
		first = append(first, p)
	}); err != nil {
		t.Fatalf("ensureQwenTTSAssetsProgress() error = %v", err)
	}
	if len(first) < 2 {
		t.Fatalf("expected multiple progress ticks, got %d: %+v", len(first), first)
	}
	if first[0].Phase != assetPhaseStart {
		t.Errorf("first tick phase = %q, want %q", first[0].Phase, assetPhaseStart)
	}
	for _, p := range first {
		if p.Asset != qwenNativeAssetName {
			t.Errorf("tick asset = %q, want %q", p.Asset, qwenNativeAssetName)
		}
		if p.Pct < 0 || p.Pct > 100 {
			t.Errorf("tick pct = %v, out of [0,100]", p.Pct)
		}
	}
	if last := first[len(first)-1]; last.Pct != 100 {
		t.Errorf("final tick pct = %v, want 100", last.Pct)
	}

	var second []AssetProgress
	if err := ensureQwenTTSAssetsProgress(t.Context(), cfg, func(p AssetProgress) {
		second = append(second, p)
	}); err != nil {
		t.Fatalf("ensureQwenTTSAssetsProgress() second call error = %v", err)
	}
	if len(second) != 1 || second[0].Phase != assetPhaseSkipped || second[0].Pct != 100 {
		t.Fatalf("already-installed shortcut ticks = %+v, want a single skipped/pct-100 tick", second)
	}
}
