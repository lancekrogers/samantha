package config

import (
	"fmt"
	"strings"
)

// Severity classifies a diagnostic result.
type Severity string

const (
	SeverityOK    Severity = "ok"
	SeverityWarn  Severity = "warn"
	SeverityError Severity = "error"
)

// Diagnostic is one read-only setup check result.
type Diagnostic struct {
	Name        string   `json:"name"`
	Severity    Severity `json:"severity"`
	Detail      string   `json:"detail"`
	Remediation string   `json:"remediation,omitempty"`
}

// Diagnose runs read-only setup checks for cfg against modelsDir and returns
// per-check diagnostics. lookPath resolves external binaries (inject a fake in
// tests; pass exec.LookPath in production). It never downloads or initializes
// providers, so it is safe offline. Missing model assets are warnings (run
// 'models ensure'); unsupported providers and absent required binaries are
// errors. It does not probe the microphone, so batch-only setups are not failed
// for lacking one.
func Diagnose(cfg *Config, modelsDir string, lookPath func(string) (string, error)) []Diagnostic {
	var diags []Diagnostic

	norm, sttErr := NormalizeSTTWithMode(cfg.STTProvider, cfg.STTMode)
	if sttErr == nil {
		diags = append(diags, Diagnostic{Name: "stt-provider", Severity: SeverityOK, Detail: fmt.Sprintf("%s/%s", norm.Provider, norm.Mode)})
	} else {
		diags = append(diags, Diagnostic{
			Name:        "stt-provider",
			Severity:    SeverityError,
			Detail:      sttErr.Error(),
			Remediation: "set a supported stt_provider/stt_mode combination",
		})
	}

	if ManagedTTS(cfg) {
		diags = append(diags, Diagnostic{Name: "tts-provider", Severity: SeverityOK, Detail: "kokoro"})
	} else {
		diags = append(diags, Diagnostic{
			Name:        "tts-provider",
			Severity:    SeverityWarn,
			Detail:      fmt.Sprintf("tts_provider %q is not a managed provider", cfg.TTSProvider),
			Remediation: "set tts_provider=kokoro for managed TTS assets",
		})
	}

	// Barge-in monitors the mic while Samantha speaks, so without the voice
	// frontend's echo cancellation her own playback can trip it.
	if cfg.BargeInEnabled && !cfg.VoiceFrontendEnabled {
		diags = append(diags, Diagnostic{
			Name:        "barge-in-echo",
			Severity:    SeverityWarn,
			Detail:      "barge_in_enabled without voice_frontend_enabled: playback echo may trigger barge-in",
			Remediation: "set voice_frontend_enabled=true when using barge-in",
		})
	}

	// whisper.cpp shells out to an external CLI; check it only when selected.
	if sttErr == nil && norm.Provider == STTProviderWhisperCPP {
		bin := strings.TrimSpace(cfg.WhisperCPPBinary)
		if _, err := lookPath(bin); err != nil {
			diags = append(diags, Diagnostic{
				Name:        "whispercpp-binary",
				Severity:    SeverityError,
				Detail:      fmt.Sprintf("whisper.cpp binary %q not found in PATH", bin),
				Remediation: "install whisper.cpp and put its CLI on PATH, or set whispercpp_binary",
			})
		} else {
			diags = append(diags, Diagnostic{Name: "whispercpp-binary", Severity: SeverityOK, Detail: bin})
		}
	}

	// Skip STT asset resolution when the STT config already failed above, so
	// the model-assets check stays about model names rather than repeating it.
	req := DefaultAssetRequest(cfg)
	req.NeedSTT = req.NeedSTT && sttErr == nil
	if m, err := ManifestFor(cfg, req); err != nil {
		diags = append(diags, Diagnostic{
			Name:        "model-assets",
			Severity:    SeverityError,
			Detail:      err.Error(),
			Remediation: "fix the configured model name",
		})
	} else {
		for _, s := range m.Status(modelsDir) {
			if s.Installed {
				diags = append(diags, Diagnostic{Name: "asset:" + s.ID, Severity: SeverityOK, Detail: s.Name + " installed"})
			} else {
				diags = append(diags, Diagnostic{
					Name:        "asset:" + s.ID,
					Severity:    SeverityWarn,
					Detail:      s.Name + " missing",
					Remediation: "run 'samantha models ensure'",
				})
			}
		}
	}

	return diags
}

// HasErrors reports whether any diagnostic has error severity.
func HasErrors(diags []Diagnostic) bool {
	for _, d := range diags {
		if d.Severity == SeverityError {
			return true
		}
	}
	return false
}
