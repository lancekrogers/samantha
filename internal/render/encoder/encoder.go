// Package encoder provides optional post-processing of rendered WAV audio into
// compressed formats (MP3/M4B/...) via an external, ffmpeg-compatible binary.
// Encoding is always optional: the renderer writes WAV first, and WAV remains
// the source of truth even when encoding is requested or fails.
package encoder

import (
	"context"
	"strings"
)

// Encoder transcodes a rendered WAV file into a compressed output.
type Encoder interface {
	// Name identifies the encoder (binary) for diagnostics and error messages.
	Name() string
	// Ext is the output file extension without a leading dot (e.g. "mp3").
	Ext() string
	// Preflight verifies the encoder can run. Callers MUST invoke it before any
	// synthesis so an unavailable encoder fails fast — before models load or any
	// audio is produced.
	Preflight(ctx context.Context) error
	// Encode transcodes src (a WAV file) to dst, overwriting dst if present.
	Encode(ctx context.Context, src, dst string) error
}

// For resolves an encoder for the requested audio format. An empty format (or
// "wav") means WAV-only output and yields (nil, true). An unknown format yields
// (nil, false). bin overrides the encoder binary (default "ffmpeg"); lookPath is
// injectable for tests (nil uses exec.LookPath).
func For(format, bin string, lookPath func(string) (string, error)) (Encoder, bool) {
	if format == "" || strings.EqualFold(format, "wav") {
		return nil, true
	}
	enc, ok := newFFmpeg(format, bin, lookPath)
	if !ok {
		return nil, false
	}
	return enc, true
}

// Supported reports whether format is a recognized output format (including the
// empty/"wav" WAV-only case). Used for fail-fast option validation.
func Supported(format string) bool {
	if format == "" || strings.EqualFold(format, "wav") {
		return true
	}
	_, ok := codecs[strings.ToLower(format)]
	return ok
}
