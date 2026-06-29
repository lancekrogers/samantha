package cmd

import (
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/events"
)

func TestEvaluateTextThresholds(t *testing.T) {
	benchmarkMaxTotal = 2 * time.Second
	benchmarkMaxFirstModelChunk = 500 * time.Millisecond
	benchmarkMaxPlaybackStart = 800 * time.Millisecond
	benchmarkMaxPlaybackComplete = 1500 * time.Millisecond
	defer func() {
		benchmarkMaxTotal = 0
		benchmarkMaxFirstModelChunk = 0
		benchmarkMaxPlaybackStart = 0
		benchmarkMaxPlaybackComplete = 0
	}()

	result := benchmarkResult{
		Elapsed: 3 * time.Second,
		Metrics: events.TurnMetrics{
			FirstModelChunkElapsed:  600 * time.Millisecond,
			PlaybackStartElapsed:    900 * time.Millisecond,
			PlaybackCompleteElapsed: 2 * time.Second,
		},
	}

	violations := evaluateTextThresholds(result)
	if len(violations) != 4 {
		t.Fatalf("len(violations) = %d, want 4", len(violations))
	}
}

func TestEvaluateSTTThresholds(t *testing.T) {
	benchmarkMaxSTTFinal = 700 * time.Millisecond
	benchmarkMinTranscriptScore = 0.8
	defer func() {
		benchmarkMaxSTTFinal = 0
		benchmarkMinTranscriptScore = 0
	}()

	result := benchmarkResult{
		Metrics:         events.TurnMetrics{STTFinalElapsed: 950 * time.Millisecond},
		TranscriptScore: 0.5,
	}

	violations := evaluateSTTThresholds(result)
	if len(violations) != 2 {
		t.Fatalf("len(violations) = %d, want 2", len(violations))
	}
}

func TestTranscriptScore(t *testing.T) {
	score := transcriptScore("hello samantha", "hello there samantha")
	if score <= 0.7 {
		t.Fatalf("transcriptScore() = %.2f, want > 0.7", score)
	}
}
