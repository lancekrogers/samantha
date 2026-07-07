package config

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// cleanTestManifest exercises every ownership shape: an individual file at the
// models-dir root, an individual file in a nested dir, a TargetDir archive
// (whole directory owned), and a root-extracting archive (footprint owned via
// its recorded install marker).
func cleanTestManifest() AssetManifest {
	return AssetManifest{
		Schema: AssetSchema,
		Assets: []Asset{
			{
				ID:       "vad.test",
				Provider: "sherpa",
				Kind:     AssetKindVAD,
				Files:    []AssetFile{{Path: "vad.onnx", URL: "https://example.invalid/vad.onnx"}},
			},
			{
				ID:       "cli.test",
				Provider: "whispercpp",
				Kind:     AssetKindSTT,
				Files:    []AssetFile{{Path: filepath.Join("whispercpp", "model.bin"), URL: "https://example.invalid/model.bin"}},
			},
			{
				ID:         "stt.test",
				Provider:   "sherpa",
				Kind:       AssetKindSTT,
				TargetDir:  "streaming-model",
				Archive:    &AssetArchive{URL: "https://example.invalid/stt.tar.bz2", SHA256: "aa"},
				CheckFiles: []string{"encoder.onnx"},
			},
			{
				ID:         "tts.test",
				Provider:   "kokoro",
				Kind:       AssetKindTTS,
				Archive:    &AssetArchive{URL: ttsFootprintURL, SHA256: "bb"},
				CheckFiles: []string{"model.onnx", "espeak-ng-data"},
			},
		},
	}
}

const ttsFootprintURL = "https://example.invalid/tts.tar.bz2"

// installRequired lays down every path cleanTestManifest owns: the two
// individual files, the TargetDir archive's whole directory (a check file plus
// an extra extracted file), and the root archive's check files plus extra
// extracted files, recording the full root footprint in a valid marker.
func installRequired(t *testing.T, dir string) {
	t.Helper()
	touchFile(t, filepath.Join(dir, "vad.onnx"))
	touchFile(t, filepath.Join(dir, "whispercpp", "model.bin"))

	// TargetDir archive: a check file plus an extra file the archive extracted
	// (e.g. the other precision) that is not in CheckFiles.
	touchFile(t, filepath.Join(dir, "streaming-model", "encoder.onnx"))
	touchFile(t, filepath.Join(dir, "streaming-model", "encoder.int8.onnx"))
	touchFile(t, filepath.Join(dir, "streaming-model", ".samantha-asset-stt.test.json"))

	// Root archive: check files, then extra extracted files, then a marker that
	// records the full extraction footprint.
	touchFile(t, filepath.Join(dir, "model.onnx"))
	touchFile(t, filepath.Join(dir, "espeak-ng-data", "phontab"))
	touchFile(t, filepath.Join(dir, "lexicon.txt"))
	touchFile(t, filepath.Join(dir, "dict", "words"))
	footprint := []string{"model.onnx", "espeak-ng-data", "lexicon.txt", "dict"}
	if err := writeArchiveInstallMarkerFiles(dir, "tts.test", ttsFootprintURL, "bb", []string{"model.onnx", "espeak-ng-data"}, footprint); err != nil {
		t.Fatalf("write tts.test marker: %v", err)
	}
}

func candidatePaths(t *testing.T, dir string, candidates []CleanCandidate) []string {
	t.Helper()
	paths := make([]string, 0, len(candidates))
	for _, c := range candidates {
		rel, err := filepath.Rel(dir, c.Path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("candidate %q is outside the models dir %q", c.Path, dir)
		}
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	return paths
}

func TestCleanCandidatesContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := cleanTestManifest().CleanCandidates(ctx, t.TempDir())
	if err == nil {
		t.Fatal("CleanCandidates() with cancelled context should error")
	}
}

