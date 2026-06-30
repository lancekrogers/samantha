package config

import (
	"errors"
	"strings"
	"testing"
)

func okLookPath(name string) (string, error)   { return "/usr/bin/" + name, nil }
func failLookPath(name string) (string, error) { return "", errors.New("not found") }

func diagByName(diags []Diagnostic) map[string]Diagnostic {
	out := make(map[string]Diagnostic, len(diags))
	for _, d := range diags {
		out[d.Name] = d
	}
	return out
}

func TestDiagnoseHealthyHasNoErrors(t *testing.T) {
	cfg := &Config{STTProvider: "sherpa", WhisperModel: "base.en", TTSProvider: "kokoro", VADEnabled: true}
	dir := t.TempDir()
	// Install every required asset.
	m, _ := ManifestFor(cfg, DefaultAssetRequest(cfg))
	for _, a := range m.Assets {
		for _, p := range a.installPaths(dir) {
			touchFile(t, p)
		}
	}

	diags := Diagnose(cfg, dir, okLookPath)
	if HasErrors(diags) {
		t.Fatalf("healthy setup reported errors: %+v", diags)
	}
	byName := diagByName(diags)
	if byName["stt-provider"].Severity != SeverityOK || byName["tts-provider"].Severity != SeverityOK {
		t.Errorf("provider checks not OK: %+v", diags)
	}
}

func TestDiagnoseMissingAssetsAreWarnings(t *testing.T) {
	cfg := &Config{STTProvider: "sherpa", WhisperModel: "base.en", TTSProvider: "kokoro", VADEnabled: true}
	diags := Diagnose(cfg, t.TempDir(), okLookPath)

	if HasErrors(diags) {
		t.Fatalf("missing assets should be warnings, not errors: %+v", diags)
	}
	warns := 0
	for _, d := range diags {
		if d.Severity == SeverityWarn && d.Remediation == "run 'samantha models ensure'" {
			warns++
		}
	}
	if warns == 0 {
		t.Errorf("expected missing-asset warnings with ensure remediation: %+v", diags)
	}
}

func TestDiagnoseUnsupportedProviderIsError(t *testing.T) {
	cfg := &Config{STTProvider: "bogus", TTSProvider: "kokoro"}
	diags := Diagnose(cfg, t.TempDir(), okLookPath)
	if !HasErrors(diags) {
		t.Fatal("unsupported stt provider should produce an error")
	}
	if diagByName(diags)["stt-provider"].Severity != SeverityError {
		t.Errorf("stt-provider should be error: %+v", diags)
	}
}

func TestDiagnoseWhisperCPPBinary(t *testing.T) {
	cfg := &Config{STTProvider: "whispercpp", WhisperCPPModel: "base.en", WhisperCPPBinary: "whisper-cli", TTSProvider: "kokoro"}

	missing := diagByName(Diagnose(cfg, t.TempDir(), failLookPath))
	if missing["whispercpp-binary"].Severity != SeverityError {
		t.Errorf("absent whisper.cpp binary should be an error: %+v", missing["whispercpp-binary"])
	}
	if missing["whispercpp-binary"].Remediation == "" {
		t.Error("whisper.cpp binary error should include remediation")
	}

	present := diagByName(Diagnose(cfg, t.TempDir(), okLookPath))
	if present["whispercpp-binary"].Severity != SeverityOK {
		t.Errorf("present whisper.cpp binary should be OK: %+v", present["whispercpp-binary"])
	}
}

func TestDiagnoseDoesNotCheckBinaryForSherpa(t *testing.T) {
	cfg := &Config{STTProvider: "sherpa", WhisperModel: "base.en", TTSProvider: "kokoro"}
	// failLookPath would error if a binary check ran; sherpa needs none.
	if _, ok := diagByName(Diagnose(cfg, t.TempDir(), failLookPath))["whispercpp-binary"]; ok {
		t.Error("sherpa setup should not run a whisper.cpp binary check")
	}
}

// TestDiagnoseNeverRequiresMicrophone proves batch/TTS-only readiness: a valid
// setup with assets present is healthy, and doctor never emits a microphone or
// audio-device check (so batch-only setups are not failed for lacking a mic).
func TestDiagnoseNeverRequiresMicrophone(t *testing.T) {
	cfg := &Config{STTProvider: "sherpa", WhisperModel: "base.en", TTSProvider: "kokoro", VADEnabled: true}
	dir := t.TempDir()
	m, _ := ManifestFor(cfg, DefaultAssetRequest(cfg))
	for _, a := range m.Assets {
		for _, p := range a.installPaths(dir) {
			touchFile(t, p)
		}
	}

	diags := Diagnose(cfg, dir, okLookPath)
	if HasErrors(diags) {
		t.Fatalf("valid setup with assets present should have no errors: %+v", diags)
	}
	for _, d := range diags {
		for _, banned := range []string{"mic", "microphone", "audio-device"} {
			if strings.Contains(d.Name, banned) || strings.Contains(d.Detail, banned) {
				t.Errorf("doctor must not require a microphone, found diagnostic %+v", d)
			}
		}
	}
	if got := diagByName(diags)["asset:tts.kokoro.multi-lang-v1_0"].Severity; got != SeverityOK {
		t.Errorf("kokoro asset severity = %q, want ok", got)
	}
}
