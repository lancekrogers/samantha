package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// toolLeakRE detects tool-shaped syntax inside spoken/chat agent text — the
// B4 leakage signal. Matches transcript narration ("Called the X tool"),
// bare tool-call JSON, and fenced code blocks.
var toolLeakRE = []*regexp.Regexp{
	regexp.MustCompile(`(?i)called the \S+ tool`),
	regexp.MustCompile(`(?i)result of calling the`),
	regexp.MustCompile(`\{\s*"(command|tool|name|input)"\s*:`),
	regexp.MustCompile("```"),
}

// InstanceMetrics is the per-instance rollup written to metrics.json.
type InstanceMetrics struct {
	Turns         int `json:"turns"`
	DegradedTurns int `json:"degraded_turns"`
	Errors        int `json:"errors"`
	ToolCalls     int `json:"tool_calls"`
	ToolLeakLines int `json:"tool_leak_lines"`
	// VoiceGateStrips is samantha's own count of lines its voice gate kept out
	// of TTS (voice_gate records). ToolLeakLines stays this harness's
	// independent detector over chat text — the two must be measured
	// separately so a broken gate cannot hide its own leaks.
	VoiceGateStrips int     `json:"voice_gate_strips"`
	MeanModelS      float64 `json:"mean_model_s"`
	P95ModelS       float64 `json:"p95_model_s"`
	BargeInS        float64 `json:"barge_in_s"` // max observed; 0 = none
	Dropped         int64   `json:"dropped"`
}

// RunMetrics is the whole run's rollup; report.md is its rendering.
type RunMetrics struct {
	Scenario           string                      `json:"scenario"`
	StopReason         string                      `json:"stop_reason"`
	ExchangesCompleted int                         `json:"exchanges_completed"`
	LoopDetected       bool                        `json:"loop_detected"`
	Instances          map[string]*InstanceMetrics `json:"instances"`
}

func computeMetrics(e *engine, stopReason string) *RunMetrics {
	rm := &RunMetrics{
		Scenario:   e.scn.Name,
		StopReason: stopReason,
		Instances:  map[string]*InstanceMetrics{},
	}
	for _, d := range e.directions {
		rm.ExchangesCompleted += d.forwarded
		if d.looped {
			rm.LoopDetected = true
		}
	}
	for id, recs := range e.records {
		im := &InstanceMetrics{}
		var modelTimes []float64
		for _, r := range recs {
			switch r.Type {
			case "agent":
				im.Turns++
				if r.Degraded {
					im.DegradedTurns++
				}
				im.ToolLeakLines += leakLines(r.Text)
			case "error":
				im.Errors++
			case "tool_call":
				im.ToolCalls++
			case "voice_gate":
				im.VoiceGateStrips += r.LeakLines
			case "metrics":
				if r.ModelS > 0 {
					modelTimes = append(modelTimes, r.ModelS)
				}
				if r.BargeInS > im.BargeInS {
					im.BargeInS = r.BargeInS
				}
			case "dropped":
				im.Dropped += r.Dropped
			}
		}
		if len(modelTimes) > 0 {
			sort.Float64s(modelTimes)
			var sum float64
			for _, v := range modelTimes {
				sum += v
			}
			im.MeanModelS = sum / float64(len(modelTimes))
			im.P95ModelS = modelTimes[min(len(modelTimes)*95/100, len(modelTimes)-1)]
		}
		rm.Instances[id] = im
	}
	return rm
}

func leakLines(text string) int {
	n := 0
	for _, line := range strings.Split(text, "\n") {
		for _, re := range toolLeakRE {
			if re.MatchString(line) {
				n++
				break
			}
		}
	}
	return n
}

