package config

import (
	"strings"
	"testing"
)

// sampleManifest expresses the five asset shapes Samantha ships today using the
// manifest structs, proving they are expressive enough to represent the current
// VAD, sherpa offline, sherpa streaming, whisper.cpp, and Kokoro assets. It is
// pure data — no network — and is the representation contract for tasks that map
// the real definitions onto it.
func sampleManifest() AssetManifest {
	return AssetManifest{
		Schema: AssetSchema,
		Assets: []Asset{
			{
				ID:       "vad.silero.v1",
				Provider: "sherpa",
				Kind:     AssetKindVAD,
				Name:     "silero_vad.onnx",
				Files: []AssetFile{{
					Path: "silero_vad.onnx",
					URL:  "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/silero_vad.onnx",
				}},
			},
			{
				ID:         "stt.sherpa.offline.whisper-base.en",
				Provider:   "sherpa",
				Mode:       "offline",
				Kind:       AssetKindSTT,
				Name:       "whisper-base.en",
				Archive:    &AssetArchive{URL: "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-whisper-base.en.tar.bz2", StripPrefix: true},
				CheckFiles: []string{"base.en-encoder.onnx", "base.en-decoder.onnx"},
			},
			{
				ID:        "stt.sherpa.streaming.zipformer-en",
				Provider:  "sherpa",
				Mode:      "streaming",
				Kind:      AssetKindSTT,
				Name:      "sherpa-streaming-en",
				TargetDir: "sherpa-onnx-streaming-zipformer-en-2023-06-26",
				Archive:   &AssetArchive{URL: "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-streaming-zipformer-en-2023-06-26.tar.bz2", StripPrefix: true},
				CheckFiles: []string{
					"encoder-epoch-99-avg-1-chunk-16-left-128.onnx",
					"decoder-epoch-99-avg-1-chunk-16-left-128.onnx",
					"joiner-epoch-99-avg-1-chunk-16-left-128.onnx",
					"tokens.txt",
				},
			},
			{
				ID:       "stt.whispercpp.base.en",
				Provider: "whispercpp",
				Mode:     "cli",
				Kind:     AssetKindSTT,
				Name:     "whispercpp-base.en",
				Files: []AssetFile{{
					Path: "whispercpp/ggml-base.en.bin",
					URL:  "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-base.en.bin",
				}},
			},
			{
				ID:         "tts.kokoro.multi-lang-v1_0",
				Provider:   "kokoro",
				Kind:       AssetKindTTS,
				Name:       "kokoro-tts",
				Archive:    &AssetArchive{URL: "https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/kokoro-multi-lang-v1_0.tar.bz2", StripPrefix: true},
				CheckFiles: []string{"model.onnx", "voices.bin", "tokens.txt", "espeak-ng-data"},
			},
		},
	}
}

func TestAssetManifestRepresentsCurrentAssets(t *testing.T) {
	m := sampleManifest()
	if err := m.Validate(); err != nil {
		t.Fatalf("sample manifest failed validation: %v", err)
	}
	if len(m.Assets) != 5 {
		t.Fatalf("manifest assets = %d, want 5 (vad, sherpa offline, sherpa streaming, whispercpp, kokoro)", len(m.Assets))
	}

	byID := make(map[string]Asset, len(m.Assets))
	for _, a := range m.Assets {
		byID[a.ID] = a
	}

	// File assets vs archive assets are distinguishable.
	if byID["vad.silero.v1"].IsArchive() {
		t.Error("VAD asset should be a file asset, not an archive")
	}
	if !byID["tts.kokoro.multi-lang-v1_0"].IsArchive() {
		t.Error("Kokoro asset should be an archive")
	}

	kinds := map[AssetKind]int{}
	for _, a := range m.Assets {
		kinds[a.Kind]++
	}
	if kinds[AssetKindVAD] != 1 || kinds[AssetKindSTT] != 3 || kinds[AssetKindTTS] != 1 {
		t.Fatalf("asset kinds = %v, want 1 vad / 3 stt / 1 tts", kinds)
	}
}

