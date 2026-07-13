package config

import (
	"context"
	"errors"
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

	// File tools only run when the local tools gate is on (pipeline passes
	// VoiceToolsEnabled into every brain turn). Ollama users hit this most.
	if strings.EqualFold(strings.TrimSpace(cfg.BrainProvider), "ollama") {
		if cfg.VoiceToolsEnabled {
			diags = append(diags, Diagnostic{
				Name:     "voice-tools",
				Severity: SeverityOK,
				Detail:   "voice_tools_enabled=true (list_files/read_file/write_file/run_command available)",
			})
		} else {
			diags = append(diags, Diagnostic{
				Name:        "voice-tools",
				Severity:    SeverityWarn,
				Detail:      "voice_tools_enabled=false — Ollama will not read or write files",
				Remediation: "set voice_tools_enabled=true in config, or VOICE_TOOLS_ENABLED=true",
			})
		}
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

	// pdftotext (Poppler) is optional; missing is a warning so non-PDF users stay clean.
	if _, err := lookPath("pdftotext"); err != nil {
		diags = append(diags, Diagnostic{
			Name:        "pdftotext-binary",
			Severity:    SeverityWarn,
			Detail:      "pdftotext not found in PATH (optional; needed for PDF narrate/render)",
			Remediation: "install Poppler (e.g. brew install poppler) to enable PDF extraction",
		})
	} else {
		diags = append(diags, Diagnostic{Name: "pdftotext-binary", Severity: SeverityOK, Detail: "pdftotext"})
	}

	return diags
}

// VoiceDeviceChecker probes audio hardware availability. Implementations must
// honor ctx cancellation and deadlines so a wedged backend cannot hang doctor.
type VoiceDeviceChecker interface {
	CaptureDevices(ctx context.Context) ([]string, error)
	PlaybackDevices(ctx context.Context) ([]string, error)
}

// DiagnoseVoiceDevices checks microphone and speaker availability through
// checker. It is opt-in (doctor --voice-devices) because it touches audio
// hardware; the default Diagnose stays read-only and hardware-free. ctx should
// carry a short timeout.
func DiagnoseVoiceDevices(ctx context.Context, checker VoiceDeviceChecker) []Diagnostic {
	return []Diagnostic{
		voiceDeviceDiagnostic(ctx, "voice:microphone",
			"connect a microphone and allow microphone access for your terminal in OS privacy settings",
			checker.CaptureDevices),
		voiceDeviceDiagnostic(ctx, "voice:speaker",
			"connect or enable an output device in OS sound settings",
			checker.PlaybackDevices),
	}
}

func voiceDeviceDiagnostic(ctx context.Context, name, remediation string, list func(context.Context) ([]string, error)) Diagnostic {
	devices, err := list(ctx)
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		return Diagnostic{
			Name:        name,
			Severity:    SeverityWarn,
			Detail:      fmt.Sprintf("device probe timed out: %v", err),
			Remediation: "audio backend did not respond; close other apps using audio and retry, or check OS audio permissions",
		}
	case err != nil:
		return Diagnostic{
			Name:        name,
			Severity:    SeverityError,
			Detail:      fmt.Sprintf("device probe failed: %v", err),
			Remediation: remediation,
		}
	case len(devices) == 0:
		return Diagnostic{
			Name:        name,
			Severity:    SeverityError,
			Detail:      "no devices found",
			Remediation: remediation,
		}
	default:
		return Diagnostic{
			Name:     name,
			Severity: SeverityOK,
			Detail:   fmt.Sprintf("%d device(s): %s", len(devices), strings.Join(devices, ", ")),
		}
	}
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
