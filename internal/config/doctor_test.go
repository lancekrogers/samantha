package config

import (
	"context"
	"errors"
	"path/filepath"
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

func TestDiagnoseReportsEnabledMeetingSpeakerAssets(t *testing.T) {
	cfg := &Config{STTProvider: "none", TTSProvider: "none"}
	cfg.Speaker.Enabled = true
	cfg.Speaker.Meeting.Enabled = true
	byName := diagByName(Diagnose(cfg, t.TempDir(), okLookPath))
	for _, name := range []string{
		"asset:speaker.segmentation.pyannote-3.0",
		"asset:speaker.embedding.nemo-titanet-small",
	} {
		diagnostic, ok := byName[name]
		if !ok || diagnostic.Severity != SeverityWarn || !strings.Contains(diagnostic.Remediation, "models ensure") {
			t.Fatalf("speaker diagnostic %q = %+v", name, diagnostic)
		}
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

func TestDiagnoseBrainProvider(t *testing.T) {
	tests := []struct {
		name            string
		cfg             Config
		lookPath        func(string) (string, error)
		wantSeverity    Severity
		wantDetail      string
		wantRemediation string
	}{
		{
			name:         "default claude found",
			cfg:          Config{},
			lookPath:     okLookPath,
			wantSeverity: SeverityOK,
			wantDetail:   "claude CLI",
		},
		{
			name:            "selected claude missing",
			cfg:             Config{BrainProvider: "claude"},
			lookPath:        failLookPath,
			wantSeverity:    SeverityError,
			wantDetail:      "claude CLI not found",
			wantRemediation: "brain_provider",
		},
		{
			name:            "selected grok missing",
			cfg:             Config{BrainProvider: "grok"},
			lookPath:        failLookPath,
			wantSeverity:    SeverityError,
			wantDetail:      "grok CLI not found",
			wantRemediation: "brain_provider",
		},
		{
			name:            "ollama model missing",
			cfg:             Config{BrainProvider: "ollama", OllamaHost: "http://localhost:11434"},
			lookPath:        failLookPath,
			wantSeverity:    SeverityError,
			wantDetail:      "ollama_model is not configured",
			wantRemediation: "samantha config ollama_model",
		},
		{
			name:            "ollama host invalid",
			cfg:             Config{BrainProvider: "ollama", OllamaModel: "gemma", OllamaHost: "localhost:11434"},
			lookPath:        failLookPath,
			wantSeverity:    SeverityError,
			wantDetail:      "invalid ollama_host",
			wantRemediation: "http://",
		},
		{
			name:         "ollama configured without requiring cli",
			cfg:          Config{BrainProvider: "ollama", OllamaModel: "gemma", OllamaHost: "http://localhost:11434"},
			lookPath:     failLookPath,
			wantSeverity: SeverityOK,
			wantDetail:   "connectivity is not probed",
		},
		{
			name:            "unsupported",
			cfg:             Config{BrainProvider: "other"},
			lookPath:        okLookPath,
			wantSeverity:    SeverityError,
			wantDetail:      "unsupported brain_provider",
			wantRemediation: "claude, grok, or ollama",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := diagnoseBrainProvider(&tt.cfg, tt.lookPath)
			if got.Severity != tt.wantSeverity {
				t.Fatalf("severity = %q, want %q: %+v", got.Severity, tt.wantSeverity, got)
			}
			if !strings.Contains(got.Detail, tt.wantDetail) {
				t.Errorf("detail = %q, want %q", got.Detail, tt.wantDetail)
			}
			if tt.wantRemediation != "" && !strings.Contains(got.Remediation, tt.wantRemediation) {
				t.Errorf("remediation = %q, want %q", got.Remediation, tt.wantRemediation)
			}
		})
	}
}

// TestDiagnoseConflictingSTTModeIsError proves an stt_provider/stt_mode
// conflict surfaces as a doctor error with the resolver's actionable message,
// and does not leak a misleading model-assets error.
func TestDiagnoseConflictingSTTModeIsError(t *testing.T) {
	cfg := &Config{STTProvider: "sherpa-streaming", STTMode: "offline", TTSProvider: "kokoro"}
	diags := Diagnose(cfg, t.TempDir(), okLookPath)
	if !HasErrors(diags) {
		t.Fatal("conflicting stt_mode should produce an error")
	}
	byName := diagByName(diags)
	if byName["stt-provider"].Severity != SeverityError {
		t.Fatalf("stt-provider severity = %q, want error: %+v", byName["stt-provider"].Severity, diags)
	}
	if !strings.Contains(byName["stt-provider"].Detail, "conflicts with stt_mode") {
		t.Errorf("stt-provider detail = %q, want conflict message", byName["stt-provider"].Detail)
	}
	if d, ok := byName["model-assets"]; ok {
		t.Errorf("unexpected model-assets diagnostic for an stt_mode conflict: %+v", d)
	}
}

// TestDiagnoseSTTModeResolvesProvider proves doctor reports the mode-resolved
// provider for the preferred stt_provider + stt_mode schema.
func TestDiagnoseSTTModeResolvesProvider(t *testing.T) {
	cfg := &Config{STTProvider: "sherpa", STTMode: "streaming", TTSProvider: "kokoro"}
	d := diagByName(Diagnose(cfg, t.TempDir(), okLookPath))["stt-provider"]
	if d.Severity != SeverityOK || d.Detail != "sherpa/streaming" {
		t.Fatalf("stt-provider = %+v, want ok sherpa/streaming", d)
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

func TestDiagnoseQwenNativeWorker(t *testing.T) {
	cfg := &Config{
		STTProvider:   "sherpa",
		TTSProvider:   "qwen3-tts",
		QwenTTSBinary: "qwen3-tts-cli",
		QwenTTSModel:  t.TempDir(),
	}

	diags := diagByName(Diagnose(cfg, t.TempDir(), okLookPath))
	for _, name := range []string{"tts-provider", "qwen3-tts-binary", "qwen3-tts-model"} {
		if diags[name].Severity != SeverityOK {
			t.Errorf("%s = %+v, want ok", name, diags[name])
		}
	}
	if diags["qwen3-tts-voice-controls"].Severity != SeverityOK {
		t.Errorf("baseline qwen voice controls = %+v, want ok", diags["qwen3-tts-voice-controls"])
	}
	if !strings.Contains(diags["tts-provider"].Detail, "external") {
		t.Errorf("external Qwen provider detail = %q", diags["tts-provider"].Detail)
	}
	if HasErrors([]Diagnostic{diags["tts-provider"], diags["qwen3-tts-binary"], diags["qwen3-tts-model"]}) {
		t.Fatalf("healthy qwen setup reported errors: %+v", diags)
	}

	diags = diagByName(Diagnose(cfg, t.TempDir(), failLookPath))
	if diags["qwen3-tts-binary"].Severity != SeverityError {
		t.Errorf("missing qwen worker = %+v, want error", diags["qwen3-tts-binary"])
	}

	cfg.QwenTTSModel = ""
	diags = diagByName(Diagnose(cfg, t.TempDir(), okLookPath))
	if diags["qwen3-tts-model"].Severity != SeverityError {
		t.Errorf("empty qwen model = %+v, want error", diags["qwen3-tts-model"])
	}

	nonDir := filepath.Join(t.TempDir(), "model-file")
	touchFile(t, nonDir)
	cfg.QwenTTSModel = nonDir
	diags = diagByName(Diagnose(cfg, t.TempDir(), okLookPath))
	if diags["qwen3-tts-model"].Severity != SeverityError {
		t.Errorf("non-directory qwen model = %+v, want error", diags["qwen3-tts-model"])
	}
}

func TestDiagnoseQwenUnsupportedVoiceControls(t *testing.T) {
	cfg := &Config{
		STTProvider:  "sherpa",
		TTSProvider:  "qwen3-tts",
		QwenTTSMode:  "voicedesign",
		QwenTTSModel: t.TempDir(),
	}
	d := diagByName(Diagnose(cfg, t.TempDir(), okLookPath))["qwen3-tts-voice-controls"]
	if d.Severity != SeverityError || !strings.Contains(d.Detail, "clear unsupported settings") || d.Remediation == "" {
		t.Fatalf("unsupported qwen controls diagnostic = %+v, want actionable error", d)
	}
}

func TestValidateQwenExternalWorkerRejectsManagedSpeaker(t *testing.T) {
	err := ValidateQwenTTSConfig(&Config{
		QwenTTSBinary: "/opt/qwen/worker", QwenTTSModel: "/opt/qwen/model",
		QwenTTSMode: "customvoice", QwenTTSVoice: "Vivian",
	})
	if err == nil || !strings.Contains(err.Error(), "external") {
		t.Fatalf("external worker validation = %v, want unsupported-controls error", err)
	}
}

func TestDiagnoseDefaultKokoroDoesNotRequireOptionalQwen(t *testing.T) {
	cfg := &Config{
		BrainProvider: "ollama",
		OllamaModel:   "test-model",
		OllamaHost:    "http://localhost:11434",
		STTProvider:   "sherpa",
		TTSProvider:   "kokoro",
	}
	diags := diagByName(Diagnose(cfg, t.TempDir(), failLookPath))

	if d := diags["tts-provider"]; d.Severity != SeverityOK || !strings.Contains(d.Detail, "kokoro") {
		t.Fatalf("default TTS diagnostic = %+v, want healthy Kokoro selection", d)
	}
	for name := range diags {
		if strings.HasPrefix(name, "qwen3-tts-") {
			t.Fatalf("default Kokoro diagnosis probed optional Qwen check %q: %+v", name, diags[name])
		}
	}
	if HasErrors(Diagnose(cfg, t.TempDir(), failLookPath)) {
		t.Fatal("missing optional binaries must not prevent the default Kokoro setup")
	}
}

func TestDiagnoseQwenMissingWorkerIncludesRemediation(t *testing.T) {
	cfg := &Config{STTProvider: "sherpa", TTSProvider: "qwen3-tts", QwenTTSModel: t.TempDir()}
	d := diagByName(Diagnose(cfg, t.TempDir(), failLookPath))["qwen3-tts-binary"]
	if d.Severity != SeverityError {
		t.Fatalf("missing Qwen worker = %+v, want error for explicitly selected provider", d)
	}
	if !strings.Contains(d.Detail, "not found") || !strings.Contains(d.Remediation, "qwen3-tts-cli") {
		t.Fatalf("missing Qwen worker lacks actionable details: %+v", d)
	}
}

func TestDiagnoseDoesNotCheckBinaryForSherpa(t *testing.T) {
	cfg := &Config{STTProvider: "sherpa", WhisperModel: "base.en", TTSProvider: "kokoro"}
	// failLookPath would error if a binary check ran; sherpa needs none.
	if _, ok := diagByName(Diagnose(cfg, t.TempDir(), failLookPath))["whispercpp-binary"]; ok {
		t.Error("sherpa setup should not run a whisper.cpp binary check")
	}
}

func TestDiagnoseCalibreOptional(t *testing.T) {
	cfg := &Config{STTProvider: "sherpa", WhisperModel: "base.en", TTSProvider: "kokoro"}

	missing := diagByName(Diagnose(cfg, t.TempDir(), failLookPath))["calibre-binary"]
	if missing.Severity != SeverityWarn {
		t.Fatalf("missing calibre should be warn, got %+v", missing)
	}
	if missing.Remediation == "" {
		t.Fatal("expected remediation")
	}

	present := diagByName(Diagnose(cfg, t.TempDir(), okLookPath))["calibre-binary"]
	if present.Severity != SeverityOK {
		t.Fatalf("present calibre should be OK, got %+v", present)
	}
	if HasErrors([]Diagnostic{missing, present}) {
		t.Fatal("calibre diagnostics must never be errors")
	}
}

// TestDiagnoseNeverRequiresMicrophone proves batch/TTS-only readiness: a valid
// setup with assets present is healthy, and doctor never emits a microphone or
// audio-device check (so batch-only setups are not failed for lacking a mic).
func TestDiagnoseNeverRequiresMicrophone(t *testing.T) {
	cfg := &Config{STTProvider: "sherpa", WhisperModel: "base.en", TTSProvider: "kokoro", VADEnabled: false}
	dir := t.TempDir()
	m, _ := ManifestFor(cfg, DefaultAssetRequest(cfg))
	for _, a := range m.Assets {
		for _, p := range a.installPaths(dir) {
			touchFile(t, p)
		}
		if a.IsArchive() && a.Archive.SHA256 != "" {
			target := dir
			if a.TargetDir != "" {
				target = filepath.Join(dir, a.TargetDir)
			}
			if err := writeArchiveInstallMarker(target, a.ID, a.Archive.URL, a.Archive.SHA256, a.CheckFiles); err != nil {
				t.Fatalf("writeArchiveInstallMarker(%s) error = %v", a.ID, err)
			}
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

// fakeDeviceChecker implements VoiceDeviceChecker without touching hardware.
type fakeDeviceChecker struct {
	capture, playback []string
	err               error
}

func (f fakeDeviceChecker) CaptureDevices(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.capture, f.err
}

func (f fakeDeviceChecker) PlaybackDevices(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.playback, f.err
}

func TestDiagnoseVoiceDevicesProbeFailureIsError(t *testing.T) {
	diags := DiagnoseVoiceDevices(context.Background(), fakeDeviceChecker{err: errors.New("backend broken")})
	byName := diagByName(diags)
	for _, name := range []string{"voice:microphone", "voice:speaker"} {
		if byName[name].Severity != SeverityError {
			t.Errorf("%s severity = %q, want error: %+v", name, byName[name].Severity, byName[name])
		}
		if byName[name].Remediation == "" {
			t.Errorf("%s probe failure should include remediation", name)
		}
	}
}

func TestDiagnoseVoiceDevicesNoDevicesIsError(t *testing.T) {
	diags := DiagnoseVoiceDevices(context.Background(), fakeDeviceChecker{playback: []string{"Speaker"}})
	byName := diagByName(diags)
	mic := byName["voice:microphone"]
	if mic.Severity != SeverityError || !strings.Contains(mic.Detail, "no devices found") {
		t.Errorf("missing microphone should be an error: %+v", mic)
	}
	if !strings.Contains(mic.Remediation, "microphone") {
		t.Errorf("microphone error should hint at permissions/connection: %+v", mic)
	}
	if byName["voice:speaker"].Severity != SeverityOK {
		t.Errorf("present speaker should be OK: %+v", byName["voice:speaker"])
	}
}

func TestDiagnoseVoiceDevicesTimeoutIsWarning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	<-ctx.Done()

	diags := DiagnoseVoiceDevices(ctx, fakeDeviceChecker{capture: []string{"Mic"}, playback: []string{"Speaker"}})
	if HasErrors(diags) {
		t.Fatalf("timed-out probe must be a warning, not an error: %+v", diags)
	}
	for _, d := range diags {
		if d.Severity != SeverityWarn {
			t.Errorf("%s severity = %q, want warn on timeout", d.Name, d.Severity)
		}
		if !strings.Contains(d.Detail, "timed out") || d.Remediation == "" {
			t.Errorf("timeout diagnostic should explain and remediate: %+v", d)
		}
	}
}

func TestDiagnoseVoiceDevicesCancelledContextIsWarning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	diags := DiagnoseVoiceDevices(ctx, fakeDeviceChecker{capture: []string{"Mic"}})
	if HasErrors(diags) {
		t.Fatalf("cancelled probe must not be an error: %+v", diags)
	}
	if diagByName(diags)["voice:microphone"].Severity != SeverityWarn {
		t.Errorf("cancelled context should produce warnings: %+v", diags)
	}
}

func TestDiagnoseVoiceDevicesHealthy(t *testing.T) {
	diags := DiagnoseVoiceDevices(context.Background(), fakeDeviceChecker{
		capture:  []string{"Built-in Mic"},
		playback: []string{"Built-in Speaker", "Headphones"},
	})
	byName := diagByName(diags)
	mic := byName["voice:microphone"]
	if mic.Severity != SeverityOK || !strings.Contains(mic.Detail, "Built-in Mic") {
		t.Errorf("microphone diagnostic should list devices: %+v", mic)
	}
	spk := byName["voice:speaker"]
	if spk.Severity != SeverityOK || !strings.Contains(spk.Detail, "2 device(s)") {
		t.Errorf("speaker diagnostic should count devices: %+v", spk)
	}
}

func TestDiagnoseWarnsOnBargeInWithoutFrontend(t *testing.T) {
	cfg := &Config{STTProvider: "sherpa", TTSProvider: "kokoro", BargeInEnabled: true, VoiceFrontendEnabled: false}
	diags := Diagnose(cfg, t.TempDir(), func(string) (string, error) { return "", nil })
	found := false
	for _, d := range diags {
		if d.Name == "barge-in-echo" {
			found = true
			if d.Severity != SeverityWarn {
				t.Errorf("barge-in-echo severity = %q, want warn", d.Severity)
			}
		}
	}
	if !found {
		t.Fatal("expected a barge-in-echo warning when barge-in is on without the voice frontend")
	}
	if HasErrors(diags) {
		t.Error("barge-in-echo must be a warning, not an error")
	}

	// With the frontend enabled (or barge-in off) there is no warning.
	cfg.VoiceFrontendEnabled = true
	for _, d := range Diagnose(cfg, t.TempDir(), func(string) (string, error) { return "", nil }) {
		if d.Name == "barge-in-echo" {
			t.Fatal("no barge-in-echo warning expected when the frontend is enabled")
		}
	}
}
