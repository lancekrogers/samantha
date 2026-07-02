//go:build !integration

package cmd

import (
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/render"
)

func TestSynthIdentityIncludesEffectiveVoiceAndSpeed(t *testing.T) {
	base := synthIdentityFor(&config.Config{
		TTSProvider: "kokoro",
		TTSVoice:    "af_heart",
		SpeechSpeed: 1,
	})
	if !strings.Contains(base, "voice=af_heart") || !strings.Contains(base, "speed=1") {
		t.Fatalf("identity = %q, want effective voice and speed", base)
	}

	revoice := synthIdentityFor(&config.Config{TTSProvider: "kokoro", TTSVoice: "af_bella", SpeechSpeed: 1})
	if revoice == base {
		t.Fatal("changing the effective config voice must change the synth identity")
	}

	respeed := synthIdentityFor(&config.Config{TTSProvider: "kokoro", TTSVoice: "af_heart", SpeechSpeed: 0.95})
	if respeed == base {
		t.Fatal("changing the effective config speed must change the synth identity")
	}
}

// TestApplyVoiceOverridesRecordsEffectiveValues guards manifest auditability:
// a config-driven render (no CLI flags) must still end up with the effective
// voice/speed in opts, which is what manifests and resume keys record.
func TestApplyVoiceOverridesRecordsEffectiveValues(t *testing.T) {
	cfg := &config.Config{TTSVoice: "af_heart", SpeechSpeed: 1.1}
	opts := render.Options{}
	applyVoiceOverrides(cfg, &opts)
	if opts.Voice != "af_heart" || opts.Speed != 1.1 {
		t.Fatalf("opts = %q/%v, want config-derived af_heart/1.1", opts.Voice, opts.Speed)
	}

	cfg = &config.Config{TTSVoice: "af_heart", SpeechSpeed: 1.1}
	opts = render.Options{Voice: "bm_fable", Speed: 0.9}
	applyVoiceOverrides(cfg, &opts)
	if cfg.TTSVoice != "bm_fable" || cfg.SpeechSpeed != 0.9 {
		t.Fatalf("cfg = %q/%v, want CLI overrides applied", cfg.TTSVoice, cfg.SpeechSpeed)
	}
	if opts.Voice != "bm_fable" || opts.Speed != 0.9 {
		t.Fatalf("opts = %q/%v, want CLI values", opts.Voice, opts.Speed)
	}
}
