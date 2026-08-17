package tts

import (
	"fmt"
	"strings"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	managedqwen "github.com/lancekrogers/samantha/pkg/voiceagent/qwen"
)

// ProviderSpec describes a TTS provider compiled into this build.
type ProviderSpec struct {
	Name        string
	Description string
}

var providerSpecs = []ProviderSpec{
	{Name: "kokoro", Description: "Local Kokoro TTS"},
	{Name: "qwen3-tts", Description: "Qwen3-TTS preset voices (native worker preferred; managed Python legacy)"},
}

// Providers returns the list of implemented TTS providers.
func Providers() []ProviderSpec {
	out := make([]ProviderSpec, len(providerSpecs))
	copy(out, providerSpecs)
	return out
}

// NewProvider constructs the configured TTS provider and its cleanup hook.
func NewProvider(cfg *config.Config) (Provider, func(), error) {
	switch strings.TrimSpace(strings.ToLower(cfg.TTSProvider)) {
	case "", "kokoro":
		provider, err := NewKokoroTTS(cfg)
		if err != nil {
			return nil, nil, err
		}
		return provider, provider.Delete, nil
	case "qwen3-tts":
		provider, err := NewQwen3TTS(cfg)
		if err != nil {
			return nil, nil, err
		}
		return provider, provider.Delete, nil
	default:
		return nil, nil, unsupportedProviderError(cfg.TTSProvider)
	}
}

// StaticVoices returns the compile-time voice catalog for providerName,
// filtered by locale/gender. Every provider's live ListVoices is built from
// this same catalog (kokoro directly; qwen3-tts's CustomVoice mode from the
// same qwen.CustomVoices() registry), so the two can never drift, and a
// caller that must never construct a provider or touch a model file — e.g.
// `voices --json` on a fresh machine — can still list real voice names.
func StaticVoices(providerName, locale, gender string) ([]Voice, error) {
	switch strings.TrimSpace(strings.ToLower(providerName)) {
	case "", "kokoro":
		return staticKokoroVoices(locale, gender), nil
	case "qwen3-tts":
		return staticQwenVoices(locale, gender), nil
	default:
		return nil, unsupportedProviderError(providerName)
	}
}

func staticKokoroVoices(locale, gender string) []Voice {
	var voices []Voice
	for _, name := range voiceNames {
		if len(name) < 3 || !strings.Contains(name, "_") {
			continue
		}
		vLocale := langMap[name[0]]
		vGender := genderMap[name[1]]
		vName := strings.SplitN(name, "_", 2)[1]

		if locale != "" && !strings.HasPrefix(vLocale, locale) {
			continue
		}
		if gender != "" && !strings.EqualFold(vGender, gender) {
			continue
		}

		voices = append(voices, Voice{
			Name:         name,
			FriendlyName: fmt.Sprintf("Kokoro %s (%s)", titleCase(vName), vLocale),
			Gender:       vGender,
			Locale:       vLocale,
		})
	}
	return voices
}

// staticQwenVoices maps the 9 CustomVoice presets qwen.CustomVoices() ships —
// they exist whether or not the native package is installed, so a picker can
// list them before anything has been downloaded. Gender is not something the
// preset registry tracks per voice, so every row reports "preset" — the same
// convention internal/tui/settings_qwen.go already uses for this exact
// mapping — rather than guessing from the description text.
func staticQwenVoices(locale, gender string) []Voice {
	const presetGender = "preset"
	if gender != "" && !strings.EqualFold(gender, presetGender) {
		return nil
	}
	var voices []Voice
	for _, v := range managedqwen.CustomVoices() {
		if locale != "" && !strings.HasPrefix(v.NativeLanguage, locale) {
			continue
		}
		voices = append(voices, Voice{
			Name:         v.Name,
			FriendlyName: v.Description,
			Gender:       presetGender,
			Locale:       v.NativeLanguage,
		})
	}
	return voices
}

func unsupportedProviderError(configured string) error {
	names := make([]string, 0, len(providerSpecs))
	for _, spec := range providerSpecs {
		names = append(names, spec.Name)
	}
	return fmt.Errorf("unsupported tts_provider %q (implemented providers: %s)", configured, strings.Join(names, ", "))
}

// titleCase upper-cases the first letter of each word — a small ASCII stand-in
// for the deprecated strings.Title, used only for Kokoro voice display names.
// Matches strings.Title's word boundaries: letters, digits, and '_' are word
// characters, so only a letter following a separator (or the start) is raised.
func titleCase(s string) string {
	prevIsWord := false
	return strings.Map(func(r rune) rune {
		isWord := r == '_' ||
			(r >= '0' && r <= '9') ||
			(r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z')
		if !prevIsWord && r >= 'a' && r <= 'z' {
			r -= 'a' - 'A'
		}
		prevIsWord = isWord
		return r
	}, s)
}
