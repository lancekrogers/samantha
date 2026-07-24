package config

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"runtime"
	"strings"

	"github.com/lancekrogers/samantha/internal/platforminfo"
	"github.com/lancekrogers/samantha/internal/qwen"
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

	diags = append(diags, diagnoseBrainProvider(cfg, lookPath))

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

	if strings.EqualFold(strings.TrimSpace(cfg.TTSProvider), qwen.ProviderName) {
		binary := strings.TrimSpace(cfg.QwenTTSBinary)
		model := strings.TrimSpace(cfg.QwenTTSModel)
		managed := qwen.UseManaged(binary, model)
		providerDetail := "qwen3-tts (external default-voice worker)"
		controlsDetail := "external model-native default/static synthesis"
		controlsRemediation := "clear unsupported voice controls for the external worker"
		if managed {
			providerDetail = "qwen3-tts (managed CustomVoice presets)"
			controlsDetail = "managed CustomVoice speaker selection"
			controlsRemediation = "select a speaker and mode advertised in Settings → Voice"
		}
		diags = append(diags, Diagnostic{
			Name:     "tts-provider",
			Severity: SeverityOK,
			Detail:   providerDetail,
		})
		if err := ValidateQwenTTSConfig(cfg); err != nil {
			diags = append(diags, Diagnostic{
				Name:        "qwen3-tts-voice-controls",
				Severity:    SeverityError,
				Detail:      err.Error(),
				Remediation: controlsRemediation,
			})
		} else {
			diags = append(diags, Diagnostic{
				Name:     "qwen3-tts-voice-controls",
				Severity: SeverityOK,
				Detail:   controlsDetail,
			})
		}

		if managed {
			status := qwen.Inspect(modelsDir)
			if status.RuntimeReady {
				diags = append(diags, Diagnostic{Name: "qwen3-tts-binary", Severity: SeverityOK, Detail: status.Python})
			} else {
				diags = append(diags, Diagnostic{
					Name: "qwen3-tts-binary", Severity: SeverityError,
					Detail:      "managed Qwen runtime is not installed",
					Remediation: "open Settings → TTS, select Qwen3-TTS, and install preset voices",
				})
			}
			if status.ModelReady {
				diags = append(diags, Diagnostic{Name: "qwen3-tts-model", Severity: SeverityOK, Detail: status.Model})
			} else {
				diags = append(diags, Diagnostic{
					Name: "qwen3-tts-model", Severity: SeverityError,
					Detail:      "managed Qwen CustomVoice model is not installed",
					Remediation: "open Settings → TTS, select Qwen3-TTS, and install preset voices",
				})
			}
		} else {
			if binary == "" {
				binary = "qwen3-tts-cli"
			}
			if _, err := lookPath(binary); err != nil {
				diags = append(diags, Diagnostic{
					Name:        "qwen3-tts-binary",
					Severity:    SeverityError,
					Detail:      fmt.Sprintf("native Qwen3-TTS worker %q not found in PATH", binary),
					Remediation: "install qwen3-tts.cpp's qwen3-tts-cli and set qwen_tts_binary if needed",
				})
			} else {
				diags = append(diags, Diagnostic{Name: "qwen3-tts-binary", Severity: SeverityOK, Detail: binary})
			}
			if model == "" {
				diags = append(diags, Diagnostic{
					Name: "qwen3-tts-model", Severity: SeverityError, Detail: "qwen_tts_model is not configured",
					Remediation: "set qwen_tts_model or clear qwen_tts_binary to use managed setup",
				})
			} else if info, err := os.Stat(model); err != nil || !info.IsDir() {
				diags = append(diags, Diagnostic{
					Name: "qwen3-tts-model", Severity: SeverityError,
					Detail:      fmt.Sprintf("Qwen3-TTS model directory %q is unavailable", model),
					Remediation: "install/convert the Qwen3-TTS native model artifacts and set qwen_tts_model",
				})
			} else {
				diags = append(diags, Diagnostic{Name: "qwen3-tts-model", Severity: SeverityOK, Detail: model})
			}
		}
	} else if ManagedTTS(cfg) {
		diags = append(diags, Diagnostic{
			Name:     "tts-provider",
			Severity: SeverityOK,
			Detail:   "kokoro (" + KokoroPack() + ")",
		})
	} else {
		diags = append(diags, Diagnostic{
			Name:        "tts-provider",
			Severity:    SeverityWarn,
			Detail:      fmt.Sprintf("tts_provider %q is not a managed provider", cfg.TTSProvider),
			Remediation: "set tts_provider=kokoro or tts_provider=qwen3-tts with its native worker and model configured",
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
				Detail:   "voice_tools_enabled=true (list_files/read_file/write_file/run_command/web_search/fetch_url available)",
			})
		} else {
			diags = append(diags, Diagnostic{
				Name:        "voice-tools",
				Severity:    SeverityWarn,
				Detail:      "voice_tools_enabled=false — Ollama will not read or write files",
				Remediation: "set voice_tools_enabled=true in config, or VOICE_TOOLS_ENABLED=true",
			})
		}
		if cfg.SkillsEnabled {
			dir := cfg.SkillsDir
			if dir == "" {
				dir = SkillsDir()
			}
			diags = append(diags, Diagnostic{
				Name:     "skills",
				Severity: SeverityOK,
				Detail:   fmt.Sprintf("skills_enabled=true (semantic model %q; scan cwd/.agents/skills, nearest ancestor .agents/skills, ~/.agents/skills, then %s)", cfg.OllamaEmbeddingModel, dir),
			})
		} else {
			diags = append(diags, Diagnostic{
				Name:     "skills",
				Severity: SeverityOK,
				Detail:   "skills_enabled=false (Agent Skills not advertised to Ollama)",
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
			Remediation: platforminfo.PopplerInstallRemediation(runtime.GOOS),
		})
	} else {
		diags = append(diags, Diagnostic{Name: "pdftotext-binary", Severity: SeverityOK, Detail: "pdftotext"})
	}

	// Calibre is optional; always report availability as OK/Warn, never Error.
	// Production callers should pass a bundle-aware lookPath so the macOS app
	// bundle is found without PATH; tests inject fakes.
	calibreBin := strings.TrimSpace(cfg.CalibredbBinary)
	if calibreBin == "" {
		calibreBin = "calibredb"
	}
	if p, err := lookPath(calibreBin); err != nil {
		diags = append(diags, Diagnostic{
			Name:     "calibre-binary",
			Severity: SeverityWarn,
			Detail:   "calibredb not found (optional ebook catalog; Library TUI / library CLI / --from-library)",
			Remediation: platforminfo.CalibreInstallRemediation(runtime.GOOS) + ". " +
				platforminfo.CalibreBinaryHint(runtime.GOOS) + ".",
		})
	} else {
		detail := p
		if cfg.CalibreEnabled {
			detail = p + " (calibre_enabled=true)"
		} else {
			detail = p + " (found; enable with: samantha config calibre_enabled true — or press e in Library)"
		}
		diags = append(diags, Diagnostic{Name: "calibre-binary", Severity: SeverityOK, Detail: detail})
	}

	return diags
}

