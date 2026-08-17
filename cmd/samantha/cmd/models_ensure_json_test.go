package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

// --- resolveEnsureTier (--tier validation/normalization) ---

func TestResolveEnsureTierRequiresTTS(t *testing.T) {
	_, err := resolveEnsureTier("1.7b", false)
	if err == nil || !contains(err.Error(), "--tier applies to --tts") {
		t.Fatalf("resolveEnsureTier() error = %v, want it to name --tier/--tts", err)
	}
}

func TestResolveEnsureTierEmptyIsNoOp(t *testing.T) {
	tier, err := resolveEnsureTier("", false)
	if err != nil || tier != "" {
		t.Fatalf("resolveEnsureTier(\"\") = %q, %v, want \"\", nil", tier, err)
	}
	tier, err = resolveEnsureTier("", true)
	if err != nil || tier != "" {
		t.Fatalf("resolveEnsureTier(\"\", true) = %q, %v, want \"\", nil", tier, err)
	}
}

func TestResolveEnsureTierNormalizes(t *testing.T) {
	cases := map[string]string{
		"1.7":  "1.7b",
		"1.7b": "1.7b",
		"0.6":  "0.6b",
		"600m": "0.6b",
		"0b6":  "0.6b",
		"1b7":  "1.7b",
	}
	for in, want := range cases {
		got, err := resolveEnsureTier(in, true)
		if err != nil {
			t.Fatalf("resolveEnsureTier(%q) error = %v", in, err)
		}
		if got != want {
			t.Errorf("resolveEnsureTier(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- throttleDownloadTicks ---

func TestThrottleDownloadTicksDropsSubThresholdTicks(t *testing.T) {
	var forwarded []config.AssetProgress
	onProgress := throttleDownloadTicks(func(p config.AssetProgress) {
		forwarded = append(forwarded, p)
	})

	onProgress(config.AssetProgress{Asset: "a", Phase: "start", Pct: 0})
	// Rapid sub-1-point ticks with no delay: only the first should forward.
	onProgress(config.AssetProgress{Asset: "a", Phase: "download", Pct: 0.2})
	onProgress(config.AssetProgress{Asset: "a", Phase: "download", Pct: 0.5})
	onProgress(config.AssetProgress{Asset: "a", Phase: "download", Pct: 0.9})
	// A >=1 point jump must forward.
	onProgress(config.AssetProgress{Asset: "a", Phase: "download", Pct: 2.0})
	onProgress(config.AssetProgress{Asset: "a", Phase: "verify"})
	onProgress(config.AssetProgress{Asset: "a", Phase: "done", Pct: 100})

	var phases []string
	for _, p := range forwarded {
		phases = append(phases, fmt.Sprintf("%s:%.1f", p.Phase, p.Pct))
	}
	want := []string{"start:0.0", "download:0.2", "download:2.0", "verify:0.0", "done:100.0"}
	if strings.Join(phases, ",") != strings.Join(want, ",") {
		t.Fatalf("forwarded ticks = %v, want %v", phases, want)
	}
}

func TestThrottleDownloadTicksForwardsAfterElapsedTime(t *testing.T) {
	var forwarded []config.AssetProgress
	onProgress := throttleDownloadTicks(func(p config.AssetProgress) {
		forwarded = append(forwarded, p)
	})

	onProgress(config.AssetProgress{Asset: "a", Phase: "download", Pct: 0.1})
	time.Sleep(260 * time.Millisecond)
	onProgress(config.AssetProgress{Asset: "a", Phase: "download", Pct: 0.15})

	if len(forwarded) != 2 {
		t.Fatalf("forwarded = %d ticks, want 2 (elapsed-time threshold should force the second through)", len(forwarded))
	}
}

func TestThrottleDownloadTicksResetsPerAsset(t *testing.T) {
	var forwarded []config.AssetProgress
	onProgress := throttleDownloadTicks(func(p config.AssetProgress) {
		forwarded = append(forwarded, p)
	})

	onProgress(config.AssetProgress{Asset: "a", Phase: "download", Pct: 50})
	onProgress(config.AssetProgress{Asset: "a", Phase: "done", Pct: 100})
	// A new asset's first download tick always fires, even at a "low" pct
	// relative to the previous asset's last recorded pct.
	onProgress(config.AssetProgress{Asset: "b", Phase: "download", Pct: 0.1})

	if len(forwarded) != 3 {
		t.Fatalf("forwarded = %d ticks, want 3 (new asset must not inherit the old throttle state)", len(forwarded))
	}
}

// --- runModelsEnsureJSON ---

// fakeProgress emits a canned sequence of ticks and returns err.
func fakeProgress(ticks []config.AssetProgress, err error) ensureAssetsProgressFunc {
	return func(ctx context.Context, cfg *config.Config, req config.AssetRequest, onProgress func(config.AssetProgress)) error {
		for _, p := range ticks {
			onProgress(p)
		}
		return err
	}
}

func decodeNDJSONLines(t *testing.T, out string) []map[string]any {
	t.Helper()
	var lines []map[string]any
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("stdout line is not valid JSON: %v\nline: %q\nfull output:\n%s", err, line, out)
		}
		lines = append(lines, m)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan stdout: %v", err)
	}
	return lines
}

func TestModelsEnsureJSONStdoutIsNDJSONWithOneTerminalLine(t *testing.T) {
	orig := ensureRuntimeAssetsProgressFn
	ensureRuntimeAssetsProgressFn = fakeProgress([]config.AssetProgress{
		{Asset: "silero_vad.onnx", Phase: "start", Pct: 0},
		{Asset: "silero_vad.onnx", Phase: "download", Bytes: 1000, Total: 2000, Pct: 50},
		{Asset: "silero_vad.onnx", Phase: "verify", Pct: 50},
		{Asset: "silero_vad.onnx", Phase: "done", Pct: 100},
		{Asset: "kokoro model.onnx", Phase: "skipped", Pct: 100},
	}, nil)
	t.Cleanup(func() { ensureRuntimeAssetsProgressFn = orig })

	cfg := &config.Config{ModelsDir: t.TempDir()}
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetContext(context.Background())

	if err := runModelsEnsureJSON(cmd, cfg, config.AssetRequest{}, ""); err != nil {
		t.Fatalf("runModelsEnsureJSON() error = %v", err)
	}

	lines := decodeNDJSONLines(t, buf.String())
	if len(lines) != 6 {
		t.Fatalf("got %d NDJSON lines, want 6 (5 ticks + 1 terminal)", len(lines))
	}
	var terminalCount int
	for i, l := range lines {
		if _, ok := l["done"]; ok {
			terminalCount++
			if i != len(lines)-1 {
				t.Errorf("terminal line at index %d, want it last (index %d)", i, len(lines)-1)
			}
		}
	}
	if terminalCount != 1 {
		t.Fatalf("terminal ('done' key) line count = %d, want exactly 1", terminalCount)
	}
	last := lines[len(lines)-1]
	if last["done"] != true {
		t.Errorf("terminal done = %v, want true", last["done"])
	}
	installed, _ := last["installed"].([]any)
	skipped, _ := last["skipped"].([]any)
	if len(installed) != 1 || installed[0] != "silero_vad.onnx" {
		t.Errorf("terminal installed = %v, want [silero_vad.onnx]", last["installed"])
	}
	if len(skipped) != 1 || skipped[0] != "kokoro model.onnx" {
		t.Errorf("terminal skipped = %v, want [kokoro model.onnx]", last["skipped"])
	}
	if last["models_dir"] != cfg.ModelsDir {
		t.Errorf("terminal models_dir = %v, want %q", last["models_dir"], cfg.ModelsDir)
	}
}

func TestModelsEnsureJSONErrorPayloadNamesFailingAsset(t *testing.T) {
	orig := ensureRuntimeAssetsProgressFn
	ensureRuntimeAssetsProgressFn = fakeProgress([]config.AssetProgress{
		{Asset: "silero_vad.onnx", Phase: "start", Pct: 0},
		{Asset: "silero_vad.onnx", Phase: "download", Bytes: 10, Total: 2000, Pct: 0.5},
	}, errors.New("download silero_vad.onnx: checksum mismatch"))
	t.Cleanup(func() { ensureRuntimeAssetsProgressFn = orig })

	cfg := &config.Config{ModelsDir: t.TempDir()}
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetContext(context.Background())

	err := runModelsEnsureJSON(cmd, cfg, config.AssetRequest{}, "")
	if err == nil || !contains(err.Error(), "models ensure:") {
		t.Fatalf("runModelsEnsureJSON() error = %v, want it wrapped with 'models ensure:'", err)
	}

	lines := decodeNDJSONLines(t, buf.String())
	last := lines[len(lines)-1]
	if last["done"] != false {
		t.Fatalf("terminal done = %v, want false", last["done"])
	}
	if last["asset"] != "silero_vad.onnx" {
		t.Errorf("terminal asset = %v, want the failing asset name", last["asset"])
	}
	if !contains(fmt.Sprint(last["error"]), "checksum mismatch") {
		t.Errorf("terminal error = %v, want it to mention the failure", last["error"])
	}
	if _, ok := last["installed"]; ok {
		t.Errorf("failure line must not carry a success field, got %v", last)
	}
}

func TestModelsEnsureJSONCancelledContextReportsBareSentinel(t *testing.T) {
	orig := ensureRuntimeAssetsProgressFn
	ensureRuntimeAssetsProgressFn = fakeProgress([]config.AssetProgress{
		{Asset: "kokoro model.onnx", Phase: "start", Pct: 0},
	}, fmt.Errorf("download kokoro model.onnx: %w", context.Canceled))
	t.Cleanup(func() { ensureRuntimeAssetsProgressFn = orig })

	cfg := &config.Config{ModelsDir: t.TempDir()}
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetContext(context.Background())

	if err := runModelsEnsureJSON(cmd, cfg, config.AssetRequest{}, ""); err == nil {
		t.Fatal("runModelsEnsureJSON() error = nil, want the wrapped cancellation error")
	}

	lines := decodeNDJSONLines(t, buf.String())
	last := lines[len(lines)-1]
	if last["error"] != "context canceled" {
		t.Errorf("terminal error = %v, want the bare %q sentinel", last["error"], "context canceled")
	}
	if last["asset"] != "kokoro model.onnx" {
		t.Errorf("terminal asset = %v, want the in-flight asset", last["asset"])
	}
}

func TestModelsEnsureJSONExplicitTierInstallsWithoutWritingConfig(t *testing.T) {
	origEnsure := ensureRuntimeAssetsProgressFn
	ensureRuntimeAssetsProgressFn = fakeProgress(nil, nil)
	t.Cleanup(func() { ensureRuntimeAssetsProgressFn = origEnsure })

	origTier := ensureQwenTierProgressFn
	var gotTier string
	ensureQwenTierProgressFn = func(_ context.Context, cfg *config.Config, tier string, onProgress func(config.AssetProgress)) error {
		gotTier = tier
		onProgress(config.AssetProgress{Asset: "Qwen3-TTS native package", Phase: "start", Pct: 0})
		onProgress(config.AssetProgress{Asset: "Qwen3-TTS native package", Phase: "done", Pct: 100})
		return nil
	}
	t.Cleanup(func() { ensureQwenTierProgressFn = origTier })

	cfg := &config.Config{ModelsDir: t.TempDir(), TTSProvider: "qwen3-tts", QwenTTSModelTier: "0.6b"}
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetContext(context.Background())

	if err := runModelsEnsureJSON(cmd, cfg, config.AssetRequest{NeedTTS: true}, "1.7b"); err != nil {
		t.Fatalf("runModelsEnsureJSON() error = %v", err)
	}
	if gotTier != "1.7b" {
		t.Errorf("tier passed to the installer = %q, want 1.7b", gotTier)
	}
	if cfg.QwenTTSModelTier != "0.6b" {
		t.Errorf("cfg.QwenTTSModelTier = %q, want it untouched (0.6b)", cfg.QwenTTSModelTier)
	}

	lines := decodeNDJSONLines(t, buf.String())
	last := lines[len(lines)-1]
	if last["done"] != true {
		t.Fatalf("terminal done = %v, want true", last["done"])
	}
	installed, _ := last["installed"].([]any)
	if len(installed) != 1 || installed[0] != "Qwen3-TTS native package" {
		t.Errorf("terminal installed = %v, want the qwen native package", last["installed"])
	}
}

func TestModelsEnsureCommandRegistersJSONAndTierFlags(t *testing.T) {
	for _, name := range []string{"json", "tier"} {
		if modelsEnsureCmd.Flags().Lookup(name) == nil {
			t.Errorf("ensure command missing --%s flag", name)
		}
	}
}
