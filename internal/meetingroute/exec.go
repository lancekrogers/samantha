package meetingroute

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// DefaultRunner runs name with args, capturing combined stdout+stderr on failure.
func DefaultRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		msg := stringsTrim(stderr.String())
		if msg == "" {
			msg = stringsTrim(stdout.String())
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

func stringsTrim(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\n' || s[0] == '\t' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 {
		c := s[len(s)-1]
		if c != ' ' && c != '\n' && c != '\t' && c != '\r' {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}

// NewDefaultRouter builds a production Router from routing config.
func NewDefaultRouter(cfg Config) *Router {
	return &Router{
		Cfg:      cfg.Normalize(),
		Run:      DefaultRunner,
		LookPath: DefaultLookPath,
	}
}
