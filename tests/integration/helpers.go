//go:build integration
// +build integration

package integration

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// demuxDockerOutput strips Docker exec multiplexed stream headers from output.
func demuxDockerOutput(data []byte) []byte {
	var result bytes.Buffer
	offset := 0
	for offset < len(data) {
		if offset+8 > len(data) {
			result.Write(data[offset:])
			break
		}
		payloadSize := binary.BigEndian.Uint32(data[offset+4 : offset+8])
		payloadStart := offset + 8
		payloadEnd := payloadStart + int(payloadSize)
		if payloadEnd > len(data) {
			payloadEnd = len(data)
		}
		result.Write(data[payloadStart:payloadEnd])
		offset = payloadEnd
	}
	return result.Bytes()
}

// TestContainer wraps container operations for testing.
type TestContainer struct {
	container testcontainers.Container
	ctx       context.Context
	t         *testing.T
}

// NewSharedContainer creates a container for reuse across multiple tests.
func NewSharedContainer() (*TestContainer, error) {
	ctx := context.Background()

	binaryPath, err := findBinary()
	if err != nil {
		return nil, fmt.Errorf("failed to find samantha binary: %w", err)
	}

	req := testcontainers.ContainerRequest{
		Image:      "alpine:latest",
		Cmd:        []string{"sleep", "3600"},
		WaitingFor: wait.ForExec([]string{"true"}).WithStartupTimeout(30 * time.Second),
		AutoRemove: true,
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to start container: %w", err)
	}

	// Copy binary into the container (not bind mount — avoids Colima stale page issues).
	if err := container.CopyFileToContainer(ctx, binaryPath, "/samantha", 0o755); err != nil {
		container.Terminate(ctx)
		return nil, fmt.Errorf("failed to copy samantha binary into container: %w", err)
	}

	// Verify binary is accessible.
	exitCode, _, err := container.Exec(ctx, []string{"/samantha", "--help"})
	if err != nil {
		container.Terminate(ctx)
		return nil, fmt.Errorf("failed to verify samantha binary: %w", err)
	}
	if exitCode != 0 {
		container.Terminate(ctx)
		return nil, fmt.Errorf("samantha --help exited with code %d", exitCode)
	}

	// Create working directories.
	exitCode, _, err = container.Exec(ctx, []string{
		"mkdir", "-p", "/test", "/root/.obey/agents/voice/samantha",
	})
	if err != nil || exitCode != 0 {
		container.Terminate(ctx)
		return nil, fmt.Errorf("failed to create initial directories: %w", err)
	}

	return &TestContainer{
		container: container,
		ctx:       ctx,
		t:         nil,
	}, nil
}

// findBinary locates the pre-built Linux binary.
func findBinary() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get working directory: %w", err)
	}

	// From tests/integration/, project root is ../..
	projectRoot := filepath.Join(cwd, "../..")
	projectRoot, err = filepath.Abs(projectRoot)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}

	binaryPath := filepath.Join(projectRoot, "bin", "linux", "samantha")
	if _, err := os.Stat(binaryPath); err != nil {
		// Fallback: try building it.
		return "", fmt.Errorf(
			"Linux binary not found at %s — run 'just build linux' or buildutil integration first: %w",
			binaryPath, err,
		)
	}

	return binaryPath, nil
}

// Reset clears container state between tests.
func (tc *TestContainer) Reset() error {
	exitCode, _, err := tc.container.Exec(tc.ctx, []string{
		"sh", "-c",
		"rm -rf /test /root/.obey 2>/dev/null; " +
			"mkdir -p /test /root/.obey/agents/voice/samantha; sync",
	})
	if err != nil {
		return fmt.Errorf("failed to reset container: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("reset command failed with exit code %d", exitCode)
	}
	return nil
}

// Cleanup terminates the container.
func (tc *TestContainer) Cleanup() {
	if tc.container != nil {
		tc.container.Terminate(tc.ctx)
	}
}

// RunSamantha runs the samantha command in the container.
func (tc *TestContainer) RunSamantha(args ...string) (string, error) {
	cmd := append([]string{"/samantha"}, args...)

	exitCode, reader, err := tc.container.Exec(tc.ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("failed to execute samantha: %w", err)
	}

	rawOutput, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("failed to read output: %w", err)
	}

	output := demuxDockerOutput(rawOutput)

	if exitCode != 0 {
		return string(output), fmt.Errorf("samantha exited with code %d: %s", exitCode, output)
	}

	return string(output), nil
}

// ReadFile reads a file from the container.
func (tc *TestContainer) ReadFile(path string) (string, error) {
	exitCode, reader, err := tc.container.Exec(tc.ctx, []string{"cat", path})
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	rawOutput, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("failed to read output: %w", err)
	}

	output := demuxDockerOutput(rawOutput)

	if exitCode != 0 {
		return "", fmt.Errorf("cat command failed with exit code %d: %s", exitCode, output)
	}

	return string(output), nil
}

// WriteFile writes content to a file in the container.
func (tc *TestContainer) WriteFile(path, content string) error {
	dir := filepath.Dir(path)
	exitCode, _, err := tc.container.Exec(tc.ctx, []string{"mkdir", "-p", dir})
	if err != nil || exitCode != 0 {
		return fmt.Errorf("failed to create parent directory: %w", err)
	}

	exitCode, _, err = tc.container.Exec(tc.ctx, []string{
		"sh", "-c",
		fmt.Sprintf("printf '%%s' '%s' > %s", strings.ReplaceAll(content, "'", "'\\''"), path),
	})
	if err != nil || exitCode != 0 {
		return fmt.Errorf("failed to write file: %w", err)
	}

	return nil
}

// CheckFileExists checks if a file exists in the container.
func (tc *TestContainer) CheckFileExists(path string) (bool, error) {
	exitCode, _, err := tc.container.Exec(tc.ctx, []string{"test", "-f", path})
	if err != nil {
		return false, fmt.Errorf("failed to check file: %w", err)
	}
	return exitCode == 0, nil
}

// ExecCommand executes an arbitrary command in the container.
func (tc *TestContainer) ExecCommand(args ...string) (string, int, error) {
	exitCode, reader, err := tc.container.Exec(tc.ctx, args)
	if err != nil {
		return "", -1, fmt.Errorf("failed to execute command: %w", err)
	}

	rawOutput, err := io.ReadAll(reader)
	if err != nil {
		return "", exitCode, fmt.Errorf("failed to read output: %w", err)
	}

	output := demuxDockerOutput(rawOutput)
	return string(output), exitCode, nil
}

// goarch returns the host GOARCH for cross-compilation.
func goarch() string {
	return runtime.GOARCH
}
