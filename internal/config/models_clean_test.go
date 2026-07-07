package config

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// cleanTestManifest claims vad.onnx at the root, an archive extracted into
// streaming-model/, and an archive extracted into the root — the three install
// shapes ManifestFor produces. Both archives carry a SHA256, so their install
// markers are claimed too.
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
				Archive:    &AssetArchive{URL: "https://example.invalid/tts.tar.bz2", SHA256: "bb"},
				CheckFiles: []string{"model.onnx", "espeak-ng-data"},
			},
		},
	}
}

// installRequired creates every path cleanTestManifest claims, including the
// archive install markers.
func installRequired(t *testing.T, dir string) {
	t.Helper()
	touchFile(t, filepath.Join(dir, "vad.onnx"))
	touchFile(t, filepath.Join(dir, "streaming-model", "encoder.onnx"))
	touchFile(t, filepath.Join(dir, "streaming-model", ".samantha-asset-stt.test.json"))
	touchFile(t, filepath.Join(dir, "model.onnx"))
	touchFile(t, filepath.Join(dir, "espeak-ng-data", "phontab"))
	touchFile(t, filepath.Join(dir, ".samantha-asset-tts.test.json"))
}

func candidatePaths(t *testing.T, dir string, candidates []CleanCandidate) []string {
	t.Helper()
	paths := make([]string, 0, len(candidates))
	for _, c := range candidates {
		rel, err := filepath.Rel(dir, c.Path)
		if err != nil || strings.HasPrefix(rel, "..") {
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
			},
			want: []string{".samantha-asset-old.json", "old-model", "stale.onnx"},
		},
		{
			name: "dir holding required paths is descended, not listed whole",
			setup: func(t *testing.T, dir string) {
				installRequired(t, dir)
				touchFile(t, filepath.Join(dir, "streaming-model", "stale.onnx"))
			},
			want: []string{filepath.Join("streaming-model", "stale.onnx")},
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
