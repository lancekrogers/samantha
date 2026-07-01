//go:build !integration

package cmd

import (
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/config"
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
