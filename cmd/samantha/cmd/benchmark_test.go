//go:build !integration

package cmd

import (
	"bytes"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/events"
)

// ansiCSI matches lipgloss/termenv SGR sequences so assertions can treat
// colored CLI output as plain text. CI often enables color (COLORTERM /
// FORCE_COLOR) even when stdout is a pipe, which splits "label: value"
// across reset codes and breaks contiguous substring checks.
var ansiCSI = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiCSI.ReplaceAllString(s, "") }

func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	os.Stdout = w
	f()
	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	return stripANSI(buf.String())
}

func TestBenchmarkExitErr(t *testing.T) {
	tests := []struct {
		name        string
		results     []benchmarkResult
		wantErr     bool
		wantContain []string
	}{
		{
			name: "hard errors fail",
			results: []benchmarkResult{
				{Errors: []benchmarkErrorLog{{Stage: "benchmark", Message: "fixture not found"}}},
			},
			wantErr:     true,
			wantContain: []string{"1 run(s) with errors"},
		},
		{
			name: "violations fail",
			results: []benchmarkResult{
				{Violations: []string{"total 3s exceeded 2s"}},
			},
			wantErr:     true,
			wantContain: []string{"1 threshold violation(s)"},
		},
		{
			name: "errors and violations both reported",
			results: []benchmarkResult{
				{Errors: []benchmarkErrorLog{{Message: "boom"}}, Violations: []string{"too slow"}},
				{Errors: []benchmarkErrorLog{{Message: "bang"}, {Message: "crash"}}},
			},
			wantErr:     true,
			wantContain: []string{"2 run(s) with errors", "1 threshold violation(s)"},
		},
		{
			name:    "clean results pass",
			results: []benchmarkResult{{Mode: "text"}, {Mode: "stt"}},
		},
		{
			name: "no results pass",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := benchmarkExitErr(tt.results)
			if (err != nil) != tt.wantErr {
				t.Fatalf("benchmarkExitErr() = %v, wantErr %v", err, tt.wantErr)
			}
			for _, want := range tt.wantContain {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("benchmarkExitErr() = %q, want substring %q", err.Error(), want)
				}
			}
		})
	}
}

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

func TestEvaluateThresholdsWithinBudgetNoViolations(t *testing.T) {
	benchmarkMaxTotal = 2 * time.Second
	benchmarkMaxFirstModelChunk = 500 * time.Millisecond
	benchmarkMaxPlaybackStart = 800 * time.Millisecond
	benchmarkMaxPlaybackComplete = 1500 * time.Millisecond
	benchmarkMaxSTTFinal = 700 * time.Millisecond
	benchmarkMinTranscriptScore = 0.8
	defer func() {
		benchmarkMaxTotal = 0
		benchmarkMaxFirstModelChunk = 0
		benchmarkMaxPlaybackStart = 0
		benchmarkMaxPlaybackComplete = 0
		benchmarkMaxSTTFinal = 0
		benchmarkMinTranscriptScore = 0
	}()

	text := benchmarkResult{
		Elapsed: time.Second,
		Metrics: events.TurnMetrics{
			FirstModelChunkElapsed:  300 * time.Millisecond,
			PlaybackStartElapsed:    600 * time.Millisecond,
			PlaybackCompleteElapsed: 1200 * time.Millisecond,
		},
	}
	if v := evaluateTextThresholds(text); len(v) != 0 {
		t.Fatalf("within-budget text violations = %v, want none", v)
	}

	stt := benchmarkResult{
		Metrics:         events.TurnMetrics{STTFinalElapsed: 500 * time.Millisecond},
		TranscriptScore: 0.95,
	}
	if v := evaluateSTTThresholds(stt); len(v) != 0 {
		t.Fatalf("within-budget stt violations = %v, want none", v)
	}
}

func TestBenchmarkSummaryShowsInterruptionLatency(t *testing.T) {
	results := []benchmarkResult{{
		Mode:      "voice",
		Iteration: 1,
		Prompt:    "hi",
		Metrics: events.TurnMetrics{
			Interrupted:    true,
			BargeInElapsed: 250 * time.Millisecond,
		},
	}}
	out := captureStdout(t, func() { printBenchmarkSummary(results) })
	if !strings.Contains(out, "interruption: 250ms") {
		t.Fatalf("benchmark summary missing interruption latency:\n%s", out)
	}
}

func TestBenchmarkSummaryOmitsInterruptionWhenNone(t *testing.T) {
	results := []benchmarkResult{{
		Mode:      "voice",
		Iteration: 1,
		Prompt:    "hi",
		Metrics:   events.TurnMetrics{FirstModelChunkElapsed: 100 * time.Millisecond},
	}}
	out := captureStdout(t, func() { printBenchmarkSummary(results) })
	if strings.Contains(out, "interruption:") {
		t.Fatalf("summary should omit interruption for a non-interrupted turn:\n%s", out)
	}
}

func TestTranscriptScore(t *testing.T) {
	score := transcriptScore("hello samantha", "hello there samantha")
	if score <= 0.7 {
		t.Fatalf("transcriptScore() = %.2f, want > 0.7", score)
	}
}
