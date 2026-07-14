//go:build !windows

package tui

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRedirectNativeDiagnosticsCapturesFileDescriptorTwo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "native.log")
	restore, err := redirectNativeDiagnostics(path)
	if err != nil {
		t.Fatalf("redirectNativeDiagnostics() error = %v", err)
	}
	if _, err := unix.Write(2, []byte("native warning\n")); err != nil {
		t.Fatalf("write fd 2: %v", err)
	}
	if err := restore(); err != nil {
		t.Fatalf("restore() error = %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read diagnostics: %v", err)
	}
	if string(got) != "native warning\n" {
		t.Fatalf("diagnostics = %q, want native warning", got)
	}
}
