package qwen

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	ProviderName         = "qwen3-tts"
	PackageVersion       = "0.1.1"
	WorkerRevision       = "2"
	UVVersion            = "0.11.30"
	PythonVersion        = "3.12"
	DefaultModelID       = "Qwen/Qwen3-TTS-12Hz-0.6B-CustomVoice"
	DefaultModelRevision = "85e237c12c027371202489a0ec509ded67b5e4b5"
	DefaultVoice         = "Vivian"
	DefaultLanguage      = "Auto"
	managedSchema        = "samantha.qwen.install.v1"
	installerTimeout     = 30 * time.Minute
)

//go:embed worker.py
var workerSource []byte

// Voice is a model-native speaker bundled with the pinned CustomVoice model.
// The worker validates the selected value again before inference.
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
// model-native speaker name expected by the managed worker.
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

// CanonicalLanguage resolves a case-insensitive user/config value to the exact
// language label expected by the managed worker.
func CanonicalLanguage(value string) (string, bool) {
	value = strings.TrimSpace(value)
	for _, language := range supportedLanguages {
		if strings.EqualFold(language, value) {
			return language, true
		}
	}
	return "", false
}

// UseManaged reports whether Qwen configuration selects Samantha's managed
// runtime. qwen3-tts-cli with no model is the legacy persisted default from
// releases before managed setup and migrates to the same path automatically.
func UseManaged(binary, model string) bool {
	if strings.TrimSpace(model) != "" {
		return false
	}
	binary = strings.TrimSpace(binary)
	return binary == "" || strings.EqualFold(filepath.Base(binary), "qwen3-tts-cli")
}

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

func ManagedPaths(modelsDir string) Paths {
	root := filepath.Join(modelsDir, ProviderName)
	binDir := filepath.Join(root, "bin")
	venv := filepath.Join(root, "runtime", "qwen-tts-"+PackageVersion)
	python := filepath.Join(venv, "bin", "python")
	uv := filepath.Join(binDir, "uv")
	if runtime.GOOS == "windows" {
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
		Model:         filepath.Join(root, "models", "customvoice-0.6b", DefaultModelRevision),
		Marker:        filepath.Join(root, "install.json"),
		UVCache:       filepath.Join(root, "uv-cache"),
		PythonRoot:    filepath.Join(root, "python"),
		RuntimeMarker: filepath.Join(venv, ".qwen-tts-"+PackageVersion),
	}
}

type Status struct {
	Installed     bool   `json:"installed"`
	RuntimeReady  bool   `json:"runtime_ready"`
	ModelReady    bool   `json:"model_ready"`
	Root          string `json:"root"`
	Python        string `json:"python"`
	Worker        string `json:"worker"`
	Model         string `json:"model"`
	ModelID       string `json:"model_id"`
	ModelRevision string `json:"model_revision"`
	Detail        string `json:"detail,omitempty"`
}

type installMarker struct {
	Schema        string    `json:"schema"`
	Package       string    `json:"package"`
	Worker        string    `json:"worker"`
	ModelID       string    `json:"model_id"`
	ModelRevision string    `json:"model_revision"`
	InstalledAt   time.Time `json:"installed_at"`
}

