//go:build !integration

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/events"
	"github.com/lancekrogers/samantha/internal/stt"
)

var (
	benchmarkPrompts             []string
	benchmarkJSONOutput          string
	benchmarkIterations          int
	benchmarkAudioFixtures       []string
	benchmarkExpectedTranscripts []string
	benchmarkSTTProviders        []string
	benchmarkFixtureRealtime     bool
	benchmarkMaxTotal            time.Duration
	benchmarkMaxFirstModelChunk  time.Duration
	benchmarkMaxPlaybackStart    time.Duration
	benchmarkMaxPlaybackComplete time.Duration
	benchmarkMaxSTTFinal         time.Duration
	benchmarkMinTranscriptScore  float64
)

var defaultBenchmarkPrompts = []string{
	"Explain in one sentence why fast feedback matters in software development.",
	"Give me two short sentences on how to review a pull request carefully.",
	"Summarize why barge-in matters for a voice assistant in two sentences.",
}

type benchmarkResult struct {
	Mode            string              `json:"mode"`
	Provider        string              `json:"provider,omitempty"`
	Iteration       int                 `json:"iteration"`
	Prompt          string              `json:"prompt,omitempty"`
	Fixture         string              `json:"fixture,omitempty"`
	Elapsed         time.Duration       `json:"elapsed"`
	Metrics         events.TurnMetrics  `json:"metrics"`
	Transcript      string              `json:"transcript,omitempty"`
	Expected        string              `json:"expected,omitempty"`
	TranscriptScore float64             `json:"transcript_score,omitempty"`
	Errors          []benchmarkErrorLog `json:"errors,omitempty"`
	Violations      []string            `json:"violations,omitempty"`
}

type benchmarkErrorLog struct {
	Stage   string `json:"stage,omitempty"`
	Message string `json:"message"`
}

var benchmarkCmd = &cobra.Command{
	Use:   "benchmark",
	Short: "Run a local Samantha benchmark",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		var results []benchmarkResult
		if len(benchmarkAudioFixtures) > 0 {
			results, err = runSTTBenchmarks(ctx, cfg)
		} else {
			results, err = runTextBenchmarks(ctx, cfg)
		}
		if err != nil {
			return err
		}

		printBenchmarkSummary(results)
		if benchmarkJSONOutput != "" {
			if err := writeBenchmarkJSON(benchmarkJSONOutput, results); err != nil {
				return err
			}
			fmt.Printf("\n  Wrote benchmark JSON to %s\n\n", benchmarkJSONOutput)
		}

		failures := countViolations(results)
		if failures > 0 {
			return fmt.Errorf("benchmark failed: %d threshold violation(s)", failures)
		}
		return nil
	},
}

func init() {
	benchmarkCmd.Flags().StringSliceVar(&benchmarkPrompts, "prompt", nil, "Benchmark prompt (repeatable)")
	benchmarkCmd.Flags().StringVar(&benchmarkJSONOutput, "json", "", "Write benchmark results to a JSON file")
	benchmarkCmd.Flags().IntVar(&benchmarkIterations, "iterations", 1, "Number of times to run each benchmark prompt")
	benchmarkCmd.Flags().StringSliceVar(&benchmarkAudioFixtures, "audio-fixture", nil, "WAV fixture for STT benchmarking (repeatable)")
	benchmarkCmd.Flags().StringSliceVar(&benchmarkExpectedTranscripts, "expect-text", nil, "Expected transcript for each fixture (repeatable)")
	benchmarkCmd.Flags().StringSliceVar(&benchmarkSTTProviders, "stt-provider", nil, "STT provider(s) to benchmark in fixture mode (repeatable)")
	benchmarkCmd.Flags().BoolVar(&benchmarkFixtureRealtime, "fixture-realtime", true, "Replay fixture audio in real time for latency measurements")
	benchmarkCmd.Flags().DurationVar(&benchmarkMaxTotal, "max-total", 0, "Fail when total benchmark time exceeds this duration")
	benchmarkCmd.Flags().DurationVar(&benchmarkMaxFirstModelChunk, "max-first-model-chunk", 0, "Fail when first model chunk exceeds this duration")
	benchmarkCmd.Flags().DurationVar(&benchmarkMaxPlaybackStart, "max-playback-start", 0, "Fail when playback start exceeds this duration")
	benchmarkCmd.Flags().DurationVar(&benchmarkMaxPlaybackComplete, "max-playback-complete", 0, "Fail when playback completion exceeds this duration")
	benchmarkCmd.Flags().DurationVar(&benchmarkMaxSTTFinal, "max-stt-final", 0, "Fail when STT finalization exceeds this duration")
	benchmarkCmd.Flags().Float64Var(&benchmarkMinTranscriptScore, "min-transcript-score", 0, "Fail when transcript score falls below this value (0-1)")
	rootCmd.AddCommand(benchmarkCmd)
}

