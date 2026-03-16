package tts

import (
	"fmt"
	"path/filepath"
	"strings"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/Obedience-Corp/samantha/internal/config"
)

// langMap maps voice name prefixes to locales.
var langMap = map[byte]string{
	'a': "en-US", 'b': "en-GB", 'e': "es", 'f': "fr",
	'h': "hi", 'i': "it", 'j': "ja", 'p': "pt", 'z': "zh",
}

// genderMap maps voice name gender chars to labels.
var genderMap = map[byte]string{
	'f': "Female", 'm': "Male",
}

// KokoroTTS implements TTS using sherpa-onnx Kokoro.
type KokoroTTS struct {
	tts   *sherpa.OfflineTts
	voice string
	speed float32
	sid   int // speaker ID (0-based index)
}

// NewKokoroTTS creates a Kokoro TTS provider via sherpa-onnx.
func NewKokoroTTS(cfg *config.Config) (*KokoroTTS, error) {
	modelsDir := config.ModelsDir()

	kokoroConfig := sherpa.OfflineTtsKokoroModelConfig{
		Model:   filepath.Join(modelsDir, "model.onnx"),
		Voices:  filepath.Join(modelsDir, "voices.bin"),
		Tokens:  filepath.Join(modelsDir, "tokens.txt"),
		DataDir: filepath.Join(modelsDir, "espeak-ng-data"),
	}

	modelConfig := sherpa.OfflineTtsModelConfig{
		Kokoro: kokoroConfig,
	}

	ttsConfig := sherpa.OfflineTtsConfig{
		Model: modelConfig,
	}

	tts := sherpa.NewOfflineTts(&ttsConfig)
	if tts == nil {
		return nil, fmt.Errorf("failed to create Kokoro TTS (models dir: %s)", modelsDir)
	}

	return &KokoroTTS{
		tts:   tts,
		voice: cfg.TTSVoice,
		speed: float32(cfg.SpeechSpeed),
		sid:   0, // default speaker
	}, nil
}

// Generate produces audio from text.
func (k *KokoroTTS) Generate(text string) ([]float32, int, error) {
	audio := k.tts.Generate(text, k.sid, k.speed)
	if audio == nil {
		return nil, 0, fmt.Errorf("TTS generation returned nil")
	}
	return audio.Samples, audio.SampleRate, nil
}

// Available returns true if TTS is ready.
func (k *KokoroTTS) Available() bool {
	return k.tts != nil
}

// ListVoices returns available Kokoro voices with optional filtering.
func (k *KokoroTTS) ListVoices(locale, gender string) []Voice {
	// Kokoro voice names follow pattern: {lang}{gender}_{name}
	// e.g., af_heart = American Female Heart
	names := []string{
		"af_alloy", "af_aoede", "af_bella", "af_heart", "af_jessica",
		"af_kore", "af_nicole", "af_nova", "af_river", "af_sarah", "af_sky",
		"am_adam", "am_echo", "am_eric", "am_fenrir", "am_liam",
		"am_michael", "am_onyx", "am_puck", "am_santa",
		"bf_alice", "bf_emma", "bf_isabella", "bf_lily",
		"bm_daniel", "bm_fable", "bm_george", "bm_lewis",
	}

	var voices []Voice
	for _, name := range names {
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
			FriendlyName: fmt.Sprintf("Kokoro %s (%s)", strings.Title(vName), vLocale),
			Gender:       vGender,
			Locale:       vLocale,
		})
	}
	return voices
}

// Delete frees TTS resources.
func (k *KokoroTTS) Delete() {
	if k.tts != nil {
		sherpa.DeleteOfflineTts(k.tts)
	}
}
