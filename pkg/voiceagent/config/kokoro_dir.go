package config

import (
	"os"
	"path/filepath"
)

// KokoroV1Subdir is where thewh1teagle Kokoro v1.0 weights live after
// conversion into a sherpa-compatible layout (English speaker subset).
const KokoroV1Subdir = "kokoro-v1.0-en"

// kokoroRequired are files NewKokoroTTS needs inside an asset directory.
var kokoroRequired = []string{
	"model.onnx",
	"voices.bin",
	"tokens.txt",
	"espeak-ng-data",
}

// KokoroDir returns the directory that holds Kokoro OfflineTts assets.
// Prefer the thewh1teagle v1.0 English pack when present (same weights as
// Python samantha-cli / kokoro-onnx); otherwise the legacy multi-lang pack
// installed at the models root.
func KokoroDir() string {
	root := ModelsDir()
	v1 := filepath.Join(root, KokoroV1Subdir)
	if kokoroPackReady(v1) {
		return v1
	}
	return root
}

// KokoroPack reports which pack KokoroDir selected.
func KokoroPack() string {
	if filepath.Base(KokoroDir()) == KokoroV1Subdir {
		return "thewh1teagle-v1.0-en"
	}
	return "sherpa-multi-lang-v1_0"
}

func kokoroPackReady(dir string) bool {
	for _, name := range kokoroRequired {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return false
		}
	}
	// Marker written by successful import of thewh1teagle weights.
	if _, err := os.Stat(filepath.Join(dir, ".kokoro-v1-en-source")); err != nil {
		return false
	}
	return true
}