func runTextBenchmarks(ctx context.Context, cfg *config.Config) ([]benchmarkResult, error) {
	bus := events.NewBus()
	p, cleanup, err := buildPipeline(ctx, cfg, bus, true, noVoice)
	if err != nil {
		return nil, fmt.Errorf("init benchmark pipeline: %w", err)
	}
	defer cleanup()

	metricsCh := make(chan events.TurnMetrics, 64)
	errorCh := make(chan events.Error, 64)
	events.Subscribe(bus, func(e events.TurnMetrics) {
		select {
		case metricsCh <- e:
		default:
		}
	})
	events.Subscribe(bus, func(e events.Error) {
		select {
		case errorCh <- e:
		default:
		}
	})

	prompts := benchmarkPrompts
	if len(prompts) == 0 {
		prompts = defaultBenchmarkPrompts
	}
	if benchmarkIterations < 1 {
		benchmarkIterations = 1
	}

	var results []benchmarkResult
	for iteration := 1; iteration <= benchmarkIterations; iteration++ {
		for _, prompt := range prompts {
			drainBenchmarkChannels(metricsCh, errorCh)
			p.Brain.ClearHistory()

			start := time.Now()
			runErr := p.RunTurnTextMode(ctx, prompt)
			elapsed := time.Since(start)
			metric := readBenchmarkMetrics(metricsCh)
			errs := readBenchmarkErrors(errorCh)

			if runErr != nil {
				errs = append(errs, benchmarkErrorLog{Stage: "benchmark", Message: runErr.Error()})
			}

			result := benchmarkResult{
				Mode:      "text",
				Iteration: iteration,
				Prompt:    prompt,
				Elapsed:   elapsed,
				Metrics:   metric,
				Errors:    errs,
			}
			result.Violations = evaluateTextThresholds(result)
			results = append(results, result)
		}
	}

	return results, nil
}

func runSTTBenchmarks(ctx context.Context, cfg *config.Config) ([]benchmarkResult, error) {
	if len(benchmarkExpectedTranscripts) > 0 && len(benchmarkExpectedTranscripts) != len(benchmarkAudioFixtures) {
		return nil, fmt.Errorf("--expect-text count (%d) must match --audio-fixture count (%d)", len(benchmarkExpectedTranscripts), len(benchmarkAudioFixtures))
	}

	providers := benchmarkSTTProviders
	if len(providers) == 0 {
		active := strings.TrimSpace(cfg.STTProvider)
		if active == "" {
			active = "sherpa"
		}
		providers = []string{active}
	}
	if benchmarkIterations < 1 {
		benchmarkIterations = 1
	}

	var results []benchmarkResult
	for _, providerName := range providers {
		for iteration := 1; iteration <= benchmarkIterations; iteration++ {
			for idx, fixture := range benchmarkAudioFixtures {
				expected := ""
				if idx < len(benchmarkExpectedTranscripts) {
					expected = benchmarkExpectedTranscripts[idx]
				}

				result, err := runSingleSTTBenchmark(ctx, cfg, providerName, fixture, expected, iteration)
				if err != nil {
					result.Errors = append(result.Errors, benchmarkErrorLog{Stage: "benchmark", Message: err.Error()})
				}
				result.Violations = append(result.Violations, evaluateSTTThresholds(result)...)
				results = append(results, result)
			}
		}
	}

	return results, nil
}

