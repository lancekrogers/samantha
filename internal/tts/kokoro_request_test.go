package tts

import (
	"context"
	"errors"
	"strings"
	"testing"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/lancekrogers/samantha/internal/audio"
)

var _ RequestProvider = (*KokoroTTS)(nil)

type generateCall struct {
	text  string
	sid   int
	speed float32
}

func newTestKokoro(gen func(text string, sid int, speed float32) *sherpa.GeneratedAudio) *KokoroTTS {
	k := &KokoroTTS{
		voice:      "af_heart",
		speed:      1.0,
		sid:        voiceToSID["af_heart"],
		sampleRate: 24000,
		generateFn: gen,
	}
	k.alive.Store(true)
	return k
}

func drainStream(t *testing.T, s *audio.PCMStream) []float32 {
	t.Helper()
	var samples []float32
	for frame := range s.Frames() {
		samples = append(samples, frame...)
	}
	if err := s.Err(); err != nil {
		t.Fatalf("stream error = %v", err)
	}
	return samples
}

func TestSynthesizeRequestRejectsUnknownVoice(t *testing.T) {
	k := newTestKokoro(nil)

	_, err := k.SynthesizeRequest(context.Background(), SynthesisRequest{Text: "hi", Voice: "not_a_voice"})
	if err == nil {
		t.Fatal("SynthesizeRequest() error = nil, want unknown voice error")
	}
	if !strings.Contains(err.Error(), "unknown kokoro voice") {
		t.Fatalf("SynthesizeRequest() error = %q, want unknown kokoro voice message", err)
	}
}

func TestSynthesizeRequestRejectsSampleRateMismatch(t *testing.T) {
	k := newTestKokoro(nil)

	_, err := k.SynthesizeRequest(context.Background(), SynthesisRequest{Text: "hi", SampleRate: 8000})
	if err == nil {
		t.Fatal("SynthesizeRequest() error = nil, want resample error")
	}
	if !strings.Contains(err.Error(), "cannot resample") {
		t.Fatalf("SynthesizeRequest() error = %q, want cannot resample message", err)
	}
}

func TestSynthesizeRequestNilGeneration(t *testing.T) {
	k := newTestKokoro(func(text string, sid int, speed float32) *sherpa.GeneratedAudio {
		return nil
	})

	result, err := k.SynthesizeRequest(context.Background(), SynthesisRequest{Text: "hi"})
	if err != nil {
		t.Fatalf("SynthesizeRequest() error = %v", err)
	}
	for range result.Stream.Frames() {
	}
	if result.Stream.Err() == nil {
		t.Fatal("stream error = nil, want generation failure")
	}
}

func TestSynthesizeRequestCancelledContext(t *testing.T) {
	generated := false
	k := newTestKokoro(func(text string, sid int, speed float32) *sherpa.GeneratedAudio {
		generated = true
		return &sherpa.GeneratedAudio{SampleRate: 24000, Samples: []float32{0}}
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := k.SynthesizeRequest(ctx, SynthesisRequest{Text: "hi"})
	if err != nil {
		t.Fatalf("SynthesizeRequest() error = %v", err)
	}
	for range result.Stream.Frames() {
	}
	if !errors.Is(result.Stream.Err(), context.Canceled) {
		t.Fatalf("stream error = %v, want context.Canceled", result.Stream.Err())
	}
	if generated {
		t.Fatal("generate ran despite cancelled context")
	}
}

func TestSynthesizeRequestAppliesVoiceAndSpeed(t *testing.T) {
	tests := []struct {
		name      string
		req       SynthesisRequest
		wantSID   int
		wantSpeed float32
		wantVoice string
	}{
		{
			name:      "explicit voice and speed",
			req:       SynthesisRequest{Text: "hi", Voice: "am_adam", Speed: 1.5},
			wantSID:   voiceToSID["am_adam"],
			wantSpeed: 1.5,
			wantVoice: "am_adam",
		},
		{
			name:      "empty voice and speed fall back to defaults",
			req:       SynthesisRequest{Text: "hi"},
			wantSID:   voiceToSID["af_heart"],
			wantSpeed: 1.0,
			wantVoice: "af_heart",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got generateCall
			k := newTestKokoro(func(text string, sid int, speed float32) *sherpa.GeneratedAudio {
				got = generateCall{text: text, sid: sid, speed: speed}
				return &sherpa.GeneratedAudio{SampleRate: 24000, Samples: []float32{0.1, 0.2}}
			})

			result, err := k.SynthesizeRequest(context.Background(), tt.req)
			if err != nil {
				t.Fatalf("SynthesizeRequest() error = %v", err)
			}
			drainStream(t, result.Stream)

			if got.sid != tt.wantSID {
				t.Errorf("generate sid = %d, want %d", got.sid, tt.wantSID)
			}
			if got.speed != tt.wantSpeed {
				t.Errorf("generate speed = %v, want %v", got.speed, tt.wantSpeed)
			}
			if result.Provider != "kokoro" {
				t.Errorf("result.Provider = %q, want kokoro", result.Provider)
			}
			if result.Voice != tt.wantVoice {
				t.Errorf("result.Voice = %q, want %q", result.Voice, tt.wantVoice)
			}
			if result.SampleRate != 24000 {
				t.Errorf("result.SampleRate = %d, want 24000", result.SampleRate)
			}
		})
	}
}

func TestSynthesizeRequestPreparesTextAtKokoroBoundary(t *testing.T) {
	var got string
	k := newTestKokoro(func(text string, sid int, speed float32) *sherpa.GeneratedAudio {
		got = text
		return &sherpa.GeneratedAudio{SampleRate: 24000, Samples: []float32{0.1}}
	})

	result, err := k.SynthesizeRequest(context.Background(), SynthesisRequest{Text: "I had written the button label."})
	if err != nil {
		t.Fatalf("SynthesizeRequest() error = %v", err)
	}
	drainStream(t, result.Stream)

	// Syllabic-n is handled via tokens alias, not text hyphenation — natural
	// wording must reach Generate unchanged.
	if want := "I had written the button label."; got != want {
		t.Fatalf("generate text = %q, want %q", got, want)
	}
}

func TestSynthesizeMatchesRequestPath(t *testing.T) {
	var got generateCall
	k := newTestKokoro(func(text string, sid int, speed float32) *sherpa.GeneratedAudio {
		got = generateCall{text: text, sid: sid, speed: speed}
		return &sherpa.GeneratedAudio{SampleRate: 24000, Samples: []float32{0.1, 0.2, 0.3}}
	})

	stream, err := k.Synthesize(context.Background(), "hello there")
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}

	rate, err := stream.WaitReady(context.Background())
	if err != nil {
		t.Fatalf("WaitReady() error = %v", err)
	}
	if rate != 24000 {
		t.Errorf("sample rate = %d, want 24000", rate)
	}

	samples := drainStream(t, stream)
	if len(samples) != 3 {
		t.Errorf("got %d samples, want 3", len(samples))
	}
	if got.text != "hello there" {
		t.Errorf("generate text = %q, want %q", got.text, "hello there")
	}
	if got.sid != voiceToSID["af_heart"] {
		t.Errorf("generate sid = %d, want configured default %d", got.sid, voiceToSID["af_heart"])
	}
	if got.speed != 1.0 {
		t.Errorf("generate speed = %v, want configured default 1.0", got.speed)
	}
}
