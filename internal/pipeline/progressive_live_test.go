//go:build integration

package pipeline

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/events"
	managedqwen "github.com/lancekrogers/samantha/internal/qwen"
	"github.com/lancekrogers/samantha/internal/tts"
)

// TestLiveProgressiveNativeConversation is opt-in conversation smoke:
// multi-sentence stream → progressive segments on the native worker.
//
//	SAMANTHA_NATIVE_QWEN_LIVE=1 go test -tags=integration ./internal/pipeline/ -run LiveProgressive -count=1 -v
//
// Requires a native package under models_dir/qwen3-tts (ensure tarball) or
// QWEN_WORKER + QWEN_MODELS pointing at a lab install.
func TestLiveProgressiveNativeConversation(t *testing.T) {
	if os.Getenv("SAMANTHA_NATIVE_QWEN_LIVE") != "1" {
		t.Skip("set SAMANTHA_NATIVE_QWEN_LIVE=1 with native package installed")
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.TTSProvider = managedqwen.ProviderName
	cfg.QwenTTSBinary = ""
	cfg.QwenTTSModel = ""
	if cfg.QwenTTSTimeout < 180 {
		cfg.QwenTTSTimeout = 180
	}

	// Prefer explicit lab paths when set.
	if w := os.Getenv("QWEN_WORKER"); w != "" {
		cfg.QwenTTSBinary = w
	}
	if m := os.Getenv("QWEN_MODELS"); m != "" {
		cfg.QwenTTSModel = m
	}

	provider, cleanup, err := tts.NewProvider(cfg)
	if err != nil {
		t.Fatalf("NewProvider: %v (install native package or set QWEN_WORKER/QWEN_MODELS)", err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	player := newFakePlayer(10 * time.Millisecond)
	defer player.Close()

	var metrics events.TurnMetrics
	bus := events.NewBus()
	events.Subscribe(bus, func(m events.TurnMetrics) { metrics = m })

	p := &Pipeline{TTS: provider, Player: player, Events: bus}
	tm := newTurnMetrics()
	turn := p.newTurnConductor(tm)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Multi-sentence scripted "brain" stream — no live LLM required.
	stream := scriptedStream(ctx, []string{
		"Hello from native progressive smoke. ",
		"This is the second sentence. ",
		"And the third ends the reply.",
	}, nil)

	response, _, err := p.streamResponse(ctx, cancel, stream, false, tm, turn)
	if err != nil {
		t.Fatalf("streamResponse: %v", err)
	}
	if !strings.Contains(response, "second sentence") {
		t.Fatalf("response incomplete: %q", response)
	}
	turn.finish(TurnCompleted)

	if metrics.FirstSegmentElapsed <= 0 || metrics.ModelCompleteElapsed <= 0 {
		t.Fatalf("metrics incomplete: %+v", metrics)
	}
	t.Logf("live progressive: FirstSegment=%v ModelComplete=%v FirstAudio=%v",
		metrics.FirstSegmentElapsed, metrics.ModelCompleteElapsed, metrics.FirstAudioReadyElapsed)
}
