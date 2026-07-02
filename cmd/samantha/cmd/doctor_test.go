package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/config"
)

func okLookPath(string) (string, error)   { return "/usr/bin/x", nil }
func failLookPath(string) (string, error) { return "", errors.New("not found") }

func runDoctorCmd(t *testing.T, cfg *config.Config, modelsDir string, lookPath func(string) (string, error), asJSON bool) (string, error) {
	t.Helper()
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	err := runDoctor(cmd, cfg, modelsDir, lookPath, asJSON)
	return buf.String(), err
}

func TestDoctorWarningsExitZero(t *testing.T) {
	cfg := &config.Config{STTProvider: "sherpa", WhisperModel: "base.en", TTSProvider: "kokoro", VADEnabled: true}
	out, err := runDoctorCmd(t, cfg, t.TempDir(), okLookPath, false)
	if err != nil {
		t.Fatalf("doctor with only warnings should exit 0, got %v", err)
	}
	if !contains(out, "WARN") || !contains(out, "models ensure") {
		t.Errorf("doctor output should warn about missing assets:\n%s", out)
	}
}

func TestDoctorErrorsExitNonZero(t *testing.T) {
	cfg := &config.Config{STTProvider: "whispercpp", WhisperCPPModel: "base.en", WhisperCPPBinary: "whisper-cli", TTSProvider: "kokoro"}
	out, err := runDoctorCmd(t, cfg, t.TempDir(), failLookPath, false)
	if err == nil {
		t.Fatal("doctor with a missing required binary should return an error")
	}
	if !contains(out, "FAIL") || !contains(out, "whisper.cpp") {
		t.Errorf("doctor output should report the binary failure:\n%s", out)
	}
}

func TestDoctorJSON(t *testing.T) {
	cfg := &config.Config{STTProvider: "sherpa", WhisperModel: "base.en", TTSProvider: "kokoro"}
	out, err := runDoctorCmd(t, cfg, t.TempDir(), okLookPath, true)
	if err != nil {
		t.Fatalf("doctor --json error = %v", err)
	}
	var diags []config.Diagnostic
	if err := json.Unmarshal([]byte(out), &diags); err != nil {
		t.Fatalf("doctor --json is not valid JSON: %v\n%s", err, out)
	}
	if len(diags) == 0 {
		t.Error("doctor --json produced no diagnostics")
	}
}
