//go:build windows

package tui

import (
	"os"
	"path/filepath"
)

// redirectNativeDiagnostics only reassigns Go's os.Stderr variable. Unlike
// the Unix build (which dup2s file descriptor 2 itself), this does not
// redirect the process's OS-level standard error handle, so cgo/native
// libraries that write directly to that handle are not covered — the same
// class of write this feature exists to catch. Windows is not currently
// built or tested by this project's CI; treat this as a partial stub for a
// future Windows-specific fix rather than a verified equivalent of the Unix
// path.
func redirectNativeDiagnostics(path string) (func() error, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	previous := os.Stderr
	os.Stderr = file
	return func() error {
		os.Stderr = previous
		return file.Close()
	}, nil
}
