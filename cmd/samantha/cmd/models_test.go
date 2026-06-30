package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/config"
)

func runStatus(t *testing.T, cfg *config.Config, modelsDir string, asJSON bool) string {
	t.Helper()
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := runModelsStatus(cmd, cfg, modelsDir, asJSON); err != nil {
		t.Fatalf("runModelsStatus() error = %v", err)
	}
	return buf.String()
}

func TestModelsStatusListsMissingAssets(t *testing.T) {
	cfg := &config.Config{STTProvider: "sherpa", WhisperModel: "base.en", TTSProvider: "kokoro", VADEnabled: true}
	dir := t.TempDir()

	out := runStatus(t, cfg, dir, false)
	for _, want := range []string{"silero_vad.onnx", "kokoro-tts", "whisper-base.en", "missing", "3 asset(s), 3 missing"} {
		if !contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}
}

func TestModelsStatusJSONIsMachineReadable(t *testing.T) {
	cfg := &config.Config{STTProvider: "whispercpp", WhisperCPPModel: "base.en"}
	dir := t.TempDir()

	out := runStatus(t, cfg, dir, true)
	var statuses []config.AssetStatus
	if err := json.Unmarshal([]byte(out), &statuses); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\n%s", err, out)
	}
	if len(statuses) != 1 || statuses[0].Provider != "whispercpp" || statuses[0].Installed {
		t.Fatalf("json statuses = %+v, want 1 missing whispercpp asset", statuses)
	}
}

func TestModelsStatusReportsInstalled(t *testing.T) {
	cfg := &config.Config{STTProvider: "whispercpp", WhisperCPPModel: "base.en"}
	dir := t.TempDir()
	// Install the whisper.cpp file so status reports it present.
	p := filepath.Join(dir, "whispercpp", "ggml-base.en.bin")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := runStatus(t, cfg, dir, false)
	// The asset line should say "installed"; the missing-state suffix must be
	// absent, and the summary should report zero missing.
	if !contains(out, "installed") || contains(out, "run 'samantha models ensure'") {
		t.Errorf("status should report installed and no missing-state line:\n%s", out)
	}
	if !contains(out, "1 asset(s), 0 missing") {
		t.Errorf("status summary should report 0 missing:\n%s", out)
	}
}

func contains(s, sub string) bool {
	return bytes.Contains([]byte(s), []byte(sub))
}
