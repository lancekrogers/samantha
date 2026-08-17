// Package config_test holds the schema checks that must import packages which
// themselves import config. config sits below tts, brain and calibre in the
// import graph, so those enums cannot be sourced from the owning package at
// runtime; sourcing them here means a provider, voice or format added upstream
// fails this build until the schema lists it.
package config_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/calibre"
	"github.com/lancekrogers/samantha/pkg/voiceagent/brain"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	"github.com/lancekrogers/samantha/pkg/voiceagent/qwen"
	"github.com/lancekrogers/samantha/pkg/voiceagent/tts"
)

func TestSchemaEnumsMatchOwningPackages(t *testing.T) {
	brainProviders := make([]string, 0, 3)
	for _, spec := range brain.Providers() {
		brainProviders = append(brainProviders, spec.Name)
	}
	ttsProviders := make([]string, 0, 2)
	for _, spec := range tts.Providers() {
		ttsProviders = append(ttsProviders, spec.Name)
	}
	qwenVoices := make([]string, 0, 9)
	for _, voice := range qwen.CustomVoices() {
		qwenVoices = append(qwenVoices, voice.Name)
	}

	cases := []struct {
		key   string
		owner string
		want  []string
	}{
		{"tts_provider", "tts.Providers", ttsProviders},
		{"voice_fallback_provider", "tts.Providers", ttsProviders},
		{"tts_voice", "tts.VoiceNames", tts.VoiceNames()},
		{"brain_provider", "brain.Providers", brainProviders},
		{"qwen_tts_voice", "qwen.CustomVoices", qwenVoices},
		{"qwen_tts_language", "qwen.SupportedLanguages", qwen.SupportedLanguages()},
		{"qwen_tts_model_tier", "qwen.ModelTiers", qwen.ModelTiers()},
		{"qwen_tts_mode", "tts.VoiceMode constants", []string{
			string(tts.VoiceModeCustomVoice),
			string(tts.VoiceModeVoiceDesign),
			string(tts.VoiceModeApprovedClone),
		}},
		{"calibre_prefer_format", "calibre.SupportedFormats", calibre.SupportedFormats()},
		{"ollama_think", "brain.ThinkLevels", brain.ThinkLevels()},
	}

	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			spec, ok := config.SpecFor(tc.key)
			if !ok {
				t.Fatalf("%s missing from the schema", tc.key)
			}
			if spec.Type != config.TypeEnum {
				t.Fatalf("%s is %s, expected enum", tc.key, spec.Type)
			}
			if !sameSet(spec.Enum, tc.want) {
				t.Errorf("%s enum drifted from %s:\n schema: %v\n  owner: %v",
					tc.key, tc.owner, spec.Enum, tc.want)
			}
		})
	}
}

// The set equality above pins what the schema offers against what brain
// publishes. This pins that list against the parser itself, so a level nobody
// can actually select cannot survive in either table.
func TestSchemaThinkEnumIsAcceptedByTheParser(t *testing.T) {
	spec, ok := config.SpecFor("ollama_think")
	if !ok {
		t.Fatal("ollama_think missing from the schema")
	}
	if len(spec.Enum) == 0 {
		t.Fatal("ollama_think has no offered values")
	}
	for _, value := range spec.Enum {
		if !brain.ThinkLevelAccepted(value) {
			t.Errorf("ollama_think offers %q, which the think-level parser rejects", value)
		}
	}
	if brain.ThinkLevelAccepted("sometimes") {
		t.Error("the parser accepts anything, so this check proves nothing")
	}
}

func sameSet(got, want []string) bool {
	g := append([]string(nil), got...)
	w := append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	return strings.Join(g, "\x00") == strings.Join(w, "\x00")
}
