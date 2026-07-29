package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeScenario(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const validScenario = `
name: demo
duration: 60s
defaults:
  flags: ["-t", "-n"]
  brain: { provider: ollama }
instances:
  a:
    persona: { display_name: "Agent A" }
    brain: { model: m1 }
  b:
    persona: { display_name: "Agent B" }
    brain: { model: m2 }
triggers:
  - when: { at: 2s }
    target: a
    action: { type: keys, text: "hello", submit: true }
bridge:
  mode: text
  pairs: [[a, b]]
  max_exchanges: 3
expect:
  - { instance: a, metric: degraded_turns, op: "==", value: 0 }
`

func TestLoadScenarioValidAndDefaults(t *testing.T) {
	s, err := LoadScenario(writeScenario(t, validScenario))
	if err != nil {
		t.Fatalf("LoadScenario: %v", err)
	}
	a := s.Instances["a"]
	if a.Brain.Provider != "ollama" || a.Brain.Model != "m1" {
		t.Fatalf("defaults not applied: %+v", a.Brain)
	}
	if len(a.Flags) != 2 {
		t.Fatalf("flags default not applied: %v", a.Flags)
	}
	if s.Bridge.DegradedPolicy != "continue" {
		t.Fatalf("degraded_policy default = %q", s.Bridge.DegradedPolicy)
	}
}

func TestLoadScenarioRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		edit func(string) string
	}{
		{"unknown key", func(s string) string { return s + "\nbogus_key: 1\n" }},
		{"one instance", func(s string) string {
			return strings.Replace(s, "  b:\n    persona: { display_name: \"Agent B\" }\n    brain: { model: m2 }\n", "", 1)
		}},
		{"trigger without condition", func(s string) string {
			return strings.Replace(s, "when: { at: 2s }", "when: {}", 1)
		}},
		{"unknown bridge instance", func(s string) string {
			return strings.Replace(s, "pairs: [[a, b]]", "pairs: [[a, z]]", 1)
		}},
		{"bad expect op", func(s string) string {
			return strings.Replace(s, `op: "=="`, `op: "~="`, 1)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := LoadScenario(writeScenario(t, tc.edit(validScenario))); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestLoopGuardDetectsNearRepeats(t *testing.T) {
	last := []string{"Please repeat yourself one more time."}
	if !similarTail(last, "please  repeat yourself one more time") {
		t.Fatal("near-identical text should trip the loop guard")
	}
	if similarTail(last, "Efficiency buys you time to do what matters.") {
		t.Fatal("distinct text must not trip the loop guard")
	}
}

func TestLeakDetectorFlagsToolShapedText(t *testing.T) {
	leaky := "Called the Bash tool with the following input: {\"command\":\"grep -rn x\"}\nResult of calling the Bash tool:\nok"
	if got := leakLines(leaky); got != 2 {
		t.Fatalf("leakLines = %d, want 2", got)
	}
	if got := leakLines("I think efficiency is overrated, honestly."); got != 0 {
		t.Fatalf("clean text leakLines = %d, want 0", got)
	}
}

func TestMetricsAndExpectations(t *testing.T) {
	e := &engine{
		scn: &Scenario{Name: "demo"},
		records: map[string][]TapRecord{
			"a": {
				{Type: "user", Text: "hi"},
				{Type: "agent", Text: "hello there"},
				{Type: "agent", Text: "Called the Bash tool with input", Degraded: true},
				{Type: "error", Stage: "brain", Message: "ollama stream: boom"},
				{Type: "metrics", Outcome: "completed", ModelS: 2.0},
				{Type: "metrics", Outcome: "completed", ModelS: 4.0},
			},
		},
		directions: []*direction{{from: "a", to: "b", forwarded: 3}},
	}
	rm := computeMetrics(e, "max_exchanges")
	im := rm.Instances["a"]
	if im.Turns != 2 || im.DegradedTurns != 1 || im.Errors != 1 || im.ToolLeakLines != 1 {
		t.Fatalf("instance metrics = %+v", im)
	}
	if im.MeanModelS != 3.0 {
		t.Fatalf("mean model_s = %v", im.MeanModelS)
	}
	if rm.ExchangesCompleted != 3 {
		t.Fatalf("exchanges = %d", rm.ExchangesCompleted)
	}

	failures := evaluate(rm, []Expectation{
		{Metric: "exchanges_completed", Op: ">=", Value: 3},
		{Instance: "a", Metric: "degraded_turns", Op: "==", Value: 0},
		{Instance: "a", Metric: "nope", Op: "==", Value: 0},
	})
	if len(failures) != 2 {
		t.Fatalf("failures = %v, want degraded_turns + unknown metric", failures)
	}
}

func TestSlugify(t *testing.T) {
	if got := slugify("Uncle Fu!"); got != "uncle-fu" {
		t.Fatalf("slugify = %q", got)
	}
}

func TestStockScenariosLoad(t *testing.T) {
	paths, err := filepath.Glob("scenarios/*.yaml")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no stock scenarios found: %v", err)
	}
	for _, p := range paths {
		if _, err := LoadScenario(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}