func runSingleSTTBenchmark(ctx context.Context, cfg *config.Config, providerName, fixture, expected string, iteration int) (benchmarkResult, error) {
	result := benchmarkResult{
		Mode:      "stt",
		Provider:  providerName,
		Iteration: iteration,
		Fixture:   fixture,
		Expected:  expected,
	}

	cfgCopy := *cfg
	cfgCopy.STTProvider = providerName

	if err := config.EnsureRuntimeAssets(&cfgCopy, config.AssetRequest{NeedSTT: true, NeedVAD: true}, nil); err != nil {
		return result, err
	}

	source, err := audio.NewFixtureSourceFromWAV(fixture, audio.ChunkSize, benchmarkFixtureRealtime)
	if err != nil {
		return result, err
	}

	vad, err := audio.NewVAD(&cfgCopy)
	if err != nil {
		return result, err
	}
	defer vad.Delete()

	provider, cleanup, err := stt.NewProvider(&cfgCopy, source, vad)
	if err != nil {
		return result, err
	}
	if cleanup != nil {
		defer cleanup()
	}

	start := time.Now()
	session, err := provider.Start(ctx)
	if err != nil {
		return result, err
	}
	defer session.Close()

	var firstPartial time.Time
	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case event, ok := <-session.Events():
			if !ok {
				result.Elapsed = time.Since(start)
				if expected != "" {
					result.TranscriptScore = transcriptScore(expected, result.Transcript)
				}
				return result, nil
			}

			switch e := event.(type) {
			case stt.PartialTranscript:
				if firstPartial.IsZero() {
					firstPartial = time.Now()
				}
				result.Transcript = e.Text
			case stt.FinalTranscript:
				now := time.Now()
				if firstPartial.IsZero() {
					firstPartial = now
				}
				result.Transcript = e.Text
				result.Elapsed = now.Sub(start)
				result.Metrics.STTFinalElapsed = result.Elapsed
				if !firstPartial.IsZero() {
					result.Metrics.FirstModelChunkElapsed = firstPartial.Sub(start)
				}
				if expected != "" {
					result.TranscriptScore = transcriptScore(expected, result.Transcript)
				}
				return result, nil
			case stt.Timeout:
				result.Elapsed = time.Since(start)
				return result, errors.New("stt timed out")
			case stt.Failure:
				result.Elapsed = time.Since(start)
				return result, e.Err
			}
		}
	}
}

func drainBenchmarkChannels(metricsCh chan events.TurnMetrics, errorCh chan events.Error) {
	for {
		select {
		case <-metricsCh:
		case <-errorCh:
		default:
			return
		}
	}
}

func readBenchmarkMetrics(metricsCh chan events.TurnMetrics) events.TurnMetrics {
	select {
	case metric := <-metricsCh:
		return metric
	default:
		return events.TurnMetrics{}
	}
}

func readBenchmarkErrors(errorCh chan events.Error) []benchmarkErrorLog {
	var out []benchmarkErrorLog
	for {
		select {
		case err := <-errorCh:
			out = append(out, benchmarkErrorLog{
				Stage:   err.Stage,
				Message: err.Message,
			})
		default:
			return out
		}
	}
}

func evaluateTextThresholds(result benchmarkResult) []string {
	var violations []string

	if benchmarkMaxTotal > 0 && result.Elapsed > benchmarkMaxTotal {
		violations = append(violations, fmt.Sprintf("total %s exceeded %s", result.Elapsed.Round(time.Millisecond), benchmarkMaxTotal))
	}
	if benchmarkMaxFirstModelChunk > 0 && (result.Metrics.FirstModelChunkElapsed <= 0 || result.Metrics.FirstModelChunkElapsed > benchmarkMaxFirstModelChunk) {
		violations = append(violations, fmt.Sprintf("first model chunk %s exceeded %s", formatMetric(result.Metrics.FirstModelChunkElapsed), benchmarkMaxFirstModelChunk))
	}
	if benchmarkMaxPlaybackStart > 0 && (result.Metrics.PlaybackStartElapsed <= 0 || result.Metrics.PlaybackStartElapsed > benchmarkMaxPlaybackStart) {
		violations = append(violations, fmt.Sprintf("playback start %s exceeded %s", formatMetric(result.Metrics.PlaybackStartElapsed), benchmarkMaxPlaybackStart))
	}
	if benchmarkMaxPlaybackComplete > 0 && (result.Metrics.PlaybackCompleteElapsed <= 0 || result.Metrics.PlaybackCompleteElapsed > benchmarkMaxPlaybackComplete) {
		violations = append(violations, fmt.Sprintf("playback complete %s exceeded %s", formatMetric(result.Metrics.PlaybackCompleteElapsed), benchmarkMaxPlaybackComplete))
	}
	return violations
}

