package encoder

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// codec describes the ffmpeg arguments for one target format.
type codec struct {
	ext  string
	args []string // codec args inserted before the output path
}

// codecs maps a requested audio format to its ffmpeg codec arguments. These are
// the formats the encoder advertises; everything else is an unknown format.
var codecs = map[string]codec{
	"mp3":  {ext: "mp3", args: []string{"-c:a", "libmp3lame", "-q:a", "2"}},
	"m4a":  {ext: "m4a", args: []string{"-c:a", "aac", "-b:a", "128k"}},
	"m4b":  {ext: "m4b", args: []string{"-c:a", "aac", "-b:a", "64k"}},
	"aac":  {ext: "aac", args: []string{"-c:a", "aac", "-b:a", "128k"}},
	"opus": {ext: "opus", args: []string{"-c:a", "libopus", "-b:a", "48k"}},
}

// ffmpeg is an Encoder backed by an ffmpeg-compatible binary invoked via
// os/exec. The PATH lookup and command runner are injectable for tests.
type ffmpeg struct {
	bin      string
	format   string
	codec    codec
	lookPath func(string) (string, error)
	run      func(ctx context.Context, name string, args ...string) error
}

// newFFmpeg builds an ffmpeg encoder for format. ok is false if the format is
// not recognized. bin defaults to "ffmpeg"; lookPath defaults to exec.LookPath.
func newFFmpeg(format, bin string, lookPath func(string) (string, error)) (*ffmpeg, bool) {
	c, known := codecs[strings.ToLower(format)]
	if !known {
		return nil, false
	}
	if strings.TrimSpace(bin) == "" {
		bin = "ffmpeg"
	}
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	return &ffmpeg{
		bin:      bin,
		format:   strings.ToLower(format),
		codec:    c,
		lookPath: lookPath,
		run:      runCommand,
	}, true
}

func (f *ffmpeg) Name() string { return f.bin }
func (f *ffmpeg) Ext() string  { return f.codec.ext }

// Preflight verifies the encoder binary is resolvable on PATH. It does no I/O
// beyond the lookup, so it is cheap to call before synthesis.
func (f *ffmpeg) Preflight(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := f.lookPath(f.bin); err != nil {
		return fmt.Errorf("encoder %q not found on PATH (required for --audio-format %s): %w", f.bin, f.format, err)
	}
	return nil
}

// Encode transcodes src to dst, overwriting dst. The WAV at src is left intact.
func (f *ffmpeg) Encode(ctx context.Context, src, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	args := append([]string{"-y", "-i", src}, f.codec.args...)
	args = append(args, dst)
	if err := f.run(ctx, f.bin, args...); err != nil {
		return fmt.Errorf("encode %s -> %s with %s: %w", src, dst, f.bin, err)
	}
	return nil
}

// runCommand executes name with args, folding stderr/stdout into the error so a
// failed encode names the offending command and its diagnostics.
func runCommand(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail != "" {
			return fmt.Errorf("%w: %s", err, detail)
		}
		return err
	}
	return nil
}
