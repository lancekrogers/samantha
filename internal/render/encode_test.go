package render

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakeEncoder is a render-level stand-in for an external encoder.
type fakeEncoder struct {
	ext     string
	failOn  string // base name whose Encode should fail (empty = never)
	encoded []string
}

func (f *fakeEncoder) Name() string                    { return "fake" }
func (f *fakeEncoder) Ext() string                     { return f.ext }
func (f *fakeEncoder) Preflight(context.Context) error { return nil }
func (f *fakeEncoder) Encode(_ context.Context, src, dst string) error {
	if f.failOn != "" && filepath.Base(src) == f.failOn {
		return errors.New("fake encode failure")
	}
	f.encoded = append(f.encoded, dst)
	return os.WriteFile(dst, []byte("encoded"), 0o644)
}

// TestResolveEncoderMissingBinaryFailsFast is the fail-before-synthesis guard:
// the command resolves the encoder before constructing the synthesizer, so a
// missing binary must surface here, before any TTS work.
func TestResolveEncoderMissingBinaryFailsFast(t *testing.T) {
	opts := Options{AudioFormat: "mp3"}
	missing := func(string) (string, error) { return "", errors.New("not found") }
	enc, err := ResolveEncoder(context.Background(), opts, missing)
	if err == nil {
		t.Fatal("ResolveEncoder with missing binary should error before synthesis")
	}
	if enc != nil {
		t.Errorf("encoder should be nil on failure, got %v", enc)
	}
}

func TestResolveEncoderWAVReturnsNil(t *testing.T) {
	enc, err := ResolveEncoder(context.Background(), Options{}, nil)
	if err != nil || enc != nil {
		t.Errorf("WAV-only ResolveEncoder = (%v, %v), want (nil, nil)", enc, err)
	}
}

func TestResolveEncoderRejectsUnknownFormat(t *testing.T) {
	_, err := ResolveEncoder(context.Background(), Options{AudioFormat: "flac"}, func(string) (string, error) { return "x", nil })
	if err == nil {
		t.Fatal("unknown audio format should error")
	}
}

func TestEncodeWAVsProducesSiblingsAndKeepsWAVs(t *testing.T) {
	dir := t.TempDir()
	wavs := []string{filepath.Join(dir, "001-one.wav"), filepath.Join(dir, "002-two.wav")}
	for _, w := range wavs {
		if err := os.WriteFile(w, []byte("RIFF"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	enc := &fakeEncoder{ext: "mp3"}
	encoded, err := EncodeWAVs(context.Background(), enc, wavs)
	if err != nil {
		t.Fatalf("EncodeWAVs = %v", err)
	}
	want := []string{filepath.Join(dir, "001-one.mp3"), filepath.Join(dir, "002-two.mp3")}
	if len(encoded) != 2 || encoded[0] != want[0] || encoded[1] != want[1] {
		t.Fatalf("encoded = %v, want %v", encoded, want)
	}
	for _, p := range append(append([]string{}, wavs...), want...) {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s on disk: %v", p, err)
		}
	}
}

// TestEncodeWAVsFailureLeavesWAVs covers the failure-mode contract: an encoder
// error leaves the source WAVs (and thus the manifest) intact and inspectable.
func TestEncodeWAVsFailureLeavesWAVs(t *testing.T) {
	dir := t.TempDir()
	wavs := []string{filepath.Join(dir, "001-one.wav"), filepath.Join(dir, "002-two.wav")}
	for _, w := range wavs {
		if err := os.WriteFile(w, []byte("RIFF"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	enc := &fakeEncoder{ext: "mp3", failOn: "002-two.wav"}
	_, err := EncodeWAVs(context.Background(), enc, wavs)
	if err == nil {
		t.Fatal("EncodeWAVs should report the encoder failure")
	}
	for _, w := range wavs {
		if _, statErr := os.Stat(w); statErr != nil {
			t.Errorf("WAV %s must survive an encode failure: %v", w, statErr)
		}
	}
}

func TestCompletedWAVPathsResolvesUnderBaseDir(t *testing.T) {
	m := RenderManifest{Segments: []ManifestSegment{
		{Output: "001-one.wav", Status: StatusComplete},
		{Output: "002-two.wav", Status: StatusSkipped},
		{Output: "003-three.wav", Status: StatusComplete},
	}}
	got := CompletedWAVPaths("/books/out", m)
	want := []string{"/books/out/001-one.wav", "/books/out/003-three.wav"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("CompletedWAVPaths = %v, want %v (only completed segments)", got, want)
	}
}