func TestCleanCandidates(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, dir string)
		want  []string
	}{
		{
			name:  "missing models dir yields no candidates",
			setup: func(t *testing.T, dir string) { os.RemoveAll(dir) },
			want:  []string{},
		},
		{
			name:  "empty dir yields no candidates",
			setup: func(t *testing.T, dir string) {},
			want:  []string{},
		},
		{
			name:  "required assets are never candidates",
			setup: installRequired,
			want:  []string{},
		},
		{
			name: "extras are reported without the required assets",
			setup: func(t *testing.T, dir string) {
				installRequired(t, dir)
				touchFile(t, filepath.Join(dir, "stale.onnx"))
				touchFile(t, filepath.Join(dir, ".samantha-asset-old.json"))
				touchFile(t, filepath.Join(dir, "old-model", "encoder.onnx"))
				touchFile(t, filepath.Join(dir, "whispercpp", "stale.bin"))
			},
			want: []string{
				".samantha-asset-old.json",
				"old-model",
				"stale.onnx",
				filepath.Join("whispercpp", "stale.bin"),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.setup(t, dir)

			got, err := cleanTestManifest().CleanCandidates(context.Background(), dir)
			if err != nil {
				t.Fatalf("CleanCandidates() error = %v", err)
			}
			paths := candidatePaths(t, dir, got)
			if len(paths) != len(tc.want) {
				t.Fatalf("candidates = %v, want %v", paths, tc.want)
			}
			for i := range tc.want {
				if paths[i] != tc.want[i] {
					t.Errorf("candidates = %v, want %v", paths, tc.want)
					break
				}
			}
		})
	}
}

// TestCleanCandidatesNeverReportsArchiveOwnedFiles is the regression for the
// review finding: files an installed, currently-selected archive extracted
// beyond its CheckFiles must never be reported as removable — both for a
// TargetDir archive (whole directory owned) and a root-extracting archive
// (footprint owned via the recorded marker).
func TestCleanCandidatesNeverReportsArchiveOwnedFiles(t *testing.T) {
	dir := t.TempDir()
	installRequired(t, dir)
	// A genuine extra so the candidate list is non-empty and the assertion below
	// is not vacuously true.
	touchFile(t, filepath.Join(dir, "stale.onnx"))

	got, err := cleanTestManifest().CleanCandidates(context.Background(), dir)
	if err != nil {
		t.Fatalf("CleanCandidates() error = %v", err)
	}
	paths := candidatePaths(t, dir, got)

	reported := func(rel string) bool {
		for _, p := range paths {
			if p == rel {
				return true
			}
		}
		return false
	}
	if !reported("stale.onnx") {
		t.Fatalf("expected the genuine extra stale.onnx to be reported, got %v", paths)
	}
	// TargetDir archive: the non-check extracted file inside the owned directory.
	if reported(filepath.Join("streaming-model", "encoder.int8.onnx")) {
		t.Errorf("extra file inside the selected TargetDir archive must not be reported: %v", paths)
	}
	// Root-extracting archive: the footprint file beyond CheckFiles.
	if reported("lexicon.txt") {
		t.Errorf("recorded footprint file of the selected root archive must not be reported: %v", paths)
	}
}

// TestCleanCandidatesQuantizationSwitch is the reviewer's quantization case:
// with the streaming sherpa archive installed, switching WhisperQuantized must
// not surface the other precision's files, because the whole extraction target
// is owned by the selected asset.
func TestCleanCandidatesQuantizationSwitch(t *testing.T) {
	asset, err := SherpaStreamingModel("")
	if err != nil {
		t.Fatalf("SherpaStreamingModel() error = %v", err)
	}
	dir := t.TempDir()
	// Both precisions and the tokens are present in the extracted directory.
	for _, f := range []string{asset.Encoder, asset.EncoderInt8, asset.Decoder, asset.Joiner, asset.JoinerInt8, asset.Tokens} {
		touchFile(t, filepath.Join(dir, asset.DirName, f))
	}

	for _, quantized := range []bool{false, true} {
		cfg := &Config{STTProvider: "sherpa-streaming", TTSProvider: "none", VADEnabled: false, WhisperQuantized: quantized}
		manifest, err := ManifestFor(cfg, DefaultAssetRequest(cfg))
		if err != nil {
			t.Fatalf("ManifestFor(quantized=%v) error = %v", quantized, err)
		}
		got, err := manifest.CleanCandidates(context.Background(), dir)
		if err != nil {
			t.Fatalf("CleanCandidates(quantized=%v) error = %v", quantized, err)
		}
		if paths := candidatePaths(t, dir, got); len(paths) != 0 {
			t.Errorf("quantized=%v: streaming archive files reported as removable: %v", quantized, paths)
		}
	}
}

