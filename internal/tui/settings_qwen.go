package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/config"
	managedqwen "github.com/lancekrogers/samantha/internal/qwen"
	"github.com/lancekrogers/samantha/internal/tts"
)

// Qwen Settings rows: tier, clone consent, native package, embedding cache.
type qwenOptionKind int

const (
	qwenOptStatus qwenOptionKind = iota
	qwenOptTier
	qwenOptConsent
	qwenOptCache
	qwenOptInstall
)

type qwenOptionItem struct {
	kind  qwenOptionKind
	label string
}

func (m *settingsModel) refreshQwenStatus() {
	modelsDir := config.ModelsDirFrom(m.cfg)
	m.qwenStatus = managedqwen.Inspect(modelsDir)
	m.nativeStatus = managedqwen.InspectNative(modelsDir, m.cfg.QwenTTSModelTier)
}

func (m *settingsModel) buildQwenItems() {
	m.qwenItems = nil
	if m.cfg == nil {
		return
	}
	m.refreshQwenStatus()

	tier := strings.TrimSpace(m.cfg.QwenTTSModelTier)
	if tier == "" {
		tier = managedqwen.DefaultModelTier
	}
	tierDetail := tier
	if m.nativeStatus.Installed {
		if m.nativeStatus.ModelReady {
			tierDetail += " · ready"
		} else {
			tierDetail += " · not in package"
		}
		if len(m.nativeStatus.TiersReady) > 0 {
			tierDetail += " · available: " + strings.Join(m.nativeStatus.TiersReady, ", ")
		}
	}

	status := "native package not installed"
	if m.nativeStatus.Installed {
		status = fmt.Sprintf("native ready · worker · tier %s", m.nativeStatus.DefaultTier)
		if m.nativeStatus.EngineSHA != "" {
			eng := m.nativeStatus.EngineSHA
			if len(eng) > 12 {
				eng = eng[:12]
			}
			status += " · eng " + eng
		}
	} else if m.qwenStatus.Installed {
		status = "legacy managed Python runtime installed (prefer native tarball)"
	}

	consent := "OFF"
	if m.cfg.QwenTTSConsent {
		consent = "ON ✓"
	}

	cachePath := managedqwen.NativeInstallPaths(config.ModelsDirFrom(m.cfg)).CacheDir
	cacheN := countEmbeddingCache(cachePath)
	cacheLabel := fmt.Sprintf("Embedding cache — %d file(s) · %s", cacheN, cachePath)

	m.qwenItems = []qwenOptionItem{
		{kind: qwenOptStatus, label: "Package — " + status},
		{kind: qwenOptTier, label: "Model tier — " + tierDetail + " · enter to cycle"},
		{kind: qwenOptConsent, label: "Clone consent — " + consent + " · required for reference voice"},
		{kind: qwenOptCache, label: cacheLabel + " · enter clears"},
	}
	if !m.nativeStatus.Installed {
		install := "Install / repair — native package (ensure) or legacy managed"
		if m.qwenInstalling {
			install = "Installing…"
		}
		m.qwenItems = append(m.qwenItems, qwenOptionItem{kind: qwenOptInstall, label: install})
	} else {
		m.qwenItems = append(m.qwenItems, qwenOptionItem{
			kind:  qwenOptInstall,
			label: "Re-run ensure — verify native package / download if URL set",
		})
	}
}

func countEmbeddingCache(dir string) int {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		n++
	}
	return n
}

