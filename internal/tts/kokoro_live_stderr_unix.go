//go:build integration && !windows

package tts

import (
	"fmt"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// withCapturedStderr runs fn while fd 2 (C/cgo stderr) is redirected to a temp
// file. Returns the captured bytes. Needed because sherpa-onnx logs U+0329
// skips via fprintf(stderr), not Go's log package.
func withCapturedStderr(fn func()) (string, error) {
	f, err := os.CreateTemp("", "kokoro-stderr-*.log")
	if err != nil {
		return "", err
	}
	path := f.Name()
	defer func() { _ = os.Remove(path) }()

	saved, err := unix.Dup(2)
	if err != nil {
		_ = f.Close()
		return "", fmt.Errorf("dup stderr: %w", err)
	}
	if err := unix.Dup2(int(f.Fd()), 2); err != nil {
		_ = unix.Close(saved)
		_ = f.Close()
		return "", fmt.Errorf("dup2 stderr: %w", err)
	}

	var once sync.Once
	restore := func() {
		once.Do(func() {
			_ = unix.Dup2(saved, 2)
			_ = unix.Close(saved)
			_ = f.Close()
		})
	}
	defer restore()

	fn()
	restore()

	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
