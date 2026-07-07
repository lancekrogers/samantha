package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/config"
)

func okLookPath(string) (string, error)   { return "/usr/bin/x", nil }
func failLookPath(string) (string, error) { return "", errors.New("not found") }

// fakeVoiceChecker implements config.VoiceDeviceChecker without hardware.
type fakeVoiceChecker struct {
	capture, playback []string
	err               error
}

func (f fakeVoiceChecker) CaptureDevices(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.capture, f.err
}

func (f fakeVoiceChecker) PlaybackDevices(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.playback, f.err
}

func runDoctorCmd(t *testing.T, cfg *config.Config, modelsDir string, lookPath func(string) (string, error), checker config.VoiceDeviceChecker, asJSON bool) (string, error) {
	t.Helper()
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	err := runDoctor(cmd, cfg, modelsDir, lookPath, checker, asJSON)
	return buf.String(), err
}

func TestDoctorWarningsExitZero(t *testing.T) {
	cfg := &config.Config{STTProvider: "sherpa", WhisperModel: "base.en", TTSProvider: "kokoro", VADEnabled: true}
	out, err := runDoctorCmd(t, cfg, t.TempDir(), okLookPath, nil, false)
	if err != nil {
		t.Fatalf("doctor with only warnings should exit 0, got %v", err)
	}
	if !contains(out, "WARN") || !contains(out, "models ensure") {
		t.Errorf("doctor output should warn about missing assets:\n%s", out)
	}
}

func TestDoctorErrorsExitNonZero(t *testing.T) {
	cfg := &config.Config{STTProvider: "whispercpp", WhisperCPPModel: "base.en", WhisperCPPBinary: "whisper-cli", TTSProvider: "kokoro"}
	out, err := runDoctorCmd(t, cfg, t.TempDir(), failLookPath, nil, false)
	if err == nil {
		t.Fatal("doctor with a missing required binary should return an error")
	}
	if !contains(out, "FAIL") || !contains(out, "whisper.cpp") {
		t.Errorf("doctor output should report the binary failure:\n%s", out)
	}
}

func TestDoctorJSON(t *testing.T) {
	cfg := &config.Config{STTProvider: "sherpa", WhisperModel: "base.en", TTSProvider: "kokoro"}
	out, err := runDoctorCmd(t, cfg, t.TempDir(), okLookPath, nil, true)
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

// TestDoctorDefaultHasNoHardwareChecks locks in that the default doctor stays
// read-only and hardware-free: no voice-device diagnostics without the flag.
func TestDoctorDefaultHasNoHardwareChecks(t *testing.T) {
	cfg := &config.Config{STTProvider: "sherpa", WhisperModel: "base.en", TTSProvider: "kokoro"}
	for _, asJSON := range []bool{false, true} {
		out, _ := runDoctorCmd(t, cfg, t.TempDir(), okLookPath, nil, asJSON)
		for _, banned := range []string{"voice:", "microphone", "speaker"} {
			if contains(out, banned) {
				t.Errorf("default doctor (json=%v) must not include hardware checks, found %q:\n%s", asJSON, banned, out)
			}
		}
	}
}

func TestDoctorVoiceDevicesProbeFailureExitsNonZero(t *testing.T) {
	cfg := &config.Config{STTProvider: "sherpa", WhisperModel: "base.en", TTSProvider: "kokoro"}
	out, err := runDoctorCmd(t, cfg, t.TempDir(), okLookPath, fakeVoiceChecker{err: errors.New("backend broken")}, false)
	if err == nil {
		t.Fatal("failed device probe should return an error")
	}
	if !contains(out, "FAIL") || !contains(out, "voice:microphone") {
		t.Errorf("doctor output should report the probe failure:\n%s", out)
	}
}

func TestDoctorVoiceDevicesTimeoutWarnsWithRemediation(t *testing.T) {
	cfg := &config.Config{STTProvider: "sherpa", WhisperModel: "base.en", TTSProvider: "kokoro"}
	// The fake honors ctx like the real checker; a pre-expired deadline
	// simulates a wedged backend hitting the probe timeout.
	cmd := &cobra.Command{}
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	<-ctx.Done()
	cmd.SetContext(ctx)
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := runDoctor(cmd, cfg, t.TempDir(), okLookPath, fakeVoiceChecker{capture: []string{"Mic"}}, false)
	if err != nil {
		t.Fatalf("timed-out probe should warn, not fail: %v", err)
	}
	out := buf.String()
	if !contains(out, "WARN") || !contains(out, "timed out") || !contains(out, "audio backend did not respond") {
		t.Errorf("timeout should produce a warning with remediation:\n%s", out)
	}
}

func TestDoctorVoiceDevicesAddsDiagnostics(t *testing.T) {
	cfg := &config.Config{STTProvider: "sherpa", WhisperModel: "base.en", TTSProvider: "kokoro"}
	checker := fakeVoiceChecker{capture: []string{"Built-in Mic"}, playback: []string{"Built-in Speaker"}}
	out, _ := runDoctorCmd(t, cfg, t.TempDir(), okLookPath, checker, false)
	if !contains(out, "voice:microphone") || !contains(out, "Built-in Mic") {
		t.Errorf("doctor --voice-devices should report microphone devices:\n%s", out)
	}
	if !contains(out, "voice:speaker") || !contains(out, "Built-in Speaker") {
		t.Errorf("doctor --voice-devices should report speaker devices:\n%s", out)
	}
}

func TestDoctorVoiceDevicesJSONIncludesDiagnostics(t *testing.T) {
	cfg := &config.Config{STTProvider: "sherpa", WhisperModel: "base.en", TTSProvider: "kokoro"}
	checker := fakeVoiceChecker{capture: []string{"Mic"}, playback: []string{"Speaker"}}
	out, err := runDoctorCmd(t, cfg, t.TempDir(), okLookPath, checker, true)
	if err != nil {
		t.Fatalf("doctor --voice-devices --json error = %v", err)
	}
	var diags []config.Diagnostic
	if err := json.Unmarshal([]byte(out), &diags); err != nil {
		t.Fatalf("doctor --voice-devices --json is not valid JSON: %v\n%s", err, out)
	}
	found := 0
	for _, d := range diags {
		if strings.HasPrefix(d.Name, "voice:") {
			found++
		}
	}
	if found != 2 {
		t.Errorf("JSON output should include 2 voice diagnostics, got %d: %+v", found, diags)
	}
}