func diagnoseBrainProvider(cfg *Config, lookPath func(string) (string, error)) Diagnostic {
	provider := strings.ToLower(strings.TrimSpace(cfg.BrainProvider))
	if provider == "" {
		provider = "claude"
	}

	switch provider {
	case "claude", "grok":
		path, err := lookPath(provider)
		if err != nil {
			return Diagnostic{
				Name:        "brain-provider",
				Severity:    SeverityError,
				Detail:      fmt.Sprintf("%s CLI not found on PATH", provider),
				Remediation: fmt.Sprintf("install the %s CLI and ensure %q is on PATH, or select another brain_provider", provider, provider),
			}
		}
		return Diagnostic{
			Name:     "brain-provider",
			Severity: SeverityOK,
			Detail:   fmt.Sprintf("%s CLI (%s)", provider, path),
		}
	case "ollama":
		model := strings.TrimSpace(cfg.OllamaModel)
		if model == "" {
			return Diagnostic{
				Name:        "brain-provider",
				Severity:    SeverityError,
				Detail:      "ollama_model is not configured",
				Remediation: "run: samantha config ollama_model <model>",
			}
		}
		host := strings.TrimSpace(cfg.OllamaHost)
		parsed, err := url.Parse(host)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return Diagnostic{
				Name:        "brain-provider",
				Severity:    SeverityError,
				Detail:      fmt.Sprintf("invalid ollama_host %q", host),
				Remediation: "set ollama_host to an http:// or https:// URL",
			}
		}
		return Diagnostic{
			Name:     "brain-provider",
			Severity: SeverityOK,
			Detail:   fmt.Sprintf("ollama model %q at %s (connectivity is not probed by offline doctor)", model, host),
		}
	default:
		return Diagnostic{
			Name:        "brain-provider",
			Severity:    SeverityError,
			Detail:      fmt.Sprintf("unsupported brain_provider %q", cfg.BrainProvider),
			Remediation: "set brain_provider to claude, grok, or ollama",
		}
	}
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
