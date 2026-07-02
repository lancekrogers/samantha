package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

// WhisperCPPAsset describes a downloadable whisper.cpp ggml model file.
// SHA256/Size come from the Hugging Face LFS metadata for the pinned revision.
type WhisperCPPAsset struct {
	Name     string
	Filename string
	URL      string
	SHA256   string
	Size     int64
}

// WhisperCPPModelAsset returns metadata for a supported whisper.cpp model.
func WhisperCPPModelAsset(name string) (WhisperCPPAsset, error) {
	switch strings.TrimSpace(strings.ToLower(name)) {
	case "", "base.en":
		return WhisperCPPAsset{
			Name:     "whispercpp-base.en",
			Filename: "ggml-base.en.bin",
			URL:      "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-base.en.bin",
			SHA256:   "a03779c86df3323075f5e796cb2ce5029f00ec8869eee3fdfb897afe36c6d002",
			Size:     147964211,
		}, nil
	case "small.en":
		return WhisperCPPAsset{
			Name:     "whispercpp-small.en",
			Filename: "ggml-small.en.bin",
			URL:      "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-small.en.bin",
			SHA256:   "c6138d6d58ecc8322097e0f987c32f1be8bb0a18532a3f88f734d1bbf9c41e5d",
			Size:     487614201,
		}, nil
	default:
		return WhisperCPPAsset{}, fmt.Errorf("unsupported whisper.cpp model %q", name)
	}
}

// ModelPath joins the asset filename under the whisper.cpp cache dir.
func (a WhisperCPPAsset) ModelPath(base string) string {
	return filepath.Join(base, "whispercpp", a.Filename)
}
