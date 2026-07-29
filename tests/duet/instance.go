package main

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Instance is one live samantha under the harness: its own $HOME, tmux
// window, transcript tap, and audio capture dir.
type Instance struct {
	ID      string
	Spec    *InstanceSpec
	Home    string // disposable $HOME
	Dir     string // run artifact dir for this instance
	TapPath string
	Target  string // tmux target (session:window)
}

// tmuxRun executes one tmux command.
func tmuxRun(args ...string) error {
	out, err := exec.Command("tmux", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// launchInstances creates the tmux session (one window per instance) and
// starts each samantha with its seeded environment.
func launchInstances(runDir, session, binPath string, s *Scenario) (map[string]*Instance, error) {
	ids := make([]string, 0, len(s.Instances))
	for id := range s.Instances {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	instances := map[string]*Instance{}
	for i, id := range ids {
		spec := s.Instances[id]
		inst := &Instance{
			ID:      id,
			Spec:    spec,
			Home:    filepath.Join(runDir, id, "home"),
			Dir:     filepath.Join(runDir, id),
			TapPath: filepath.Join(runDir, id, "transcript.jsonl"),
			Target:  session + ":" + id,
		}
		if err := seedInstance(inst.Home, s, id, spec); err != nil {
			return nil, fmt.Errorf("seed %s: %w", id, err)
		}

		w, h := spec.Pane.Width, spec.Pane.Height
		if w == 0 {
			w = 110
		}
		if h == 0 {
			h = 34
		}
		// The samantha command runs AS the window command — typing it into an
		// interactive shell races login-shell init (instant prompts eat
		// buffered keys and leave a half-pasted command at a PS2 prompt).
		cmd := launchCommand(binPath, inst, s)
		if i == 0 {
			if err := tmuxRun("new-session", "-d", "-s", session, "-n", id, "-x", strconv.Itoa(w), "-y", strconv.Itoa(h), cmd); err != nil {
				return nil, err
			}
		} else {
			if err := tmuxRun("new-window", "-t", session, "-n", id, cmd); err != nil {
				return nil, err
			}
		}
		// Keep the pane alive after exit so pane.txt still captures crashes.
		if err := tmuxRun("set-option", "-t", inst.Target, "remain-on-exit", "on"); err != nil {
			return nil, err
		}
		instances[id] = inst
	}
	return instances, nil
}

// launchCommand builds the shell line that starts one instance. The model
// cache and PATH pass through so seeded homes don't re-download runtime
// assets; everything identity-shaped lives under the disposable HOME.
func launchCommand(binPath string, inst *Instance, s *Scenario) string {
	env := map[string]string{
		"HOME":                   inst.Home,
		"SAMANTHA_COLOR_PROFILE": "ansi",
		"CLICOLOR_FORCE":         "1",
		"TERM":                   "xterm-256color",
	}
	if cache := os.Getenv("XDG_CACHE_HOME"); cache != "" {
		env["XDG_CACHE_HOME"] = cache
	} else if home, err := os.UserHomeDir(); err == nil {
		env["XDG_CACHE_HOME"] = filepath.Join(home, ".cache")
	}
	for k, v := range inst.Spec.Env {
		env[k] = v
	}

	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s ", k, shellQuote(env[k]))
	}
	b.WriteString(shellQuote(binPath))
	for _, f := range inst.Spec.Flags {
		b.WriteString(" " + shellQuote(f))
	}
	fmt.Fprintf(&b, " --transcript-log=%s", shellQuote(inst.TapPath))
	if s.Capture.Audio {
		// = form is required: --debug-audio declares NoOptDefVal, so a
		// space-separated value would be parsed as a positional command.
		fmt.Fprintf(&b, " --debug-audio=%s", shellQuote(filepath.Join(inst.Dir, "audio")))
	}
	return b.String()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// typeInto sends text to an instance's pane. Human typing lands in bursts
// with jitter so barge-in probes see realistic keystroke pacing.
func typeInto(ctx context.Context, inst *Instance, text, typing string, submit bool) error {
	if typing == "" {
		typing = "human"
	}
	if typing == "instant" {
		if err := tmuxRun("send-keys", "-t", inst.Target, "-l", "--", text); err != nil {
			return err
		}
	} else {
		for _, chunk := range chunkRunes(text, 4) {
			if err := tmuxRun("send-keys", "-t", inst.Target, "-l", "--", chunk); err != nil {
				return err
			}
			delay := 90*time.Millisecond + time.Duration(rand.Intn(80))*time.Millisecond
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	if submit {
		return tmuxRun("send-keys", "-t", inst.Target, "Enter")
	}
	return nil
}

func chunkRunes(s string, n int) []string {
	runes := []rune(s)
	var out []string
	for len(runes) > 0 {
		m := min(n, len(runes))
		out = append(out, string(runes[:m]))
		runes = runes[m:]
	}
	return out
}

// collectPane saves the full pane history for one instance.
func collectPane(inst *Instance) error {
	out, err := exec.Command("tmux", "capture-pane", "-t", inst.Target, "-p", "-S", "-").Output()
	if err != nil {
		return fmt.Errorf("capture-pane %s: %w", inst.Target, err)
	}
	return os.WriteFile(filepath.Join(inst.Dir, "pane.txt"), out, 0o644)
}

// shutdownInstances tears the session down unless keep is set; logs are
// collected either way.
func shutdownInstances(instances map[string]*Instance, session string, keep bool) {
	for _, inst := range instances {
		_ = collectPane(inst)
		src := filepath.Join(inst.Home, appDir, "logs", "native-diagnostics.log")
		if data, err := os.ReadFile(src); err == nil {
			_ = os.WriteFile(filepath.Join(inst.Dir, "native-diagnostics.log"), data, 0o644)
		}
	}
	if keep {
		fmt.Printf("  tmux session %q kept for inspection (tmux attach -t %s)\n", session, session)
		return
	}
	_ = tmuxRun("kill-session", "-t", session)
}
