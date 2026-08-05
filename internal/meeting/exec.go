package meeting

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// DefaultRunner runs name with args, capturing combined stdout+stderr on failure.
func DefaultRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return runCommand(ctx, "", name, args...)
}

// runCommand executes name with optional working directory (empty = process cwd).
// Shared by DefaultRunner and campaign import-meeting (must run inside the
// target campaign so camp loads the correct .campaign/ config).
func runCommand(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, name, args...)
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg != "" {
			return stdout.Bytes(), fmt.Errorf("%w: %s", err, msg)
		}
		return stdout.Bytes(), err
	}
	return stdout.Bytes(), nil
}

// DefaultLookPath is exec.LookPath.
func DefaultLookPath(file string) (string, error) {
	return exec.LookPath(file)
}

// NewDefaultRouter builds a production Router from routing config.
func NewDefaultRouter(cfg Config) *Router {
	return &Router{
		Cfg:      cfg.Normalize(),
		Run:      DefaultRunner,
		LookPath: DefaultLookPath,
	}
}