func evaluateSTTThresholds(result benchmarkResult) []string {
	var violations []string

	if benchmarkMaxSTTFinal > 0 && (result.Metrics.STTFinalElapsed <= 0 || result.Metrics.STTFinalElapsed > benchmarkMaxSTTFinal) {
		violations = append(violations, fmt.Sprintf("stt final %s exceeded %s", formatMetric(result.Metrics.STTFinalElapsed), benchmarkMaxSTTFinal))
	}
	if benchmarkMinTranscriptScore > 0 && result.TranscriptScore < benchmarkMinTranscriptScore {
		violations = append(violations, fmt.Sprintf("transcript score %.2f below %.2f", result.TranscriptScore, benchmarkMinTranscriptScore))
	}
	return violations
}

func countViolations(results []benchmarkResult) int {
	total := 0
	for _, result := range results {
		total += len(result.Violations)
	}
	return total
}

func printBenchmarkSummary(results []benchmarkResult) {
	fmt.Println()
	fmt.Println("  Samantha Benchmark")
	fmt.Println()

	for _, result := range results {
		switch result.Mode {
		case "stt":
			fmt.Printf("  [%d] %s :: %s\n", result.Iteration, result.Provider, filepath.Base(result.Fixture))
			fmt.Printf("    stt final: %s\n", formatMetric(result.Metrics.STTFinalElapsed))
			if result.Expected != "" {
				fmt.Printf("    transcript score: %.2f\n", result.TranscriptScore)
			}
			if result.Transcript != "" {
				fmt.Printf("    transcript: %s\n", result.Transcript)
			}
		default:
			fmt.Printf("  [%d] %s\n", result.Iteration, result.Prompt)
			fmt.Printf("    total: %s\n", result.Elapsed.Round(time.Millisecond))
			fmt.Printf("    first model chunk: %s\n", formatMetric(result.Metrics.FirstModelChunkElapsed))
			fmt.Printf("    model complete: %s\n", formatMetric(result.Metrics.ModelCompleteElapsed))
			fmt.Printf("    first segment: %s\n", formatMetric(result.Metrics.FirstSegmentElapsed))
			fmt.Printf("    first audio ready: %s\n", formatMetric(result.Metrics.FirstAudioReadyElapsed))
			fmt.Printf("    playback start: %s\n", formatMetric(result.Metrics.PlaybackStartElapsed))
			fmt.Printf("    playback complete: %s\n", formatMetric(result.Metrics.PlaybackCompleteElapsed))
			if result.Metrics.Interrupted || result.Metrics.BargeInElapsed > 0 {
				fmt.Printf("    interruption: %s\n", formatMetric(result.Metrics.BargeInElapsed))
			}
		}

		if len(result.Errors) > 0 {
			for _, err := range result.Errors {
				stage := err.Stage
				if stage == "" {
					stage = "runtime"
				}
				fmt.Printf("    error [%s]: %s\n", stage, err.Message)
			}
		}
		if len(result.Violations) > 0 {
			for _, violation := range result.Violations {
				fmt.Printf("    violation: %s\n", violation)
			}
		}
		fmt.Println()
	}
}

func writeBenchmarkJSON(path string, results []benchmarkResult) error {
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal benchmark results: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write benchmark results: %w", err)
	}
	return nil
}

func transcriptScore(expected, actual string) float64 {
	exp := tokenizeTranscript(expected)
	act := tokenizeTranscript(actual)
	if len(exp) == 0 {
		if len(act) == 0 {
			return 1
		}
		return 0
	}

	expCounts := make(map[string]int, len(exp))
	for _, token := range exp {
		expCounts[token]++
	}

	matches := 0
	for _, token := range act {
		if expCounts[token] > 0 {
			matches++
			expCounts[token]--
		}
	}

	precision := float64(matches) / float64(max(len(act), 1))
	recall := float64(matches) / float64(len(exp))
	if precision+recall == 0 {
		return 0
	}
	return 2 * precision * recall / (precision + recall)
}

func tokenizeTranscript(text string) []string {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return nil
	}

	clean := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) {
			return r
		}
		return ' '
	}, text)
	return strings.Fields(clean)
}

func formatMetric(d time.Duration) string {
	if d <= 0 {
		return "n/a"
	}
	return d.Round(time.Millisecond).String()
}
