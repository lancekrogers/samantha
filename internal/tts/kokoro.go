package tts

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/lancekrogers/samantha/internal/config"
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

// voiceNames lists kokoro-multi-lang-v1_0 voices in order — index = speaker ID.
var voiceNames = []string{
	"af_alloy", "af_aoede", "af_bella", "af_heart", "af_jessica",
	"af_kore", "af_nicole", "af_nova", "af_river", "af_sarah", "af_sky",
	"am_adam", "am_echo", "am_eric", "am_fenrir", "am_liam",
	"am_michael", "am_onyx", "am_puck", "am_santa",
	"bf_alice", "bf_emma", "bf_isabella", "bf_lily",
	"bm_daniel", "bm_fable", "bm_george", "bm_lewis",
}

// voiceToSID maps voice name to speaker ID.
var voiceToSID map[string]int

func init() {
	voiceToSID = make(map[string]int, len(voiceNames))
	for i, name := range voiceNames {
		voiceToSID[name] = i
	}
}

// KokoroTTS implements TTS using sherpa-onnx Kokoro.
type KokoroTTS struct {
	tts   *sherpa.OfflineTts
	voice string
	speed float32
	sid   int
}

// NewKokoroTTS creates a Kokoro TTS provider via sherpa-onnx.
func NewKokoroTTS(cfg *config.Config) (*KokoroTTS, error) {
	modelsDir := config.ModelsDir()

	lang := cfg.Language[:2] // "en-US" -> "en"
	lexicon := filepath.Join(modelsDir, "lexicon-us-en.txt")
	if lang != "en" {
		lexicon = filepath.Join(modelsDir, fmt.Sprintf("lexicon-%s.txt", lang))
	}

	kokoroConfig := sherpa.OfflineTtsKokoroModelConfig{
		Model:       filepath.Join(modelsDir, "model.onnx"),
		Voices:      filepath.Join(modelsDir, "voices.bin"),
		Tokens:      filepath.Join(modelsDir, "tokens.txt"),
		DataDir:     filepath.Join(modelsDir, "espeak-ng-data"),
		DictDir:     filepath.Join(modelsDir, "dict"),
		Lexicon:     lexicon,
		LengthScale: 1.0,
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

	// Map voice name to speaker ID.
	sid, ok := voiceToSID[cfg.TTSVoice]
	if !ok {
		sid = voiceToSID["af_heart"] // fallback
	}

	return &KokoroTTS{
		tts:   tts,
		voice: cfg.TTSVoice,
		speed: float32(cfg.SpeechSpeed),
		sid:   sid,
	}, nil
}

// Synthesize streams synthesized PCM frames for the given text.
func (k *KokoroTTS) Synthesize(ctx context.Context, text string) (*audio.PCMStream, error) {
	stream := audio.NewPCMStream()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				stream.CloseWithError(fmt.Errorf("kokoro panic: %v", r))
			}
		}()

		if ctx.Err() != nil {
			stream.CloseWithError(ctx.Err())
			return
		}

		audioResult := k.tts.Generate(text, k.sid, k.speed)
		if audioResult == nil {
			stream.CloseWithError(fmt.Errorf("TTS generation returned nil"))
			return
		}

		if err := stream.SetSampleRate(audioResult.SampleRate); err != nil {
			stream.CloseWithError(err)
			return
		}

		const chunkSize = 2048
		for start := 0; start < len(audioResult.Samples); start += chunkSize {
			if ctx.Err() != nil {
				stream.CloseWithError(ctx.Err())
				return
			}

			end := start + chunkSize
			if end > len(audioResult.Samples) {
				end = len(audioResult.Samples)
			}

			if err := stream.Write(audioResult.Samples[start:end]); err != nil {
				stream.CloseWithError(err)
				return
			}
		}

		stream.Close()
	}()

	return stream, nil
}

// Available returns true if TTS is ready.
func (k *KokoroTTS) Available() bool {
	return k.tts != nil
}

// ListVoices returns available Kokoro voices with optional filtering.
func (k *KokoroTTS) ListVoices(locale, gender string) []Voice {
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
