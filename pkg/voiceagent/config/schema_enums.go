package config

import (
	"sort"

	"github.com/lancekrogers/samantha/pkg/voiceagent/qwen"
)

// Enum values are sourced from the package that owns the domain wherever the
// import graph allows it, so a new provider or preset cannot appear in samantha
// and be invisible to a front end.
//
// config sits below tts, brain and calibre in the import graph, so those four
// enums are literal here and TestSchemaEnumsMatchOwningPackages (an external
// test, which may import them) fails the build when they drift.

// ttsProviderEnum mirrors tts.Providers().
func ttsProviderEnum() []string { return []string{"kokoro", "qwen3-tts"} }

// kokoroVoiceEnum mirrors tts.VoiceNames() — the same list `samantha voices`
// prints, in speaker-id order.
func kokoroVoiceEnum() []string {
	return []string{
		"af_alloy", "af_aoede", "af_bella", "af_heart", "af_jessica",
		"af_kore", "af_nicole", "af_nova", "af_river", "af_sarah", "af_sky",
		"am_adam", "am_echo", "am_eric", "am_fenrir", "am_liam",
		"am_michael", "am_onyx", "am_puck", "am_santa",
		"bf_alice", "bf_emma", "bf_isabella", "bf_lily",
		"bm_daniel", "bm_fable", "bm_george", "bm_lewis",
	}
}

// brainProviderEnum mirrors brain.Providers().
func brainProviderEnum() []string { return []string{"claude", "grok", "ollama"} }

// ollamaThinkEnum mirrors the canonical values brain's think-level parser
// accepts. The parser also takes off/0/no and on/1/yes as aliases; those are
// not offered as choices.
func ollamaThinkEnum() []string {
	return []string{"false", "true", "low", "medium", "high", "max"}
}

// qwenVoiceModeEnum mirrors the tts.VoiceMode constants a Qwen worker accepts.
func qwenVoiceModeEnum() []string {
	return []string{"customvoice", "voicedesign", "approved_clone"}
}

// calibreFormatEnum mirrors the format preference order in internal/calibre.
func calibreFormatEnum() []string {
	return []string{"epub", "pdf", "mobi", "azw3", "azw", "prc"}
}

// sttProviderEnum is sourced from the alias table that resolves the key.
// The empty alias is a valid config value but not an offered choice.
func sttProviderEnum() []string {
	out := make([]string, 0, len(sttAliasTable))
	for alias := range sttAliasTable {
		if alias == "" {
			continue
		}
		out = append(out, alias)
	}
	sort.Strings(out)
	return out
}

// sttModeEnum is sourced from the modes each normalized provider accepts.
func sttModeEnum() []string {
	seen := map[string]bool{}
	var out []string
	for _, modes := range sttProviderModes {
		for _, mode := range modes {
			if !seen[mode] {
				seen[mode] = true
				out = append(out, mode)
			}
		}
	}
	sort.Strings(out)
	return out
}

// qwenVoiceEnum is sourced from the Qwen preset registry.
func qwenVoiceEnum() []string {
	voices := qwen.CustomVoices()
	out := make([]string, 0, len(voices))
	for _, voice := range voices {
		out = append(out, voice.Name)
	}
	return out
}

// qwenLanguageEnum is sourced from the Qwen language table.
func qwenLanguageEnum() []string { return qwen.SupportedLanguages() }

// qwenTierEnum is sourced from the native package's tier list.
func qwenTierEnum() []string { return qwen.ModelTiers() }
