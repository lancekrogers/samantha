package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKokoroDirPrefersV1Pack(t *testing.T) {
	root := t.TempDir()
	// Point ModelsDir at temp via viper defaults is hard; set env + Load.
	t.Setenv("MODELS_DIR", root)
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}

	if got := KokoroDir(); got != root {
		t.Fatalf("without v1 pack KokoroDir = %q, want models root %q", got, root)
	}
	if got := KokoroPack(); got != "sherpa-multi-lang-v1_0" {
		t.Fatalf("KokoroPack = %q", got)
	}

	v1 := filepath.Join(root, KokoroV1Subdir)
	for _, name := range kokoroRequired {
		p := filepath.Join(v1, name)
		if name == "espeak-ng-data" {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(v1, ".kokoro-v1-en-source"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := KokoroDir(); got != v1 {
		t.Fatalf("with v1 pack KokoroDir = %q, want %q", got, v1)
	}
	if got := KokoroPack(); got != "thewh1teagle-v1.0-en" {
		t.Fatalf("KokoroPack = %q", got)
	}
}
