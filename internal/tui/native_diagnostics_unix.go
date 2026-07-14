//go:build !windows

package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

func redirectNativeDiagnostics(path string) (func() error, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create diagnostics directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open diagnostics log: %w", err)
	}

	saved, err := unix.Dup(2)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("duplicate stderr: %w", err)
	}
	if err := unix.Dup2(int(file.Fd()), 2); err != nil {
		_ = unix.Close(saved)
		_ = file.Close()
		return nil, fmt.Errorf("redirect stderr: %w", err)
	}

	var once sync.Once
	var restoreErr error
	return func() error {
		once.Do(func() {
			if err := unix.Dup2(saved, 2); err != nil {
				restoreErr = fmt.Errorf("restore stderr: %w", err)
			}
			_ = unix.Close(saved)
			_ = file.Close()
		})
		return restoreErr
	}, nil
}
