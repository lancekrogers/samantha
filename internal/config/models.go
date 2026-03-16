package config

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// ModelFile describes a required model file.
type ModelFile struct {
	Name string
	URL  string
	Size int64 // expected size in bytes (0 = skip check)
}

// RequiredModels returns the list of model files needed to run.
var RequiredModels = []ModelFile{
	{
		Name: "kokoro-v1.0.onnx",
		URL:  "https://github.com/thewh1teagle/kokoro-onnx/releases/download/model-files-v1.0/kokoro-v1.0.onnx",
		Size: 325322752,
	},
	{
		Name: "voices-v1.0.bin",
		URL:  "https://github.com/thewh1teagle/kokoro-onnx/releases/download/model-files-v1.0/voices-v1.0.bin",
		Size: 28221440,
	},
	{
		Name: "silero_vad.onnx",
		URL:  "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/silero_vad.onnx",
		Size: 0, // skip size check
	},
}

// EnsureModels downloads any missing model files.
func EnsureModels(onProgress func(name string, pct float64)) error {
	dir := ModelsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create models dir: %w", err)
	}

	for _, m := range RequiredModels {
		path := filepath.Join(dir, m.Name)
		if fileExists(path, m.Size) {
			continue
		}

		if onProgress != nil {
			onProgress(m.Name, 0)
		}

		if err := downloadFile(path, m.URL, func(pct float64) {
			if onProgress != nil {
				onProgress(m.Name, pct)
			}
		}); err != nil {
			return fmt.Errorf("download %s: %w", m.Name, err)
		}
	}
	return nil
}

func fileExists(path string, expectedSize int64) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if expectedSize > 0 && info.Size() != expectedSize {
		return false
	}
	return true
}

func downloadFile(path, url string, onProgress func(float64)) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}

	f, err := os.Create(path + ".tmp")
	if err != nil {
		return err
	}
	defer f.Close()

	total := resp.ContentLength
	var written int64

	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				return err
			}
			written += int64(n)
			if total > 0 && onProgress != nil {
				onProgress(float64(written) / float64(total) * 100)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}

	f.Close()
	return os.Rename(path+".tmp", path)
}
