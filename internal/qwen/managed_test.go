package qwen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUseManaged(t *testing.T) {
	tests := []struct {
		name, binary, model string
		want                bool
	}{
		{"empty", "", "", true},
		{"legacy cli default", "qwen3-tts-cli", "", true},
		{"explicit model", "qwen3-tts-cli", "/m", false},
		{"explicit worker", "/opt/w", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := UseManaged(tt.binary, tt.model); got != tt.want {
				t.Fatalf("UseManaged(%q,%q)=%v want %v", tt.binary, tt.model, got, tt.want)
			}
		})
	}
}

func TestInspectNativeInstalled(t *testing.T) {
	dir := t.TempDir()
	// empty → not installed, not legacy
	st := Inspect(dir)
	if st.Installed || st.LegacyPython {
		t.Fatalf("%+v", st)
	}
}

func TestDetectAndQuarantineLegacyPython(t *testing.T) {
	dir := t.TempDir()
	p := ManagedPaths(dir)
	if err := os.MkdirAll(filepath.Dir(p.Worker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Worker, []byte("# legacy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	leg := DetectLegacyPython(dir)
	if !leg.Present {
		t.Fatal("expected legacy")
	}
	dst, err := QuarantineLegacyPython(dir)
	if err != nil {
		t.Fatal(err)
	}
	if dst == "" {
		t.Fatal("empty quarantine path")
	}
	if DetectLegacyPython(dir).Present {
		t.Fatal("legacy still present after quarantine")
	}
}

func TestCanonicalVoice(t *testing.T) {
	if v, ok := CanonicalVoice("vivian"); !ok || v != "Vivian" {
		t.Fatalf("%v %v", v, ok)
	}
}
