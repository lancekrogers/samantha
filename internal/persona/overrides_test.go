package persona

import (
	"sort"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

// appConfig is a plain app-level config: no persona applied, the values a
// `config get` would report.
func appConfig() *config.Config {
	return &config.Config{
		AgentName:        "Samantha",
		Persona:          "samantha",
		ActivePersona:    "samantha",
		BrainProvider:    "ollama",
		OllamaModel:      "qwen2.5:14b",
		GrokModel:        "grok-4",
		TTSProvider:      "kokoro",
		TTSVoice:         "af_heart",
		QwenTTSVoice:     "Vivian",
		QwenTTSModelTier: "0.6b",
	}
}

func TestOverriddenKeysMatchesApply(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())

	tests := []struct {
		name    string
		cfg     func() *config.Config
		profile *Profile
		want    []string
	}{
		{
			name:    "nil profile overrides nothing",
			cfg:     appConfig,
			profile: nil,
			want:    nil,
		},
		{
			name:    "empty profile still claims its own identity",
			cfg:     appConfig,
			profile: &Profile{ID: "ada"},
			want:    []string{"active_persona"},
		},
		{
			name: "display name and prompt",
			cfg:  appConfig,
			profile: &Profile{
				ID:          "ada",
				DisplayName: "Ada",
				Prompts:     PromptRefs{Persona: "ada"},
			},
			want: []string{"agent_name", "persona", "active_persona"},
		},
		{
			name: "ollama model routes to ollama_model",
			cfg:  appConfig,
			profile: &Profile{
				ID:    "ada",
				Brain: Brain{Provider: "ollama", Model: "llama3.1"},
			},
			want: []string{"active_persona", "brain_provider", "ollama_model"},
		},
		{
			name: "grok model routes to grok_model",
			cfg:  appConfig,
			profile: &Profile{
				ID:    "ada",
				Brain: Brain{Provider: "grok", Model: "grok-4-fast"},
			},
			want: []string{"active_persona", "brain_provider", "grok_model"},
		},
		{
			name: "model with no provider follows the config's provider",
			cfg:  appConfig,
			profile: &Profile{
				ID:    "ada",
				Brain: Brain{Model: "llama3.1"},
			},
			want: []string{"active_persona", "ollama_model"},
		},
		{
			name: "claude model is recorded but not routed",
			cfg:  appConfig,
			profile: &Profile{
				ID:    "ada",
				Brain: Brain{Provider: "claude", Model: "sonnet"},
			},
			want: []string{"active_persona", "brain_provider"},
		},
		{
			name: "kokoro voice routes to tts_voice",
			cfg:  appConfig,
			profile: &Profile{
				ID:  "ada",
				TTS: TTS{Provider: "kokoro", Voice: "am_michael"},
			},
			want: []string{"active_persona", "tts_provider", "tts_voice"},
		},
		{
			name: "qwen voice and tier route to the qwen keys",
			cfg:  appConfig,
			profile: &Profile{
				ID:  "uncle-fu",
				TTS: TTS{Provider: "qwen3-tts", Voice: "Uncle_Fu", Tier: "1.7b"},
			},
			want: []string{"active_persona", "tts_provider", "qwen_tts_voice", "qwen_tts_model_tier"},
		},
		{
			name: "a tier without qwen is inert",
			cfg:  appConfig,
			profile: &Profile{
				ID:  "ada",
				TTS: TTS{Provider: "kokoro", Voice: "af_sky", Tier: "1.7b"},
			},
			want: []string{"active_persona", "tts_provider", "tts_voice"},
		},
		{
			name: "voice with no provider follows the config's provider",
			cfg: func() *config.Config {
				cfg := appConfig()
				cfg.TTSProvider = "qwen3-tts"
				return cfg
			},
			profile: &Profile{ID: "ada", TTS: TTS{Voice: "Serena"}},
			want:    []string{"active_persona", "qwen_tts_voice"},
		},
		{
			name: "a value identical to the config's is still an override",
			cfg:  appConfig,
			profile: &Profile{
				ID:          "samantha",
				DisplayName: "Samantha",
				TTS:         TTS{Provider: "kokoro", Voice: "af_heart"},
			},
			want: []string{"agent_name", "active_persona", "tts_provider", "tts_voice"},
		},
		{
			name: "everything at once",
			cfg:  appConfig,
			profile: &Profile{
				ID:          "ada",
				DisplayName: "Ada",
				Brain:       Brain{Provider: "ollama", Model: "llama3.1"},
				TTS:         TTS{Provider: "kokoro", Voice: "af_sky"},
				Prompts:     PromptRefs{Persona: "ada"},
			},
			want: []string{
				"agent_name", "persona", "active_persona",
				"brain_provider", "ollama_model", "tts_provider", "tts_voice",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg()
			before := snapshotRoutes(cfg)

			got := OverriddenKeys(cfg, tt.profile)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("OverriddenKeys = %v, want %v", got, tt.want)
			}
			if snapshotRoutes(cfg) != before {
				t.Error("OverriddenKeys mutated the config it was asked about")
			}

			// The contract: every reported key is one Apply actually changes,
			// unless the profile happens to write the value already there.
			applied := *cfg
			Apply(&applied, tt.profile)
			reported := map[string]bool{}
			for _, key := range got {
				reported[key] = true
			}
			for _, route := range personaRoutes {
				changed := route.read(cfg) != route.read(&applied)
				if changed && !reported[route.key] {
					t.Errorf("Apply changed %s but OverriddenKeys did not report it", route.key)
				}
			}
		})
	}
}

// snapshotRoutes renders every routed key's value, for a cheap mutation check.
func snapshotRoutes(cfg *config.Config) string {
	var parts []string
	for _, route := range personaRoutes {
		parts = append(parts, route.key+"="+route.read(cfg))
	}
	return strings.Join(parts, "\x00")
}

func TestOverriddenKeysIgnoresAnAlreadyOverlaidConfig(t *testing.T) {
	// Apply is idempotent, so a value-diff implementation would report nothing
	// for a config the persona has already been applied to. The probe-based one
	// still names the keys the persona owns.
	config.SetConfigDirForTest(t, t.TempDir())
	profile := &Profile{
		ID:          "ada",
		DisplayName: "Ada",
		Brain:       Brain{Provider: "ollama", Model: "llama3.1"},
		TTS:         TTS{Provider: "kokoro", Voice: "af_sky"},
	}
	cfg := appConfig()
	Apply(cfg, profile)

	got := OverriddenKeys(cfg, profile)
	want := []string{"agent_name", "active_persona", "brain_provider", "ollama_model", "tts_provider", "tts_voice"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("OverriddenKeys = %v, want %v", got, want)
	}
}

func TestOverridableKeysMatchTheSchema(t *testing.T) {
	// The schema's persona_overridable flag and this routing table describe the
	// same set. If they drift, the Mac app badges the wrong controls.
	var fromSchema []string
	for _, spec := range config.Schema() {
		if spec.PersonaOverridable {
			fromSchema = append(fromSchema, spec.Key)
		}
	}
	fromTable := OverridableKeys()
	sort.Strings(fromSchema)
	sort.Strings(fromTable)
	if strings.Join(fromSchema, ",") != strings.Join(fromTable, ",") {
		t.Errorf("persona_overridable in the schema = %v, routing table = %v", fromSchema, fromTable)
	}
}