func TestAssetValidateFileAsset(t *testing.T) {
	good := Asset{
		ID: "f", Provider: "p", Kind: AssetKindVAD,
		Files: []AssetFile{{Path: "x.onnx", URL: "https://example/x"}},
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid file asset rejected: %v", err)
	}

	cases := map[string]Asset{
		"missing path": {ID: "f", Provider: "p", Kind: AssetKindVAD, Files: []AssetFile{{URL: "https://example/x"}}},
		"missing url":  {ID: "f", Provider: "p", Kind: AssetKindVAD, Files: []AssetFile{{Path: "x.onnx"}}},
	}
	for name, a := range cases {
		if err := a.Validate(); err == nil {
			t.Errorf("%s: expected validation error, got nil", name)
		}
	}
}

func TestAssetValidateArchiveAsset(t *testing.T) {
	good := Asset{
		ID: "a", Provider: "p", Kind: AssetKindTTS,
		Archive:    &AssetArchive{URL: "https://example/a.tar.bz2", StripPrefix: true},
		CheckFiles: []string{"model.onnx"},
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid archive asset rejected: %v", err)
	}

	noCheck := good
	noCheck.CheckFiles = nil
	if err := noCheck.Validate(); err == nil {
		t.Error("archive without check_files should fail validation")
	}

	noURL := Asset{ID: "a", Provider: "p", Kind: AssetKindTTS, Archive: &AssetArchive{}, CheckFiles: []string{"x"}}
	if err := noURL.Validate(); err == nil {
		t.Error("archive without url should fail validation")
	}
}

func TestAssetValidateRejectsBothOrNeitherSource(t *testing.T) {
	both := Asset{
		ID: "x", Provider: "p", Kind: AssetKindSTT,
		Files:      []AssetFile{{Path: "f", URL: "u"}},
		Archive:    &AssetArchive{URL: "u"},
		CheckFiles: []string{"c"},
	}
	if err := both.Validate(); err == nil {
		t.Error("asset with both files and archive should fail validation")
	}

	neither := Asset{ID: "x", Provider: "p", Kind: AssetKindSTT}
	if err := neither.Validate(); err == nil {
		t.Error("asset with neither files nor archive should fail validation")
	}
}

func TestAssetValidateRequiresIdentity(t *testing.T) {
	cases := map[string]Asset{
		"missing id":       {Provider: "p", Kind: AssetKindSTT, Files: []AssetFile{{Path: "f", URL: "u"}}},
		"missing provider": {ID: "x", Kind: AssetKindSTT, Files: []AssetFile{{Path: "f", URL: "u"}}},
		"invalid kind":     {ID: "x", Provider: "p", Kind: AssetKind("bogus"), Files: []AssetFile{{Path: "f", URL: "u"}}},
	}
	for name, a := range cases {
		if err := a.Validate(); err == nil {
			t.Errorf("%s: expected validation error, got nil", name)
		}
	}
}

func TestAssetManifestValidateRejectsSchemaAndDuplicates(t *testing.T) {
	wrongSchema := AssetManifest{Schema: "samantha.assets.v0"}
	if err := wrongSchema.Validate(); err == nil || !strings.Contains(err.Error(), "schema") {
		t.Errorf("wrong schema error = %v, want a schema error", err)
	}

	dup := AssetManifest{
		Schema: AssetSchema,
		Assets: []Asset{
			{ID: "dup", Provider: "p", Kind: AssetKindVAD, Files: []AssetFile{{Path: "f", URL: "u"}}},
			{ID: "dup", Provider: "p", Kind: AssetKindVAD, Files: []AssetFile{{Path: "g", URL: "u"}}},
		},
	}
	if err := dup.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("duplicate id error = %v, want a duplicate error", err)
	}
}