func clearEmbeddingCache(dir string) (int, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (m *settingsModel) selectQwenItem() tea.Cmd {
	if m.cursor >= len(m.qwenItems) {
		return nil
	}
	item := m.qwenItems[m.cursor]
	save := m.saveConfig
	if save == nil {
		save = config.SetAndSave
	}

	switch item.kind {
	case qwenOptStatus:
		m.message = m.nativeStatus.Detail
		if m.nativeStatus.Installed {
			m.message = "Native package: " + m.nativeStatus.Root
		}
		return nil

	case qwenOptTier:
		cur := strings.TrimSpace(m.cfg.QwenTTSModelTier)
		if cur == "" {
			cur = managedqwen.DefaultModelTier
		}
		next := managedqwen.DefaultModelTier
		if cur == managedqwen.DefaultModelTier || cur == "0.6b" {
			next = managedqwen.Tier1_7B
		}
		// Fail closed if 1.7B requested but not present in the installed package.
		if next == managedqwen.Tier1_7B {
			st := managedqwen.InspectNative(config.ModelsDirFrom(m.cfg), next)
			if !st.ModelReady {
				m.message = "1.7B is not in the installed native package — install a multi-tier release or keep 0.6b"
				return nil
			}
		}
		if err := save("qwen_tts_model_tier", next); err != nil {
			m.message = fmt.Sprintf("Failed to save tier: %v", err)
			return nil
		}
		m.cfg.QwenTTSModelTier = next
		m.buildQwenItems()
		// Worker is started with QWEN3_TTS_TIER; warm process must be recreated.
		m.message = fmt.Sprintf("Qwen model tier set to %s — restart voice / new conversation to apply", next)
		return nil

	case qwenOptConsent:
		next := !m.cfg.QwenTTSConsent
		if err := save("qwen_tts_consent", next); err != nil {
			m.message = fmt.Sprintf("Failed to save consent: %v", err)
			return nil
		}
		m.cfg.QwenTTSConsent = next
		m.buildQwenItems()
		if next {
			m.message = "Clone consent ON — reference/clone workflows allowed when supported"
		} else {
			m.message = "Clone consent OFF — reference voice use blocked"
		}
		return nil

	case qwenOptCache:
		cache := managedqwen.NativeInstallPaths(config.ModelsDirFrom(m.cfg)).CacheDir
		n, err := clearEmbeddingCache(cache)
		if err != nil {
			m.message = fmt.Sprintf("Failed to clear cache: %v", err)
			return nil
		}
		m.buildQwenItems()
		m.message = fmt.Sprintf("Cleared %d embedding cache file(s) under %s", n, cache)
		return nil

	case qwenOptInstall:
		if m.qwenInstalling {
			return nil
		}
		ctx, cancel := context.WithCancel(context.Background())
		m.qwenInstallCancel = cancel
		m.qwenInstalling = true
		m.qwenInstallEvents = newEventBridge(16)
		m.message = "Installing native Qwen3-TTS package (large download)…"
		m.buildQwenItems()
		return tea.Batch(m.qwenInstallEvents.wait(), m.installQwenAssets(ctx))
	}
	return nil
}

// installQwenAssets runs models ensure --tts for the native package and streams
// progress into the Settings status line via qwenInstallProgressMsg.
func (m settingsModel) installQwenAssets(ctx context.Context) tea.Cmd {
	ensureAssets := m.ensureTTSAssets
	if ensureAssets == nil {
		ensureAssets = func(ctx context.Context, cfg *config.Config, onProgress func(string, float64)) error {
			return config.EnsureRuntimeAssets(ctx, cfg, config.AssetRequest{NeedTTS: true}, onProgress)
		}
	}
	events := m.qwenInstallEvents
	cfgCopy := *m.cfg
	// Ensure path keys off qwen3-tts provider.
	cfgCopy.TTSProvider = managedqwen.ProviderName
	return func() tea.Msg {
		progress := func(name string, pct float64) {
			if events == nil {
				return
			}
			events.send(qwenInstallProgressMsg{stage: name, pct: pct})
		}
		progress("starting download", 1)
		err := ensureAssets(ctx, &cfgCopy, progress)
		if events != nil {
			if err == nil {
				events.send(qwenInstallProgressMsg{stage: "complete", pct: 100})
			}
			events.send(qwenInstallProgressClosedMsg{})
		}
		st := managedqwen.InspectNative(config.ModelsDirFrom(&cfgCopy), cfgCopy.QwenTTSModelTier)
		status := managedqwen.Inspect(config.ModelsDirFrom(&cfgCopy))
		return qwenInstallDoneMsg{status: status, native: st, err: err}
	}
}

// qwenUsesPresets reports whether Settings should list CustomVoice-class presets.
func qwenUsesPresets(cfg *config.Config, native managedqwen.NativeStatus, managed managedqwen.Status) bool {
	if cfg == nil {
		return false
	}
	if !strings.EqualFold(activeTTSProvider(cfg), managedqwen.ProviderName) {
		return false
	}
	// Explicit external binary/model: no preset browser.
	if !managedqwen.UseManaged(cfg.QwenTTSBinary, cfg.QwenTTSModel) {
		return false
	}
	// Presets only when native package is installed (no legacy Python product path).
	return native.Installed
}

func qwenPresetVoices(cfg *config.Config, native managedqwen.NativeStatus) []tts.Voice {
	if native.Installed {
		if names := loadNativePresetNames(native.ModelDir); len(names) > 0 {
			out := make([]tts.Voice, 0, len(names))
			for _, name := range names {
				desc := name
				loc := "multi"
				for _, v := range managedqwen.CustomVoices() {
					if strings.EqualFold(v.Name, name) {
						if v.Description != "" {
							desc = v.Description
						}
						name = v.Name
						loc = v.NativeLanguage
						break
					}
				}
				out = append(out, tts.Voice{Name: name, FriendlyName: desc, Gender: "preset", Locale: loc})
			}
			return out
		}
	}
	out := make([]tts.Voice, 0, len(managedqwen.CustomVoices()))
	for _, voice := range managedqwen.CustomVoices() {
		out = append(out, tts.Voice{
			Name: voice.Name, FriendlyName: voice.Description,
			Gender: "preset", Locale: voice.NativeLanguage,
		})
	}
	return out
}

func loadNativePresetNames(modelDir string) []string {
	var names []string
	for _, v := range managedqwen.CustomVoices() {
		q3te := filepath.Join(modelDir, "presets", v.Name+".q3te")
		if _, err := os.Stat(q3te); err == nil {
			names = append(names, v.Name)
		}
	}
	return names
}
