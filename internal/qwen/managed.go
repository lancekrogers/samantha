// Package qwen holds product helpers for Qwen3-TTS: preset registry, managed
// selection (empty binary/model → Samantha-owned assets), and legacy detection.
//
// Product inference is native-only (models_dir/qwen3-tts worker + GGUF).
// There is no uv/Python ensure or embedded worker.py on the product path.
package qwen

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	ProviderName = "qwen3-tts"
	DefaultVoice = "Vivian"
	// DefaultLanguage is the product default for CustomVoice-class presets.
	DefaultLanguage = "Auto"
	// Legacy identifiers kept for diagnostics only (no longer installed).
	PackageVersion = "0.1.1" // last managed Python package pin (historical)
)

// Voice is a CustomVoice-class preset name shared by native .q3te embeds
// and historical managed installs.
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
// qwen3-tts-cli with no model is a legacy persisted default and migrates to
// the same managed-native path.
func UseManaged(binary, model string) bool {
	if strings.TrimSpace(model) != "" {
		return false
	}
	binary = strings.TrimSpace(binary)
	return binary == "" || strings.EqualFold(filepath.Base(binary), "qwen3-tts-cli")
}

// Paths describes the historical Python install layout (for migration only).
type Paths struct {
	Root          string
	BinDir        string
	UV            string
	Venv          string
	Python        string
	Worker        string
	Model         string
	Marker        string
	UVCache       string
	PythonRoot    string
	RuntimeMarker string
}

// ManagedPaths returns the legacy Python tree layout under modelsDir/qwen3-tts
// (uv venv + worker.py). Product installs no longer create this tree.
func ManagedPaths(modelsDir string) Paths {
	root := filepath.Join(modelsDir, ProviderName)
	binDir := filepath.Join(root, "bin")
	venv := filepath.Join(root, "runtime", "qwen-tts-"+PackageVersion)
	python := filepath.Join(venv, "bin", "python")
	uv := filepath.Join(binDir, "uv")
	if fileExists(filepath.Join(venv, "Scripts", "python.exe")) {
		python = filepath.Join(venv, "Scripts", "python.exe")
		uv += ".exe"
	}
	return Paths{
		Root:          root,
		BinDir:        binDir,
		UV:            uv,
		Venv:          venv,
		Python:        python,
		Worker:        filepath.Join(root, "worker", "qwen_worker.py"),
		Model:         filepath.Join(root, "models", "customvoice-0.6b"),
		Marker:        filepath.Join(root, "install.json"),
		UVCache:       filepath.Join(root, "uv-cache"),
		PythonRoot:    filepath.Join(root, "python"),
		RuntimeMarker: filepath.Join(venv, ".qwen-tts-"+PackageVersion),
	}
}

// Status is a product readiness view for Settings/doctor (native-first).
type Status struct {
	Installed     bool   `json:"installed"`
	RuntimeReady  bool   `json:"runtime_ready"`
	ModelReady    bool   `json:"model_ready"`
	Root          string `json:"root"`
	Python        string `json:"python,omitempty"` // always empty after cutover
	Worker        string `json:"worker"`
	Model         string `json:"model"`
	ModelID       string `json:"model_id,omitempty"`
	ModelRevision string `json:"model_revision,omitempty"`
	Detail        string `json:"detail,omitempty"`
	LegacyPython  bool   `json:"legacy_python,omitempty"`
}

// ProgressFunc reports coarse install stages (0–100).
type ProgressFunc func(stage string, pct float64)

// Inspect reports product readiness: native package installed, or legacy
// Python tree present (not product-ready).
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
	if leg := DetectLegacyPython(modelsDir); leg.Present {
		p := ManagedPaths(modelsDir)
		return Status{
			Installed:    false,
			RuntimeReady: leg.RuntimePresent,
			ModelReady:   leg.ModelPresent,
			Root:         p.Root,
			Worker:       p.Worker,
			Model:        p.Model,
			LegacyPython: true,
			Detail:       leg.Detail,
		}
	}
	return Status{
		Root:   NativeInstallPaths(modelsDir).Root,
		Detail: "native Qwen3-TTS package is not installed; set qwen_tts_native_url and run models ensure --tts",
	}
}

// Ensure installs the product native package only (no Python/uv).
// Requires QWEN3_TTS_NATIVE_URL / qwen_tts_native_url when not already installed.
func Ensure(ctx context.Context, modelsDir string, progress ProgressFunc) (Status, error) {
	if strings.TrimSpace(modelsDir) == "" {
		return Status{}, errors.New("qwen ensure: models directory is empty")
	}
	if st := Inspect(modelsDir); st.Installed && !st.LegacyPython {
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

// LegacyPythonInstall describes a pre-cutover uv/torch tree.
type LegacyPythonInstall struct {
	Present        bool
	Root           string
	RuntimePresent bool
	ModelPresent   bool
	Detail         string
}

// DetectLegacyPython finds old managed Python installs under modelsDir/qwen3-tts.
func DetectLegacyPython(modelsDir string) LegacyPythonInstall {
	p := ManagedPaths(modelsDir)
	workerPy := p.Worker
	// Also detect historical path names.
	candidates := []string{
		workerPy,
		filepath.Join(p.Root, "worker", "worker.py"),
		filepath.Join(p.Root, "worker.py"),
	}
	hasWorker := false
	for _, c := range candidates {
		if regularFile(c) {
			hasWorker = true
			workerPy = c
			break
		}
	}
	hasPython := regularFile(p.Python) || dirExists(p.Venv)
	hasUV := regularFile(p.UV)
	modelPresent := regularFile(filepath.Join(p.Model, "config.json")) ||
		dirExists(filepath.Join(p.Root, "models", "customvoice-0.6b"))
	if !hasWorker && !hasPython && !hasUV && !modelPresent {
		return LegacyPythonInstall{}
	}
	detail := "legacy Python/uv Qwen install detected under " + p.Root +
		" — product path is native-only; run models ensure --tts with qwen_tts_native_url, then remove the legacy tree (doctor can quarantine)"
	return LegacyPythonInstall{
		Present:        true,
		Root:           p.Root,
		RuntimePresent: hasPython || hasUV,
		ModelPresent:   modelPresent,
		Detail:         detail,
	}
}

// QuarantineLegacyPython renames the legacy Python tree out of the way so it
// cannot be used as a product runtime. Native package under the same root's
// sibling layout (qwen3-tts with bin/worker + models/*.gguf) is left intact
// when already native-shaped.
func QuarantineLegacyPython(modelsDir string) (string, error) {
	leg := DetectLegacyPython(modelsDir)
	if !leg.Present {
		return "", nil
	}
	// If root already looks like a native package, only remove python/worker/runtime subtrees.
	ns := InspectNative(modelsDir, DefaultModelTier)
	if ns.Installed {
		p := ManagedPaths(modelsDir)
		for _, sub := range []string{p.Venv, p.UVCache, p.PythonRoot, filepath.Dir(p.Worker), p.UV} {
			_ = os.RemoveAll(sub)
		}
		// Remove worker.py if nested under native root
		_ = os.Remove(p.Worker)
		return p.Root + " (python subtrees removed; native package kept)", nil
	}
	// Full tree is legacy: move aside.
	src := leg.Root
	dst := src + ".legacy-python-quarantine"
	if err := os.Rename(src, dst); err != nil {
		return "", fmt.Errorf("quarantine legacy python tree: %w", err)
	}
	return dst, nil
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
