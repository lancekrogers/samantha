//go:build !integration

package cmd

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	"github.com/lancekrogers/samantha/pkg/voiceagent/tts"
)

// --- voices --json: source + languages (MDL-A5) ---

func TestVoicesJSONSourceIsProviderWhenAssetsPresent(t *testing.T) {
	personaEnv(t)
	stubVoiceStack(t, kokoroFake(), nil)

	out, err := runVoicesCmd(t, "--json")
	if err != nil {
		t.Fatalf("voices --json error = %v (out %s)", err, out)
	}
	var got voicesJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if got.Source != "provider" {
		t.Errorf("source = %q, want provider (assets present, a live provider was constructed)", got.Source)
	}
	if got.Languages != nil {
		t.Errorf("languages = %v, want omitted for kokoro", got.Languages)
	}
}

func TestVoicesJSONLanguagesFieldOmittedForKokoro(t *testing.T) {
	personaEnv(t)
	stubVoiceStack(t, kokoroFake(), nil)

	out, err := runVoicesCmd(t, "--json")
	if err != nil {
		t.Fatalf("voices --json error = %v (out %s)", err, out)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if _, present := raw["languages"]; present {
		t.Errorf("raw output has a languages key for kokoro, want it fully omitted: %s", out)
	}
}

func TestVoicesJSONQwenStaticFallbackWhenAssetsMissing(t *testing.T) {
	personaEnv(t)
	var newProviderCalled bool
	orig := voiceStack
	voiceStack = voiceStackDeps{
		newProvider: func(*config.Config) (tts.Provider, func(), error) {
			newProviderCalled = true
			return kokoroFake(), nil, nil
		},
		missingAssets: func(cfg *config.Config) []string {
			if isQwenTTS(cfg.TTSProvider) {
				return []string{"Qwen3-TTS native package"}
			}
			return nil
		},
		ensureAssets: func(context.Context, *config.Config, config.AssetRequest, func(string, float64)) error {
			t.Fatal("ensureAssets must never be called by voices --json")
			return nil
		},
	}
	t.Cleanup(func() { voiceStack = orig })

	out, err := runVoicesCmd(t, "--json", "--provider", "qwen3-tts")
	if err != nil {
		t.Fatalf("voices --json error = %v, want the static fallback to succeed (out %s)", err, out)
	}
	var got voicesJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if got.Source != "static" {
		t.Errorf("source = %q, want static (assets missing, no provider constructed)", got.Source)
	}
	if newProviderCalled {
		t.Error("newProvider was called; the static fallback must never construct a provider")
	}
	if len(got.Voices) != 9 {
		t.Fatalf("voices = %+v, want the 9 CustomVoice presets", got.Voices)
	}
	if len(got.Languages) == 0 {
		t.Error("languages is empty, want qwen.SupportedLanguages()")
	}
}

func TestVoicesJSONQwenLanguagesPresentOnLiveProviderPath(t *testing.T) {
	personaEnv(t)
	stubVoiceStack(t, &fakeTTS{voices: []tts.Voice{{Name: "Vivian", FriendlyName: "Vivian", Gender: "preset", Locale: "Chinese"}}}, nil)

	out, err := runVoicesCmd(t, "--json", "--provider", "qwen3-tts")
	if err != nil {
		t.Fatalf("voices --json error = %v (out %s)", err, out)
	}
	var got voicesJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if got.Source != "provider" {
		t.Errorf("source = %q, want provider (assets present)", got.Source)
	}
	if len(got.Languages) == 0 {
		t.Error("languages is empty, want qwen.SupportedLanguages() regardless of source")
	}
}

// A provider whose static catalog is genuinely empty (or errors) must keep
// the original assets_missing refusal rather than returning an empty static
// success — the caller still needs a clear signal to run models ensure.
func TestVoicesJSONMissingAssetsStillRefusesForNonQwenProviders(t *testing.T) {
	personaEnv(t)
	ensured := stubVoiceStack(t, kokoroFake(), []string{"Kokoro model"})

	out, err := runVoicesCmd(t, "--json")
	if err == nil {
		t.Fatalf("voices --json error = nil, want assets_missing preserved for kokoro (out %s)", out)
	}
	if code, _, _ := decodeErrorJSON(t, out); code != codeAssetsMissing {
		t.Fatalf("code = %q, want %q", code, codeAssetsMissing)
	}
	if *ensured != 0 {
		t.Fatalf("ensureAssets called %d times, want 0", *ensured)
	}
}
