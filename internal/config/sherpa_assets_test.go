package config

import (
	"path/filepath"
	"testing"
)

func TestSherpaStreamingModelDefaults(t *testing.T) {
	asset, err := SherpaStreamingModel("en-2023-06-26")
	if err != nil {
		t.Fatalf("SherpaStreamingModel() error = %v", err)
	}

	if asset.ArchiveName != "sherpa-onnx-streaming-zipformer-en-2023-06-26" {
		t.Fatalf("ArchiveName = %q", asset.ArchiveName)
	}
	if got := asset.ModelDir("/tmp/models"); got != filepath.Join("/tmp/models", asset.DirName) {
		t.Fatalf("ModelDir() = %q", got)
	}
	if got := asset.EncoderFile(true); got != asset.EncoderInt8 {
		t.Fatalf("EncoderFile(true) = %q, want %q", got, asset.EncoderInt8)
	}
	files := asset.RequiredFiles(true)
	if len(files) != 4 {
		t.Fatalf("RequiredFiles() len = %d, want 4", len(files))
	}
}

func TestSherpaStreamingModelRejectsUnknownModel(t *testing.T) {
	if _, err := SherpaStreamingModel("unknown"); err == nil {
		t.Fatal("SherpaStreamingModel() error = nil, want unsupported model")
	}
}
