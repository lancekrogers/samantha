package render

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/lancekrogers/samantha/internal/render/encoder"
)

// ResolveEncoder selects the optional output encoder for opts and verifies it is
// available. It MUST be called before any synthesis so an unavailable encoder
// fails fast — before models load or any audio is produced. It returns
// (nil, nil) for WAV-only output (the default). lookPath is injectable for
// tests.
func ResolveEncoder(ctx context.Context, opts Options, lookPath func(string) (string, error)) (encoder.Encoder, error) {
	enc, ok := encoder.For(opts.AudioFormat, opts.EncoderBin, lookPath)
	if !ok {
		return nil, fmt.Errorf("render: unsupported --audio-format %q", opts.AudioFormat)
	}
	if enc == nil {
		return nil, nil
	}
	if err := enc.Preflight(ctx); err != nil {
		return nil, fmt.Errorf("render: %w", err)
	}
	return enc, nil
}

// EncodeWAVs transcodes each WAV to the encoder's format alongside it and
// returns the encoded paths. It is a post-processing step that runs only after
// the WAVs and the manifest are written, so an error here leaves WAV outputs and
// the manifest intact and inspectable. WAV remains the source of truth.
func EncodeWAVs(ctx context.Context, enc encoder.Encoder, wavPaths []string) ([]string, error) {
	encoded := make([]string, 0, len(wavPaths))
	for _, src := range wavPaths {
		if err := ctx.Err(); err != nil {
			return encoded, err
		}
		dst := encodedPath(src, enc.Ext())
		if err := enc.Encode(ctx, src, dst); err != nil {
			return encoded, err
		}
		encoded = append(encoded, dst)
	}
	return encoded, nil
}

// CompletedWAVPaths returns the on-disk WAV paths for the manifest's completed
// segments, resolved under baseDir (the segment Output is a bare filename for
// multi-file renders and a full path for single-file renders).
func CompletedWAVPaths(baseDir string, m RenderManifest) []string {
	var paths []string
	for _, s := range m.Segments {
		if s.Status != StatusComplete {
			continue
		}
		out := s.Output
		if !filepath.IsAbs(out) && baseDir != "" {
			out = filepath.Join(baseDir, out)
		}
		paths = append(paths, out)
	}
	return paths
}

// encodedPath swaps a source file's extension for the encoder's extension.
func encodedPath(src, ext string) string {
	return src[:len(src)-len(filepath.Ext(src))] + "." + ext
}
