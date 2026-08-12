package config

import "testing"

func TestWhisperCPPModelAssetDefaults(t *testing.T) {
	asset, err := WhisperCPPModelAsset("base.en")
	if err != nil {
		t.Fatalf("WhisperCPPModelAsset() error = %v", err)
	}
	if asset.Filename != "ggml-base.en.bin" {
		t.Fatalf("Filename = %q, want ggml-base.en.bin", asset.Filename)
	}
}

func TestWhisperCPPModelAssetRejectsUnknownModel(t *testing.T) {
	if _, err := WhisperCPPModelAsset("huge.custom"); err == nil {
		t.Fatal("WhisperCPPModelAsset() error = nil, want unsupported model")
	}
}
