package tts

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/textclean"
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

const kokoroProviderName = "kokoro"

// KokoroTTS implements TTS using sherpa-onnx Kokoro.
type KokoroTTS struct {
	mu         sync.Mutex
	tts        *sherpa.OfflineTts
	alive      atomic.Bool
	voice      string
	speed      float32
	sid        int
	sampleRate int

	// generateFn overrides sherpa generation in tests.
	generateFn func(text string, sid int, speed float32) *sherpa.GeneratedAudio
}

// NewKokoroTTS creates a Kokoro TTS provider via sherpa-onnx.
func NewKokoroTTS(cfg *config.Config) (*KokoroTTS, error) {
	// Prefer thewh1teagle v1.0 English pack (same weights as Python
	// samantha-cli) when present; else multi-lang pack at models root.
	modelsDir := config.KokoroDir()

	lang := config.LanguageCode(cfg.Language) // "en-US" -> "en"
	lexicon := filepath.Join(modelsDir, "lexicon-us-en.txt")
	if lang != "en" {
		// Multi-lang pack only; v1 English pack may lack non-en lexicons.
		alt := filepath.Join(modelsDir, fmt.Sprintf("lexicon-%s.txt", lang))
		if _, err := os.Stat(alt); err == nil {
			lexicon = alt
		} else if root := config.ModelsDir(); root != modelsDir {
			if _, err := os.Stat(filepath.Join(root, fmt.Sprintf("lexicon-%s.txt", lang))); err == nil {
				lexicon = filepath.Join(root, fmt.Sprintf("lexicon-%s.txt", lang))
			}
		}
	}

	// eSpeak emits syllabic-n (U+0329) for words like "wasn't" and "button".
	// Stock Kokoro tokens omit that codepoint, so sherpa skips the phone and
	// speech clips. Alias U+0329 → n in a sidecar tokens file so real
	// contractions and unhyphenated stems work without text mangling.
	tokensPath, err := ensureKokoroTokensWithSyllabicN(modelsDir)
	if err != nil {
		return nil, err
	}

	kokoroConfig := sherpa.OfflineTtsKokoroModelConfig{
		Model:       filepath.Join(modelsDir, "model.onnx"),
		Voices:      filepath.Join(modelsDir, "voices.bin"),
		Tokens:      tokensPath,
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

	k := &KokoroTTS{
		tts:        tts,
		voice:      cfg.TTSVoice,
		speed:      float32(cfg.SpeechSpeed),
		sid:        sid,
		sampleRate: tts.SampleRate(),
	}
	k.alive.Store(true)
	return k, nil
}

// Synthesize streams synthesized PCM frames for the given text.
func (k *KokoroTTS) Synthesize(ctx context.Context, text string) (*audio.PCMStream, error) {
	result, err := k.SynthesizeRequest(ctx, SynthesisRequest{Text: text})
	if err != nil {
		return nil, err
	}
	return result.Stream, nil
}

// SynthesizeRequest streams synthesized PCM frames for a typed request. Empty
// Voice and zero Speed fall back to the configured defaults; an explicit
// unknown voice or non-native sample rate is an error.
func (k *KokoroTTS) SynthesizeRequest(ctx context.Context, req SynthesisRequest) (SynthesisResult, error) {
	voice, sid := k.voice, k.sid
	if req.Voice != "" {
		s, ok := voiceToSID[req.Voice]
		if !ok {
			return SynthesisResult{}, fmt.Errorf("unknown kokoro voice %q", req.Voice)
		}
		voice, sid = req.Voice, s
	}

	speed := k.speed
	if req.Speed != 0 {
		speed = float32(req.Speed)
	}

	if req.SampleRate != 0 && req.SampleRate != k.sampleRate {
		return SynthesisResult{}, fmt.Errorf("kokoro cannot resample to %d Hz (native rate %d Hz)", req.SampleRate, k.sampleRate)
	}

	stream := audio.NewPCMStream(ctx)

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

		preparedText := textclean.PrepareKokoroText(req.Text)
		audio.RecordDebugSynthesis(kokoroProviderName, req.Text, preparedText)
		audioResult := k.generate(preparedText, sid, speed)
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

	return SynthesisResult{
		Stream:     stream,
		SampleRate: k.sampleRate,
		Provider:   kokoroProviderName,
		Model:      "kokoro",
		Voice:      voice,
	}, nil
}

func (k *KokoroTTS) generate(text string, sid int, speed float32) *sherpa.GeneratedAudio {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.generateFn != nil {
		return k.generateFn(text, sid, speed)
	}
	if k.tts == nil {
		return nil // deleted while a synthesis was queued
	}
	return k.tts.Generate(text, sid, speed)
}

// Available returns true if TTS is ready. It reads an atomic flag rather than
// k.tts under k.mu: generate holds the mutex across a whole cgo synthesis, and
// Available is called from the turn loop, which must never block behind one.
func (k *KokoroTTS) Available() bool {
	return k.alive.Load()
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

// Capabilities exposes Kokoro's verified static voice catalog through the
// provider-neutral discovery seam. Kokoro remains the default and does not
// advertise cloning or design controls.
func (k *KokoroTTS) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Provider:               kokoroProviderName,
		Model:                  "kokoro",
		ModelReady:             k.Available(),
		Modes:                  []VoiceModeCapability{{ID: VoiceModeStatic, Voices: k.ListVoices("", "")}},
		Languages:              []string{"en-US", "en-GB", "es", "fr", "hi", "it", "ja", "pt", "zh"},
		SampleRates:            []int{k.sampleRate},
		SupportsPreview:        true,
		SupportsStreaming:      true,
		SupportsCancellation:   true,
		SupportsReferenceAudio: false,
		SupportsSpeed:          true,
	}
}

func (k *KokoroTTS) Status() ProviderStatus {
	return ProviderStatus{Provider: kokoroProviderName, Available: k.Available(), ModelReady: k.Available()}
}

// Delete frees TTS resources. It takes the same mutex as generate: Generate is
// an uncancellable cgo call, and freeing the handle while one is in flight (a
// superseded voice preview, shutdown cleanup) is a use-after-free.
func (k *KokoroTTS) Delete() {
	k.alive.Store(false)
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.tts != nil {
		sherpa.DeleteOfflineTts(k.tts)
		k.tts = nil
	}
}
