package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

// WhisperCPPAsset describes a downloadable whisper.cpp ggml model file.
type WhisperCPPAsset struct {
	Name     string
	Filename string
	URL      string
}

// WhisperCPPModelAsset returns metadata for a supported whisper.cpp model.
func WhisperCPPModelAsset(name string) (WhisperCPPAsset, error) {
	switch strings.TrimSpace(strings.ToLower(name)) {
	case "", "base.en":
		return WhisperCPPAsset{
			Name:     "whispercpp-base.en",
			Filename: "ggml-base.en.bin",
			URL:      "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-base.en.bin",
		}, nil
	case "small.en":
		return WhisperCPPAsset{
			Name:     "whispercpp-small.en",
			Filename: "ggml-small.en.bin",
			URL:      "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-small.en.bin",
		}, nil
	default:
		return WhisperCPPAsset{}, fmt.Errorf("unsupported whisper.cpp model %q", name)
	}
}

// ModelPath joins the asset filename under the whisper.cpp cache dir.
func (a WhisperCPPAsset) ModelPath(base string) string {
	return filepath.Join(base, "whispercpp", a.Filename)
}