func Inspect(modelsDir string) Status {
	p := ManagedPaths(modelsDir)
	status := Status{
		Root: p.Root, Python: p.Python, Worker: p.Worker, Model: p.Model,
		ModelID: DefaultModelID, ModelRevision: DefaultModelRevision,
	}
	status.RuntimeReady = regularFile(p.Python) && regularFile(p.Worker) && regularFile(p.RuntimeMarker)
	status.ModelReady = regularFile(filepath.Join(p.Model, "config.json")) &&
		regularFile(filepath.Join(p.Model, "model.safetensors")) &&
		regularFile(filepath.Join(p.Model, "speech_tokenizer", "model.safetensors"))

	data, err := os.ReadFile(p.Marker)
	if err != nil {
		status.Detail = "Qwen preset voices are not installed"
		return status
	}
	var marker installMarker
	if json.Unmarshal(data, &marker) != nil || marker.Schema != managedSchema ||
		marker.Package != PackageVersion || marker.Worker != WorkerRevision || marker.ModelID != DefaultModelID ||
		marker.ModelRevision != DefaultModelRevision {
		status.Detail = "Qwen installation metadata is stale; repair is required"
		return status
	}
	status.Installed = status.RuntimeReady && status.ModelReady
	if status.Installed {
		status.Detail = "Qwen preset voices installed"
	} else {
		status.Detail = "Qwen installation is incomplete; retry setup"
	}
	return status
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

type ProgressFunc func(stage string, pct float64)

// Ensure installs an isolated uv/Python runtime, the pinned official qwen-tts
// package, Samantha's worker, and the pinned CustomVoice model. It never changes
// shell profiles or writes outside modelsDir (apart from transient OS temp files).
func Ensure(ctx context.Context, modelsDir string, progress ProgressFunc) (Status, error) {
	if runtime.GOOS == "windows" {
		return Status{}, errors.New("managed Qwen setup currently supports macOS and Linux")
	}
	if strings.TrimSpace(modelsDir) == "" {
		return Status{}, errors.New("managed Qwen setup: models directory is empty")
	}
	if status := Inspect(modelsDir); status.Installed {
		if progress != nil {
			progress("Qwen preset voices", 100)
		}
		return status, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, installerTimeout)
	defer cancel()
	p := ManagedPaths(modelsDir)
	for _, dir := range []string{p.Root, p.BinDir, filepath.Dir(p.Worker), filepath.Dir(p.Model), p.UVCache, p.PythonRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Status{}, fmt.Errorf("managed Qwen setup: create %s: %w", dir, err)
		}
	}
	if progress != nil {
		progress("Qwen setup", 0)
	}

	if !regularFile(p.UV) {
		if progress != nil {
			progress("Qwen runtime manager", 5)
		}
		if err := installUV(ctx, p); err != nil {
			return Status{}, err
		}
	}
	if err := writeFileAtomic(p.Worker, workerSource, 0o600); err != nil {
		return Status{}, fmt.Errorf("managed Qwen setup: install worker: %w", err)
	}

	env := managedEnv(p)
	if !regularFile(p.Python) {
		if progress != nil {
			progress("Python 3.12 runtime", 15)
		}
		if err := run(ctx, env, p.UV, "venv", "--python", PythonVersion, "--managed-python", p.Venv); err != nil {
			return Status{}, fmt.Errorf("managed Qwen setup: create Python runtime: %w", err)
		}
	}

	if progress != nil {
		progress("qwen-tts runtime", 30)
	}
	if err := run(ctx, env, p.UV, "pip", "install", "--python", p.Python, "qwen-tts=="+PackageVersion); err != nil {
		return Status{}, fmt.Errorf("managed Qwen setup: install qwen-tts: %w", err)
	}
	if err := writeFileAtomic(p.RuntimeMarker, []byte(PackageVersion+"\n"), 0o600); err != nil {
		return Status{}, fmt.Errorf("managed Qwen setup: mark qwen-tts runtime: %w", err)
	}

	if progress != nil {
		progress("Qwen CustomVoice model", 55)
	}
	if err := run(ctx, env, p.Python, p.Worker, "download",
		"--model-id", DefaultModelID,
		"--revision", DefaultModelRevision,
		"--model-dir", p.Model,
	); err != nil {
		return Status{}, fmt.Errorf("managed Qwen setup: download model: %w", err)
	}
	if progress != nil {
		progress("Qwen model verification", 90)
	}
	// Load the pinned model and ask the official API for its capabilities before
	// marking setup complete. This catches incompatible hardware, runtime, and
	// model revisions while the user's prior provider is still active.
	if err := run(ctx, env, p.Python, p.Worker, "capabilities", "--model", p.Model); err != nil {
		return Status{}, fmt.Errorf("managed Qwen setup: verify model: %w", err)
	}

	marker := installMarker{
		Schema: managedSchema, Package: PackageVersion, Worker: WorkerRevision, ModelID: DefaultModelID,
		ModelRevision: DefaultModelRevision, InstalledAt: time.Now().UTC(),
	}
	markerData, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return Status{}, err
	}
	markerData = append(markerData, '\n')
	if err := writeFileAtomic(p.Marker, markerData, 0o600); err != nil {
		return Status{}, fmt.Errorf("managed Qwen setup: write install marker: %w", err)
	}
	status := Inspect(modelsDir)
	if !status.Installed {
		return status, errors.New(status.Detail)
	}
	if progress != nil {
		progress("Qwen preset voices", 100)
	}
	return status, nil
}

func managedEnv(p Paths) []string {
	return append(os.Environ(),
		"UV_CACHE_DIR="+p.UVCache,
		"UV_PYTHON_INSTALL_DIR="+p.PythonRoot,
		"UV_MANAGED_PYTHON=1",
		"UV_NO_MODIFY_PATH=1",
		"UV_NO_CONFIG=1",
		"HF_HUB_DISABLE_TELEMETRY=1",
		"JOBLIB_MULTIPROCESSING=0",
	)
}

func installUV(ctx context.Context, p Paths) error {
	url := fmt.Sprintf("https://astral.sh/uv/%s/install.sh", UVVersion)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("managed Qwen setup: download uv installer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("managed Qwen setup: download uv installer: HTTP %d", resp.StatusCode)
	}
	script, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return fmt.Errorf("managed Qwen setup: read uv installer: %w", err)
	}
	if len(script) == 0 || len(script) >= 2<<20 {
		return errors.New("managed Qwen setup: invalid uv installer response")
	}
	tmp, err := os.CreateTemp(p.Root, ".uv-install-*.sh")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(script); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	env := append(os.Environ(), "UV_UNMANAGED_INSTALL="+p.BinDir, "UV_NO_MODIFY_PATH=1")
	if err := run(ctx, env, "sh", name); err != nil {
		return fmt.Errorf("managed Qwen setup: install uv: %w", err)
	}
	if !regularFile(p.UV) {
		return fmt.Errorf("managed Qwen setup: uv installer did not create %s", p.UV)
	}
	return nil
}

func run(ctx context.Context, env []string, binary string, args ...string) error {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = env
	var output limitedOutput
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(output.String())
		if detail != "" {
			return fmt.Errorf("%w: %s", err, detail)
		}
		return err
	}
	return nil
}

type limitedOutput struct{ strings.Builder }

func (b *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	const limit = 32 << 10
	if b.Len() < limit {
		remaining := limit - b.Len()
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Builder.Write(p)
	}
	return n, nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".install-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
