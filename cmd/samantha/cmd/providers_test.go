//go:build !integration

package cmd

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/pkg/voiceagent/brain"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	"github.com/lancekrogers/samantha/pkg/voiceagent/stt"
	"github.com/lancekrogers/samantha/pkg/voiceagent/tts"
)

// --- resolveSTTActive ---

func TestResolveSTTActiveCompoundAlias(t *testing.T) {
	cfg := &config.Config{STTProvider: "sherpa-streaming"}
	active, mode := resolveSTTActive(cfg)
	if active != "sherpa-streaming" || mode != "streaming" {
		t.Fatalf("resolveSTTActive() = %q, %q, want sherpa-streaming, streaming", active, mode)
	}
}

func TestResolveSTTActiveProviderPlusMode(t *testing.T) {
	cfg := &config.Config{STTProvider: "sherpa", STTMode: "streaming"}
	active, mode := resolveSTTActive(cfg)
	if active != "sherpa-streaming" || mode != "streaming" {
		t.Fatalf("resolveSTTActive() = %q, %q, want sherpa-streaming, streaming (matches the spec's own example)", active, mode)
	}
}

func TestResolveSTTActiveFallsBackOnInvalidConfig(t *testing.T) {
	cfg := &config.Config{STTProvider: "bogus-provider"}
	active, mode := resolveSTTActive(cfg)
	if active != "bogus-provider" || mode != "" {
		t.Fatalf("resolveSTTActive() = %q, %q, want the raw configured value with no mode (doctor reports the error)", active, mode)
	}
}

// --- buildProvidersJSON (MDL-A4) ---

func TestBuildProvidersJSONShapeAndActiveFlags(t *testing.T) {
	cfg := &config.Config{BrainProvider: "ollama", TTSProvider: "kokoro", STTProvider: "sherpa-streaming"}
	doc := buildProvidersJSON(cfg)

	if doc.Brain.Active != "ollama" {
		t.Errorf("brain.active = %q, want ollama", doc.Brain.Active)
	}
	if len(doc.Brain.Providers) != len(brain.Providers()) {
		t.Errorf("brain.providers has %d entries, want %d (unchanged from brain.Providers())", len(doc.Brain.Providers), len(brain.Providers()))
	}
	var sawActiveOllama bool
	for _, p := range doc.Brain.Providers {
		if p.Name == "ollama" {
			sawActiveOllama = p.Active
		}
		if !p.Implemented {
			t.Errorf("brain provider %q implemented = false, want true (it came from brain.Providers() itself)", p.Name)
		}
	}
	if !sawActiveOllama {
		t.Error("ollama row must be marked active")
	}

	if doc.TTS.Active != "kokoro" {
		t.Errorf("tts.active = %q, want kokoro", doc.TTS.Active)
	}
	if len(doc.TTS.Providers) != len(tts.Providers()) {
		t.Errorf("tts.providers has %d entries, want %d", len(doc.TTS.Providers), len(tts.Providers()))
	}

	if doc.STT.Active != "sherpa-streaming" {
		t.Errorf("stt.active = %q, want sherpa-streaming", doc.STT.Active)
	}
	if doc.STT.Configured != "sherpa-streaming" {
		t.Errorf("stt.configured = %q, want the raw cfg.STTProvider value", doc.STT.Configured)
	}
	if doc.STT.Mode != "streaming" {
		t.Errorf("stt.mode = %q, want streaming", doc.STT.Mode)
	}
	if len(doc.STT.Providers) != len(stt.Providers()) {
		t.Errorf("stt.providers has %d entries, want %d", len(doc.STT.Providers), len(stt.Providers()))
	}
	var sawStreamingDetail bool
	for _, p := range doc.STT.Providers {
		if p.Name == "sherpa-streaming" && p.Detail != "" {
			sawStreamingDetail = true
		}
	}
	if !sawStreamingDetail {
		t.Error("sherpa-streaming row should carry a non-empty detail (mirrors the human path's sttSpecDetail line)")
	}
}

func TestBuildProvidersJSONDefaultsWhenUnset(t *testing.T) {
	doc := buildProvidersJSON(&config.Config{})
	if doc.Brain.Active != "ollama" {
		t.Errorf("brain.active default = %q, want ollama", doc.Brain.Active)
	}
	if doc.TTS.Active != "kokoro" {
		t.Errorf("tts.active default = %q, want kokoro", doc.TTS.Active)
	}
}

func TestBuildProvidersJSONSTTMisconfiguredHasNoActiveRow(t *testing.T) {
	doc := buildProvidersJSON(&config.Config{STTProvider: "bogus"})
	if doc.STT.Active != "bogus" || doc.STT.Configured != "bogus" {
		t.Fatalf("stt active/configured = %q/%q, want the raw bogus value surfaced, not silently dropped", doc.STT.Active, doc.STT.Configured)
	}
	for _, p := range doc.STT.Providers {
		if p.Active {
			t.Errorf("no provider row should match an unsupported configured value, got %q active", p.Name)
		}
	}
}

// --- runProviders --json is offline (no provider construction, no network) ---

func TestRunProvidersJSONIsMachineReadableAndOffline(t *testing.T) {
	// A models dir that does not exist: if runProviders ever tried to
	// construct a provider or touch assets, a qwen3-tts config would need
	// EnsureRuntimeAssets first. It must not.
	cfg := &config.Config{BrainProvider: "ollama", TTSProvider: "qwen3-tts", STTProvider: "sherpa", ModelsDir: "/nonexistent/does-not-exist"}
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := runProviders(cmd, cfg, true); err != nil {
		t.Fatalf("runProviders(json) error = %v", err)
	}

	var doc providersJSONDoc
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\n%s", err, buf.String())
	}
	if doc.TTS.Active != "qwen3-tts" {
		t.Errorf("tts.active = %q, want qwen3-tts", doc.TTS.Active)
	}
}

func TestProvidersCommandRegistersJSONFlag(t *testing.T) {
	if providersCmd.Flags().Lookup("json") == nil {
		t.Error("providers command missing --json flag")
	}
}
