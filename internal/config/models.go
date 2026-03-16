package config

import (
	"archive/tar"
	"compress/bzip2"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// ModelFile describes a required model file (individual download).
type ModelFile struct {
	Name string
	URL  string
	Size int64 // expected size in bytes (0 = skip check)
}

// ModelArchive describes a tar.bz2 archive to download and extract.
type ModelArchive struct {
	Name       string   // display name for progress
	URL        string   // tar.bz2 URL
	CheckFiles []string // paths relative to ModelsDir to verify extraction
}

// AssetRequest describes which runtime assets are needed for a command.
type AssetRequest struct {
	NeedSTT bool
	NeedTTS bool
	NeedVAD bool
}

// runtimeFiles returns the required individual downloads for the given run.
func runtimeFiles(req AssetRequest) []ModelFile {
	if !req.NeedVAD {
		return nil
	}
	return []ModelFile{
		{
			Name: "silero_vad.onnx",
			URL:  "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/silero_vad.onnx",
			Size: 0,
		},
	}
}

// runtimeArchives returns tar.bz2 archives parameterized by config and request.
func runtimeArchives(cfg *Config, req AssetRequest) []ModelArchive {
	var archives []ModelArchive

	if req.NeedSTT && strings.EqualFold(cfg.STTProvider, "sherpa") {
		model := cfg.WhisperModel
		archives = append(archives, ModelArchive{
			Name: fmt.Sprintf("whisper-%s", model),
			URL:  fmt.Sprintf("https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-whisper-%s.tar.bz2", model),
			CheckFiles: []string{
				fmt.Sprintf("%s-encoder.onnx", model),
				fmt.Sprintf("%s-decoder.onnx", model),
			},
		})
	}

	if req.NeedTTS && strings.EqualFold(cfg.TTSProvider, "kokoro") {
		archives = append(archives, ModelArchive{
			Name: "kokoro-tts",
			URL:  "https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/kokoro-multi-lang-v1_0.tar.bz2",
			CheckFiles: []string{
				"model.onnx",
				"voices.bin",
				"tokens.txt",
				"espeak-ng-data",
			},
		})
	}

	return archives
}

// EnsureRuntimeAssets downloads any missing model files and archives needed for this run.
func EnsureRuntimeAssets(cfg *Config, req AssetRequest, onProgress func(name string, pct float64)) error {
	dir := ModelsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create models dir: %w", err)
	}

	// Individual file downloads.
	for _, m := range runtimeFiles(req) {
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

	// Archive downloads with extraction.
	for _, a := range runtimeArchives(cfg, req) {
		if archiveExtracted(dir, a.CheckFiles) {
			continue
		}

		if onProgress != nil {
			onProgress(a.Name, 0)
		}

		if err := downloadAndExtractArchive(dir, a.URL, func(pct float64) {
			if onProgress != nil {
				onProgress(a.Name, pct)
			}
		}); err != nil {
			return fmt.Errorf("download %s: %w", a.Name, err)
		}
	}

	return nil
}

// EnsureModels preserves the old behavior for callers that still need the default asset set.
func EnsureModels(cfg *Config, onProgress func(name string, pct float64)) error {
	return EnsureRuntimeAssets(cfg, AssetRequest{
		NeedSTT: strings.EqualFold(cfg.STTProvider, "sherpa"),
		NeedTTS: strings.EqualFold(cfg.TTSProvider, "kokoro"),
		NeedVAD: cfg.VADEnabled,
	}, onProgress)
}

// archiveExtracted checks if all expected files/dirs exist.
func archiveExtracted(dir string, checkFiles []string) bool {
	for _, f := range checkFiles {
		path := filepath.Join(dir, f)
		if _, err := os.Stat(path); err != nil {
			return false
		}
	}
	return true
}

// downloadAndExtractArchive downloads a tar.bz2 and extracts to dir,
// stripping the top-level directory prefix.
func downloadAndExtractArchive(dir, url string, onProgress func(float64)) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}

	// Wrap body in a progress-tracking reader.
	total := resp.ContentLength
	var read int64
	progressReader := &progressReaderWrapper{
		r: resp.Body,
		onRead: func(n int) {
			read += int64(n)
			if total > 0 && onProgress != nil {
				onProgress(float64(read) / float64(total) * 100)
			}
		},
	}

	bzReader := bzip2.NewReader(progressReader)
	tr := tar.NewReader(bzReader)

	// Detect and strip top-level directory prefix.
	var prefix string
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading archive: %w", err)
		}

		name := header.Name

		// Detect prefix from first entry.
		if prefix == "" {
			parts := strings.SplitN(name, "/", 2)
			if len(parts) > 1 {
				prefix = parts[0] + "/"
			}
		}

		// Strip prefix.
		rel := strings.TrimPrefix(name, prefix)
		if rel == "" || rel == "." {
			continue
		}

		target := filepath.Join(dir, rel)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("create dir %s: %w", rel, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("create parent dir for %s: %w", rel, err)
			}
			f, err := os.Create(target)
			if err != nil {
				return fmt.Errorf("create file %s: %w", rel, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return fmt.Errorf("write file %s: %w", rel, err)
			}
			f.Close()
			if err := os.Chmod(target, os.FileMode(header.Mode)); err != nil {
				return fmt.Errorf("chmod %s: %w", rel, err)
			}
		}
	}

	return nil
}

type progressReaderWrapper struct {
	r      io.Reader
	onRead func(int)
}

func (p *progressReaderWrapper) Read(buf []byte) (int, error) {
	n, err := p.r.Read(buf)
	if n > 0 {
		p.onRead(n)
	}
	return n, err
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
