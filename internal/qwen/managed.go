// Package qwen holds product helpers for Qwen3-TTS: preset registry and managed
// selection (empty binary/model → Samantha-owned native assets).
//
// Product inference is native-only (models_dir/qwen3-tts worker + GGUF).
// There is no uv/Python ensure, embedded worker, or legacy-tree migrator.
// If an old Python tree is still under models/qwen3-tts, delete that directory
// and re-run models ensure --tts.
package qwen

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

const (
	ProviderName = "qwen3-tts"
	DefaultVoice = "Vivian"
	// DefaultLanguage is the product default for CustomVoice-class presets.
	DefaultLanguage = "Auto"
)

// Voice is a CustomVoice-class preset name shared by native .q3te embeds.
type Voice struct {
	Name           string
	NativeLanguage string
	Description    string
}

var customVoices = []Voice{
	{Name: "Vivian", NativeLanguage: "Chinese", Description: "Bright young female voice"},
	{Name: "Serena", NativeLanguage: "Chinese", Description: "Warm, gentle young female voice"},
	{Name: "Uncle_Fu", NativeLanguage: "Chinese", Description: "Low, mellow seasoned male voice"},
	{Name: "Dylan", NativeLanguage: "Chinese", Description: "Clear, youthful Beijing male voice"},
	{Name: "Eric", NativeLanguage: "Chinese", Description: "Lively Chengdu male voice"},
	{Name: "Ryan", NativeLanguage: "English", Description: "Dynamic English male voice"},
	{Name: "Aiden", NativeLanguage: "English", Description: "Clear American male voice"},
	{Name: "Ono_Anna", NativeLanguage: "Japanese", Description: "Playful Japanese female voice"},
	{Name: "Sohee", NativeLanguage: "Korean", Description: "Warm Korean female voice"},
}

var supportedLanguages = []string{
	"Auto", "Chinese", "English", "Japanese", "Korean", "German",
	"French", "Russian", "Portuguese", "Spanish", "Italian",
}

func CustomVoices() []Voice {
	out := make([]Voice, len(customVoices))
	copy(out, customVoices)
	return out
}

// CanonicalVoice resolves a case-insensitive user/config value to the exact
// preset name used by native workers and Settings.
func CanonicalVoice(value string) (string, bool) {
	value = strings.TrimSpace(value)
	for _, voice := range customVoices {
		if strings.EqualFold(voice.Name, value) {
			return voice.Name, true
		}
	}
	return "", false
}

func SupportedLanguages() []string {
	out := make([]string, len(supportedLanguages))
	copy(out, supportedLanguages)
	return out
}

// CanonicalLanguage resolves a case-insensitive language label.
func CanonicalLanguage(value string) (string, bool) {
	value = strings.TrimSpace(value)
	for _, language := range supportedLanguages {
		if strings.EqualFold(language, value) {
			return language, true
		}
	}
	return "", false
}

// UseManaged reports whether configuration selects Samantha-managed assets
// (empty binary/model). Product resolution then uses the native package under
// models_dir/qwen3-tts — never a Python runtime.
//
// qwen3-tts-cli with no model is a legacy persisted config default and resolves
// to the same managed-native path.
func UseManaged(binary, model string) bool {
	if strings.TrimSpace(model) != "" {
		return false
	}
	binary = strings.TrimSpace(binary)
	return binary == "" || strings.EqualFold(filepath.Base(binary), "qwen3-tts-cli")
}

// Status is a product readiness view for Settings/doctor (native-only).
type Status struct {
	Installed    bool   `json:"installed"`
	RuntimeReady bool   `json:"runtime_ready"`
	ModelReady   bool   `json:"model_ready"`
	Root         string `json:"root"`
	Worker       string `json:"worker"`
	Model        string `json:"model"`
	Detail       string `json:"detail,omitempty"`
}

// ProgressFunc reports coarse install stages (0–100).
type ProgressFunc func(stage string, pct float64)

// Inspect reports product readiness for the native package only.
func Inspect(modelsDir string) Status {
	ns := InspectNative(modelsDir, DefaultModelTier)
	if ns.Installed {
		return Status{
			Installed:    true,
			RuntimeReady: true,
			ModelReady:   true,
			Root:         ns.Root,
			Worker:       ns.Worker,
			Model:        ns.ModelDir,
			Detail:       ns.Detail,
		}
	}
	return Status{
		Root:   NativeInstallPaths(modelsDir).Root,
		Detail: "native Qwen3-TTS package is not installed; set qwen_tts_native_url (or SAMANTHA_QWEN_NATIVE_URL) and run models ensure --tts",
	}
}

// Ensure installs the product native package only (no Python/uv).
// Requires qwen_tts_native_url or SAMANTHA_QWEN_NATIVE_URL when not already installed.
func Ensure(ctx context.Context, modelsDir string, progress ProgressFunc) (Status, error) {
	if strings.TrimSpace(modelsDir) == "" {
		return Status{}, errors.New("qwen ensure: models directory is empty")
	}
	if st := Inspect(modelsDir); st.Installed {
		if progress != nil {
			progress("native Qwen3-TTS", 100)
		}
		return st, nil
	}
	ns, err := EnsureNative(ctx, modelsDir, NativeEnsureOptions{
		URL:    ResolveNativeURL(""),
		SHA256: ResolveNativeSHA256(""),
		Tier:   DefaultModelTier,
	}, progress)
	if err != nil {
		return Inspect(modelsDir), fmt.Errorf("native Qwen ensure: %w", err)
	}
	return Status{
		Installed:    ns.Installed,
		RuntimeReady: ns.WorkerReady,
		ModelReady:   ns.ModelReady,
		Root:         ns.Root,
		Worker:       ns.Worker,
		Model:        ns.ModelDir,
		Detail:       ns.Detail,
	}, nil
}
