//go:build windows

package tui

import (
	"os"
	"path/filepath"
)

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
