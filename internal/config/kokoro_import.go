package config

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

//go:embed kokoro_import.py
var kokoroImportPy []byte

const (
	// thewh1teagle Kokoro v1.0 — same files Python samantha-cli downloads.
	kokoroV1OnnxURL   = "https://github.com/thewh1teagle/kokoro-onnx/releases/download/model-files-v1.0/kokoro-v1.0.onnx"
	kokoroV1VoicesURL = "https://github.com/thewh1teagle/kokoro-onnx/releases/download/model-files-v1.0/voices-v1.0.bin"
	kokoroV1OnnxName  = "kokoro-v1.0.onnx"
	kokoroV1VoicesName = "voices-v1.0.bin"
)

// EnsureKokoroV1EnglishPack downloads thewh1teagle Kokoro v1.0 weights and
// converts them into models_dir/kokoro-v1.0-en for sherpa OfflineTts.
// Requires python3 with numpy+onnx on PATH. On failure returns a soft error
// so callers can keep using the multi-lang pack.
func EnsureKokoroV1EnglishPack(ctx context.Context, onProgress func(name string, pct float64)) error {
	root := ModelsDir()
	outDir := filepath.Join(root, KokoroV1Subdir)
	if kokoroPackReady(outDir) {
		return nil
	}

	// Frontend (tokens/espeak) comes from the multi-lang pack at models root.
	if !archiveExtracted(root, []string{"tokens.txt", "espeak-ng-data"}) {
		return fmt.Errorf("kokoro v1 import needs multi-lang frontend files (tokens.txt, espeak-ng-data) in %s", root)
	}

	python, err := lookupPython3()
	if err != nil {
		return err
	}

	stage, err := os.MkdirTemp(root, ".kokoro-v1-stage-*")
	if err != nil {
		return fmt.Errorf("kokoro v1 stage: %w", err)
	}
	defer os.RemoveAll(stage)

	onnxPath := filepath.Join(stage, kokoroV1OnnxName)
	voicesPath := filepath.Join(stage, kokoroV1VoicesName)
	if err := fetchOrReuse(ctx, kokoroV1OnnxURL, onnxPath, kokoroV1OnnxName, localKokoroV1Onnx(), onProgress); err != nil {
		return err
	}
	if err := fetchOrReuse(ctx, kokoroV1VoicesURL, voicesPath, kokoroV1VoicesName, localKokoroV1Voices(), onProgress); err != nil {
		return err
	}

	script := filepath.Join(stage, "kokoro_import.py")
	if err := os.WriteFile(script, kokoroImportPy, 0o644); err != nil {
		return err
	}

	outTmp, err := os.MkdirTemp(root, ".kokoro-v1-out-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(outTmp)

	cmd := exec.CommandContext(ctx, python, script,
		"--onnx", onnxPath,
		"--voices-npz", voicesPath,
		"--frontend-dir", root,
		"--out-dir", outTmp,
	)
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kokoro v1 convert (need python3 + numpy + onnx): %w", err)
	}

	// Atomic replace of destination.
	_ = os.RemoveAll(outDir)
	if err := os.Rename(outTmp, outDir); err != nil {
		// Cross-device rename can fail; fall back to copy.
		if err := copyDir(outTmp, outDir); err != nil {
			return fmt.Errorf("install kokoro v1 pack: %w", err)
		}
	}
	return nil
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

func lookupPython3() (string, error) {
	for _, name := range []string{"python3", "python"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("python3 not found on PATH (install python3 with numpy and onnx to enable Kokoro v1.0 English pack)")
}

// localKokoroV1Onnx returns a cached thewh1teagle ONNX if present (Python samantha-cli path).
func localKokoroV1Onnx() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "samantha", "models", kokoroV1OnnxName)
}

func localKokoroV1Voices() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "samantha", "models", kokoroV1VoicesName)
}

// fetchOrReuse copies localPath when present, otherwise downloads url to dest.
func fetchOrReuse(ctx context.Context, url, dest, name, localPath string, onProgress func(string, float64)) error {
	if localPath != "" {
		if st, err := os.Stat(localPath); err == nil && st.Size() > 0 {
			data, err := os.ReadFile(localPath)
			if err != nil {
				return err
			}
			if err := os.WriteFile(dest, data, 0o644); err != nil {
				return err
			}
			if onProgress != nil {
				onProgress(name, 1)
			}
			return nil
		}
	}
	var fileProg func(float64)
	if onProgress != nil {
		fileProg = func(p float64) { onProgress(name, p) }
	}
	return downloadFile(ctx, dest, url, name, 0, "", fileProg)
}

// TryEnsureKokoroV1EnglishPack attempts the import and prints a warning on
// soft failure so multi-lang remains available.
func TryEnsureKokoroV1EnglishPack(ctx context.Context, onProgress func(name string, pct float64)) {
	if kokoroPackReady(filepath.Join(ModelsDir(), KokoroV1Subdir)) {
		return
	}
	if err := EnsureKokoroV1EnglishPack(ctx, onProgress); err != nil {
		fmt.Fprintf(os.Stderr, "warning: Kokoro v1.0 English pack not installed: %v\n", err)
		fmt.Fprintf(os.Stderr, "  (using sherpa multi-lang pack; for Python-matching weights install python3+numpy+onnx and re-run models ensure)\n")
		return
	}
	fmt.Fprintf(os.Stderr, "Kokoro pack: thewh1teagle v1.0 English (matches Python samantha-cli weights)\n")
}
