package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/samantha/internal/config"
	"github.com/Obedience-Corp/samantha/internal/events"
)

var (
	benchmarkPrompts    []string
	benchmarkJSONOutput string
	benchmarkIterations int
)

var defaultBenchmarkPrompts = []string{
	"Explain in one sentence why fast feedback matters in software development.",
	"Give me two short sentences on how to review a pull request carefully.",
	"Summarize why barge-in matters for a voice assistant in two sentences.",
}

type benchmarkResult struct {
	Iteration int                 `json:"iteration"`
	Prompt    string              `json:"prompt"`
	Elapsed   time.Duration       `json:"elapsed"`
	Metrics   events.TurnMetrics  `json:"metrics"`
	Errors    []benchmarkErrorLog `json:"errors,omitempty"`
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

		bus := events.NewBus()
		p, cleanup, err := buildPipeline(ctx, cfg, bus, true, noVoice)
		if err != nil {
			return fmt.Errorf("init benchmark pipeline: %w", err)
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

				results = append(results, benchmarkResult{
					Iteration: iteration,
					Prompt:    prompt,
					Elapsed:   elapsed,
					Metrics:   metric,
					Errors:    errs,
				})
			}
		}

		printBenchmarkSummary(results)
		if benchmarkJSONOutput != "" {
			if err := writeBenchmarkJSON(benchmarkJSONOutput, results); err != nil {
				return err
			}
			fmt.Printf("\n  Wrote benchmark JSON to %s\n\n", benchmarkJSONOutput)
		}

		return nil
	},
}

func init() {
	benchmarkCmd.Flags().StringSliceVar(&benchmarkPrompts, "prompt", nil, "Benchmark prompt (repeatable)")
	benchmarkCmd.Flags().StringVar(&benchmarkJSONOutput, "json", "", "Write benchmark results to a JSON file")
	benchmarkCmd.Flags().IntVar(&benchmarkIterations, "iterations", 1, "Number of times to run each benchmark prompt")
	rootCmd.AddCommand(benchmarkCmd)
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

func printBenchmarkSummary(results []benchmarkResult) {
	fmt.Println()
	fmt.Println("  Samantha Benchmark")
	fmt.Println()

	for _, result := range results {
		fmt.Printf("  [%d] %s\n", result.Iteration, result.Prompt)
		fmt.Printf("    total: %s\n", result.Elapsed.Round(time.Millisecond))
		fmt.Printf("    first model chunk: %s\n", formatMetric(result.Metrics.FirstModelChunkElapsed))
		fmt.Printf("    model complete: %s\n", formatMetric(result.Metrics.ModelCompleteElapsed))
		fmt.Printf("    first segment: %s\n", formatMetric(result.Metrics.FirstSegmentElapsed))
		fmt.Printf("    first audio ready: %s\n", formatMetric(result.Metrics.FirstAudioReadyElapsed))
		fmt.Printf("    playback start: %s\n", formatMetric(result.Metrics.PlaybackStartElapsed))
		fmt.Printf("    playback complete: %s\n", formatMetric(result.Metrics.PlaybackCompleteElapsed))
		if len(result.Errors) > 0 {
			for _, err := range result.Errors {
				stage := err.Stage
				if stage == "" {
					stage = "runtime"
				}
				fmt.Printf("    error [%s]: %s\n", stage, err.Message)
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

func formatMetric(d time.Duration) string {
	if d <= 0 {
		return "n/a"
	}
	return d.Round(time.Millisecond).String()
}
