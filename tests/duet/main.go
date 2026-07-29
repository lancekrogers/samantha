// Command duet runs a scripted multi-instance samantha conversation in tmux
// and leaves a reviewable artifact directory: the WI-dc9e33 duet harness.
//
//	go run ./tests/duet -scenario tests/duet/scenarios/crosstalk.yaml
//	just duet crosstalk
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	scenarioPath := flag.String("scenario", "", "scenario YAML (required)")
	binPath := flag.String("bin", "bin/samantha", "samantha binary (just build)")
	runsDir := flag.String("runs-dir", "runs", "artifact directory root")
	keep := flag.Bool("keep", false, "keep the tmux session alive after the run")
	flag.Parse()

	if err := run(*scenarioPath, *binPath, *runsDir, *keep); err != nil {
		fmt.Fprintf(os.Stderr, "duet: %v\n", err)
		os.Exit(1)
	}
}

func run(scenarioPath, binPath, runsDir string, keep bool) error {
	if scenarioPath == "" {
		return fmt.Errorf("-scenario is required")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		return fmt.Errorf("tmux not found on PATH")
	}
	abs, err := filepath.Abs(binPath)
	if err != nil {
		return err
	}
	if _, err := os.Stat(abs); err != nil {
		return fmt.Errorf("samantha binary %s not found — run `just build` first", abs)
	}

	scn, err := LoadScenario(scenarioPath)
	if err != nil {
		return err
	}

	started := time.Now()
	runDir, err := filepath.Abs(filepath.Join(runsDir, fmt.Sprintf("%s-%s", scn.Name, started.UTC().Format("20060102T150405Z"))))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}
	if data, err := os.ReadFile(scenarioPath); err == nil {
		_ = os.WriteFile(filepath.Join(runDir, "scenario.yaml"), data, 0o644)
	}
	fmt.Printf("duet %s → %s\n", scn.Name, runDir)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	session := "duet-" + started.UTC().Format("150405")
	instances, err := launchInstances(runDir, session, abs, scn)
	if err != nil {
		_ = tmuxRun("kill-session", "-t", session)
		return err
	}
	defer shutdownInstances(instances, session, keep)

	for id, inst := range instances {
		if err := awaitConversation(ctx, inst, 45*time.Second); err != nil {
			return fmt.Errorf("instance %s never reached a conversation: %w — pane history in %s", id, err, inst.Dir)
		}
		fmt.Printf("  %s ready (%s)\n", id, inst.Spec.Persona.DisplayName)
	}

	eng, err := newEngine(scn, instances, runDir)
	if err != nil {
		return err
	}
	defer eng.close()

	stopReason := eng.run(ctx)
	fmt.Printf("  run stopped: %s\n", stopReason)

	// Give trailing records (final agent turns, metrics) a moment to land.
	drain(eng, 2*time.Second)

	rm := computeMetrics(eng, stopReason)
	if err := writeArtifacts(runDir, eng, rm, started); err != nil {
		return err
	}
	fmt.Printf("  report: %s\n", filepath.Join(runDir, "report.md"))

	if failures := evaluate(rm, scn.Expect); len(failures) > 0 {
		for _, f := range failures {
			fmt.Fprintf(os.Stderr, "  FAIL %s\n", f)
		}
		return fmt.Errorf("%d expectation(s) failed — see %s", len(failures), runDir)
	}
	if len(scn.Expect) > 0 {
		fmt.Printf("  all %d expectations passed\n", len(scn.Expect))
	}
	return nil
}

// drain pulls any already-queued tap events into the engine's records after
// the loop stopped, so final replies make it into the report.
func drain(e *engine, window time.Duration) {
	deadline := time.After(window)
	for {
		select {
		case ev := <-e.taps:
			e.records[ev.Instance] = append(e.records[ev.Instance], ev.Rec)
		case <-deadline:
			return
		}
	}
}