// TestCleanCandidatesLegacyMarkerSuppressesRoot documents the conservative
// fallback: a root-extracting archive installed with a marker that predates the
// recorded footprint can't be enumerated, so top-level entries are not reported
// at all (never a false positive). Suppression is scoped to the root level —
// unowned files in a required-holding subdirectory are still reported.
func TestCleanCandidatesLegacyMarkerSuppressesRoot(t *testing.T) {
	manifest := AssetManifest{
		Schema: AssetSchema,
		Assets: []Asset{
			{
				ID:       "cli.test",
				Provider: "whispercpp",
				Kind:     AssetKindSTT,
				Files:    []AssetFile{{Path: filepath.Join("whispercpp", "model.bin"), URL: "https://example.invalid/model.bin"}},
			},
			{
				ID:         "tts.test",
				Provider:   "kokoro",
				Kind:       AssetKindTTS,
				Archive:    &AssetArchive{URL: ttsFootprintURL, SHA256: "bb"},
				CheckFiles: []string{"model.onnx", "espeak-ng-data"},
			},
		},
	}
	dir := t.TempDir()
	touchFile(t, filepath.Join(dir, "whispercpp", "model.bin"))
	touchFile(t, filepath.Join(dir, "whispercpp", "stale.bin"))
	touchFile(t, filepath.Join(dir, "model.onnx"))
	touchFile(t, filepath.Join(dir, "espeak-ng-data", "phontab"))
	touchFile(t, filepath.Join(dir, "lexicon.txt")) // owned by the archive, footprint unknown
	touchFile(t, filepath.Join(dir, "stale-root.onnx"))
	// Legacy marker: valid but without the recorded footprint.
	if err := writeArchiveInstallMarker(dir, "tts.test", ttsFootprintURL, "bb", []string{"model.onnx", "espeak-ng-data"}); err != nil {
		t.Fatalf("write legacy marker: %v", err)
	}

	got, err := manifest.CleanCandidates(context.Background(), dir)
	if err != nil {
		t.Fatalf("CleanCandidates() error = %v", err)
	}
	paths := candidatePaths(t, dir, got)
	// Root-level entries suppressed; the nested unowned file still reported.
	want := []string{filepath.Join("whispercpp", "stale.bin")}
	if len(paths) != len(want) || paths[0] != want[0] {
		t.Fatalf("candidates = %v, want %v (root suppressed, nested extra reported)", paths, want)
	}
}

func TestCleanCandidatesDoesNotFollowSymlinks(t *testing.T) {
	outside := t.TempDir()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "big.bin"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}

	got, err := cleanTestManifest().CleanCandidates(context.Background(), dir)
	if err != nil {
		t.Fatalf("CleanCandidates() error = %v", err)
	}
	// candidatePaths fails the test if any candidate resolves outside dir.
	paths := candidatePaths(t, dir, got)
	if len(paths) != 1 || paths[0] != "escape" {
		t.Fatalf("candidates = %v, want only the symlink itself", paths)
	}
	if got[0].Size >= 4096 {
		t.Errorf("symlink candidate size = %d, must not count the linked-to target", got[0].Size)
	}
	if got[0].IsDir {
		t.Error("symlink candidate must not be reported as a directory")
	}
}

func TestCleanCandidateSizes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stale.onnx"), []byte("1234"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "old-model"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, size := range map[string]int{"a.onnx": 3, "b.onnx": 5} {
		if err := os.WriteFile(filepath.Join(dir, "old-model", name), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := cleanTestManifest().CleanCandidates(context.Background(), dir)
	if err != nil {
		t.Fatalf("CleanCandidates() error = %v", err)
	}
	sizes := map[string]int64{}
	dirs := map[string]bool{}
	for _, c := range got {
		rel, _ := filepath.Rel(dir, c.Path)
		sizes[rel] = c.Size
		dirs[rel] = c.IsDir
	}
	if sizes["stale.onnx"] != 4 {
		t.Errorf("stale.onnx size = %d, want 4", sizes["stale.onnx"])
	}
	if sizes["old-model"] != 8 || !dirs["old-model"] {
		t.Errorf("old-model size = %d dir = %v, want recursive size 8 and dir", sizes["old-model"], dirs["old-model"])
	}
}
