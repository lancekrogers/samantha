package encoder

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func okLookPath(string) (string, error)  { return "/usr/bin/ffmpeg", nil }
func errLookPath(string) (string, error) { return "", errors.New("executable file not found in $PATH") }

func TestForResolvesAndRejects(t *testing.T) {
	if enc, ok := For("", "", okLookPath); !ok || enc != nil {
		t.Errorf("For(\"\") = (%v, %v), want (nil, true) for WAV-only", enc, ok)
	}
	if enc, ok := For("wav", "", okLookPath); !ok || enc != nil {
		t.Errorf("For(\"wav\") = (%v, %v), want (nil, true)", enc, ok)
	}
	if enc, ok := For("mp3", "", okLookPath); !ok || enc == nil {
		t.Errorf("For(\"mp3\") = (%v, %v), want a real encoder", enc, ok)
	}
	if enc, ok := For("flac", "", okLookPath); ok || enc != nil {
		t.Errorf("For(\"flac\") = (%v, %v), want (nil, false) for unknown format", enc, ok)
	}
}

func TestSupported(t *testing.T) {
	for _, f := range []string{"", "wav", "WAV", "mp3", "M4B", "opus"} {
		if !Supported(f) {
			t.Errorf("Supported(%q) = false, want true", f)
		}
	}
	for _, f := range []string{"flac", "ogg", "wma"} {
		if Supported(f) {
			t.Errorf("Supported(%q) = true, want false", f)
		}
	}
}

func TestFFmpegPreflightMissingBinaryFailsClearly(t *testing.T) {
	f, ok := newFFmpeg("mp3", "ffmpeg", errLookPath)
	if !ok {
		t.Fatal("newFFmpeg(mp3) not ok")
	}
	err := f.Preflight(context.Background())
	if err == nil {
		t.Fatal("Preflight with missing binary should error")
	}
	// Error must name the binary and the requested format.
	if !strings.Contains(err.Error(), "ffmpeg") || !strings.Contains(err.Error(), "mp3") {
		t.Errorf("error %q should name the binary and format", err)
	}
}

func TestFFmpegPreflightOK(t *testing.T) {
	f, _ := newFFmpeg("mp3", "", okLookPath)
	if err := f.Preflight(context.Background()); err != nil {
		t.Errorf("Preflight ok case = %v", err)
	}
	if f.Name() != "ffmpeg" || f.Ext() != "mp3" {
		t.Errorf("Name/Ext = %q/%q", f.Name(), f.Ext())
	}
}

func TestFFmpegEncodeBuildsExpectedArgs(t *testing.T) {
	f, _ := newFFmpeg("mp3", "ffmpeg", okLookPath)
	var gotName string
	var gotArgs []string
	f.run = func(_ context.Context, name string, args ...string) error {
		gotName, gotArgs = name, args
		return nil
	}
	if err := f.Encode(context.Background(), "ch.wav", "ch.mp3"); err != nil {
		t.Fatalf("Encode = %v", err)
	}
	want := []string{"-y", "-i", "ch.wav", "-c:a", "libmp3lame", "-q:a", "2", "ch.mp3"}
	if gotName != "ffmpeg" || !reflect.DeepEqual(gotArgs, want) {
		t.Errorf("ran %q %v, want ffmpeg %v", gotName, gotArgs, want)
	}
}

func TestFFmpegEncodeFailureNamesSourceAndDest(t *testing.T) {
	f, _ := newFFmpeg("m4b", "ffmpeg", okLookPath)
	f.run = func(_ context.Context, _ string, _ ...string) error {
		return errors.New("codec not available")
	}
	err := f.Encode(context.Background(), "ch.wav", "ch.m4b")
	if err == nil {
		t.Fatal("Encode should surface a runner failure")
	}
	if !strings.Contains(err.Error(), "ch.wav") || !strings.Contains(err.Error(), "ch.m4b") {
		t.Errorf("error %q should name source and destination", err)
	}
}
