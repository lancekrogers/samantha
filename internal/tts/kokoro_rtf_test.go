//go:build integration

package tts

import (
	"context"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/config"
)

// TestKokoroRealtimeFactor measures how much faster than realtime Kokoro
// synthesizes, which decides whether the pipeline can batch sentences.
//
// The pipeline runs one ordered synthesis worker, so segment N+1 is synthesized
// while segment N plays. Batching two sentences into one call is only free if
//
//	synth(2 sentences) < playback(1 sentence)
//
// which needs a realtime factor above roughly 2.3x. Measured on an M4 Max in
// 2026-08 it was **1.32x**, so batching opened an 804 ms silence immediately
// after the opening sentence — see festival decision D009, which set
// laterBatchSegments back to 1 because of this number.
//
// Re-run this after any TTS change (phase 009's bake-off especially): a
// synthesiser above ~2.3x makes cross-sentence batching viable, and this is the
// measurement that says so.
//
//	go test -tags integration ./internal/tts/ -run TestKokoroRealtimeFactor -v
func TestKokoroRealtimeFactor(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("no config: %v", err)
	}
	cfg.TTSProvider = "kokoro"
	cfg.TTSVoice = "af_heart"
	cfg.SpeechSpeed = 0.95

	k, err := NewKokoroTTS(cfg)
	if err != nil {
		t.Skipf("kokoro unavailable: %v", err)
	}
	defer k.Delete()

	// Warm up so model load is not charged to the first case.
	if s, err := k.Synthesize(context.Background(), "warm up"); err == nil {
		for range s.Frames() {
		}
	}

	cases := []struct{ name, text string }{
		{"one sentence", "It continues here."},
		{"two sentences", "It continues here. Then a third thought arrives."},
	}

	results := make(map[string]struct{ synth, audio float64 })
	for _, c := range cases {
		const runs = 5
		var total time.Duration
		var audio float64
		for range runs {
			start := time.Now()
			stream, err := k.Synthesize(context.Background(), c.text)
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			rate, err := stream.WaitReady(context.Background())
			if err != nil {
				t.Fatalf("%s: WaitReady: %v", c.name, err)
			}
			samples := 0
			for f := range stream.Frames() {
				samples += len(f)
			}
			total += time.Since(start)
			if rate > 0 {
				audio = float64(samples) / float64(rate)
			}
		}
		synth := total.Seconds() / runs
		results[c.name] = struct{ synth, audio float64 }{synth, audio}
		t.Logf("%-14s synth %6.1f ms   audio %5.3f s   realtime factor %.2fx",
			c.name, synth*1000, audio, audio/synth)
	}

	one, two := results["one sentence"], results["two sentences"]
	gap := two.synth - one.audio
	t.Logf("batching headroom: synth(2 sentences) %.3f s vs playback(1 sentence) %.3f s -> %+.3f s",
		two.synth, one.audio, gap)
	if gap > 0 {
		t.Logf("BATCHING WOULD STALL: %.0f ms of silence after the opening sentence. "+
			"This is why laterBatchSegments is 1 (D009).", gap*1000)
	} else {
		t.Logf("Batching is viable on this machine: %.0f ms of headroom. Revisit D009.", -gap*1000)
	}
}