// writeArtifacts renders metrics.json and report.md into the run dir.
func writeArtifacts(runDir string, e *engine, rm *RunMetrics, started time.Time) error {
	data, err := json.MarshalIndent(rm, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(runDir, "metrics.json"), data, 0o644); err != nil {
		return err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# duet run — %s\n\n", rm.Scenario)
	fmt.Fprintf(&b, "- started: %s · wall: %s · stop: **%s**\n", started.UTC().Format(time.RFC3339), time.Since(started).Round(time.Second), rm.StopReason)
	fmt.Fprintf(&b, "- exchanges completed: %d", rm.ExchangesCompleted)
	if rm.LoopDetected {
		b.WriteString(" · **loop detected**")
	}
	b.WriteString("\n\n")

	ids := make([]string, 0, len(rm.Instances))
	for id := range rm.Instances {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	b.WriteString("| instance | turns | degraded | errors | tool calls | leak lines | voice strips | model mean/p95 (s) | dropped |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|\n")
	for _, id := range ids {
		im := rm.Instances[id]
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %d | %.1f / %.1f | %d |\n",
			id, im.Turns, im.DegradedTurns, im.Errors, im.ToolCalls, im.ToolLeakLines, im.VoiceGateStrips, im.MeanModelS, im.P95ModelS, im.Dropped)
	}

	for _, id := range ids {
		fmt.Fprintf(&b, "\n## %s\n\n", id)
		errs, turns := 0, 0
		for _, r := range e.records[id] {
			switch r.Type {
			case "error":
				errs++
				fmt.Fprintf(&b, "- ⚠ error [%s] %s\n", r.Stage, r.Message)
			case "user":
				turns++
				fmt.Fprintf(&b, "- › %s\n", firstLine(r.Text, 100))
			case "agent":
				mark := "●"
				if r.Degraded {
					mark = "● (degraded)"
				}
				fmt.Fprintf(&b, "- %s %s\n", mark, firstLine(r.Text, 100))
			}
		}
		if errs == 0 && turns == 0 {
			b.WriteString("- (no conversation records)\n")
		}
	}
	b.WriteString("\nArtifacts: per-instance `transcript.jsonl`, `native-diagnostics.log`, `pane.txt`, `audio/`; run-level `timeline.jsonl`, `metrics.json`, `scenario.yaml`.\n")
	return os.WriteFile(filepath.Join(runDir, "report.md"), []byte(b.String()), 0o644)
}

func firstLine(s string, n int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// evaluate checks scenario expectations against the metrics; failures list
// what broke and where to look.
func evaluate(rm *RunMetrics, expects []Expectation) []string {
	var failures []string
	for _, ex := range expects {
		val, ok := metricValue(rm, ex)
		if !ok {
			failures = append(failures, fmt.Sprintf("expect %s %s %v: unknown metric (instance %q)", ex.Metric, ex.Op, ex.Value, ex.Instance))
			continue
		}
		if !compare(val, ex.Op, ex.Value) {
			scope := "run"
			if ex.Instance != "" {
				scope = ex.Instance
			}
			failures = append(failures, fmt.Sprintf("expect [%s] %s %s %v — got %v", scope, ex.Metric, ex.Op, ex.Value, val))
		}
	}
	return failures
}

func metricValue(rm *RunMetrics, ex Expectation) (float64, bool) {
	if ex.Instance == "" {
		switch ex.Metric {
		case "exchanges_completed":
			return float64(rm.ExchangesCompleted), true
		case "loop_detected":
			return boolMetric(rm.LoopDetected), true
		}
		return 0, false
	}
	im, ok := rm.Instances[ex.Instance]
	if !ok {
		return 0, false
	}
	switch ex.Metric {
	case "turns":
		return float64(im.Turns), true
	case "degraded_turns":
		return float64(im.DegradedTurns), true
	case "errors":
		return float64(im.Errors), true
	case "tool_calls":
		return float64(im.ToolCalls), true
	case "tool_leak_lines":
		return float64(im.ToolLeakLines), true
	case "voice_gate_strips":
		return float64(im.VoiceGateStrips), true
	case "mean_model_s":
		return im.MeanModelS, true
	case "p95_model_s":
		return im.P95ModelS, true
	case "barge_in_s":
		return im.BargeInS, true
	case "dropped":
		return float64(im.Dropped), true
	}
	return 0, false
}

func boolMetric(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func compare(got float64, op string, want float64) bool {
	switch op {
	case "==":
		return got == want
	case "!=":
		return got != want
	case ">=":
		return got >= want
	case "<=":
		return got <= want
	case ">":
		return got > want
	case "<":
		return got < want
	}
	return false
}
