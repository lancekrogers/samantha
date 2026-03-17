package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

// SherpaStreamingAsset describes a bundled sherpa-onnx online STT model.
type SherpaStreamingAsset struct {
	Name        string
	ArchiveName string
	URL         string
	DirName     string
	Encoder     string
	EncoderInt8 string
	Decoder     string
	Joiner      string
	JoinerInt8  string
	Tokens      string
}

// SherpaStreamingModel returns metadata for a supported sherpa streaming model.
func SherpaStreamingModel(name string) (SherpaStreamingAsset, error) {
	switch strings.TrimSpace(strings.ToLower(name)) {
	case "", "en", "en-us", "en-2023-06-26":
		return SherpaStreamingAsset{
			Name:        "sherpa-streaming-en",
			ArchiveName: "sherpa-onnx-streaming-zipformer-en-2023-06-26",
			URL:         "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-streaming-zipformer-en-2023-06-26.tar.bz2",
			DirName:     "sherpa-onnx-streaming-zipformer-en-2023-06-26",
			Encoder:     "encoder-epoch-99-avg-1-chunk-16-left-128.onnx",
			EncoderInt8: "encoder-epoch-99-avg-1-chunk-16-left-128.int8.onnx",
			Decoder:     "decoder-epoch-99-avg-1-chunk-16-left-128.onnx",
			Joiner:      "joiner-epoch-99-avg-1-chunk-16-left-128.onnx",
			JoinerInt8:  "joiner-epoch-99-avg-1-chunk-16-left-128.int8.onnx",
			Tokens:      "tokens.txt",
		}, nil
	default:
		return SherpaStreamingAsset{}, fmt.Errorf("unsupported sherpa streaming model %q", name)
	}
}

// ModelDir joins the asset directory to the base models dir.
func (a SherpaStreamingAsset) ModelDir(base string) string {
	return filepath.Join(base, a.DirName)
}

// EncoderFile returns the encoder filename for the desired precision.
func (a SherpaStreamingAsset) EncoderFile(quantized bool) string {
	if quantized && a.EncoderInt8 != "" {
		return a.EncoderInt8
	}
	return a.Encoder
}

// JoinerFile returns the joiner filename for the desired precision.
func (a SherpaStreamingAsset) JoinerFile(quantized bool) string {
	if quantized && a.JoinerInt8 != "" {
		return a.JoinerInt8
	}
	return a.Joiner
}

// RequiredFiles lists the files that must exist after extraction.
func (a SherpaStreamingAsset) RequiredFiles(quantized bool) []string {
	return []string{
		a.EncoderFile(quantized),
		a.Decoder,
		a.JoinerFile(quantized),
		a.Tokens,
	}
}
