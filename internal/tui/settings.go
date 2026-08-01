package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/discovery"
	"github.com/lancekrogers/samantha/internal/meeting"
	managedqwen "github.com/lancekrogers/samantha/internal/qwen"
	"github.com/lancekrogers/samantha/internal/tts"
)

type settingsSection int

const (
	sectionProvider settingsSection = iota
	sectionModel
	sectionTools
	sectionTTS
	sectionQwen
	sectionVoice
	sectionLanguage
	sectionSpeakers
	sectionInput
	sectionOutput
	sectionMeeting
	settingsSectionCount
)

type settingsModel struct {
	cfg       *config.Config
	providers []discovery.ProviderInfo

	section settingsSection
	cursor  int
	width   int
	height  int
	offset  int

	// Derived lists for current section.
	providerItems  []string
	modelItems     []string
	toolItems      []string
	ttsItems       []ttsSettingItem
	qwenItems      []qwenOptionItem
	voiceItems     []tts.Voice
	languageItems  []string
	inputItems     []string
	outputItems    []string
	devicesLoading bool
	deviceChecker  config.VoiceDeviceChecker

	// Meeting route discovery (camp list + config).
	routeDests        []meeting.Destination
	routeDestsLoading bool
	routeDestsErr     string
	routeDestsSeq     int

	// Preview playback state.
	previewing       string
	previewID        int64
	previewCancel    context.CancelFunc
	previewPlayer    audio.Engine
	newPreviewPlayer func() audio.Engine
	// ensureTTSAssets installs TTS assets; onProgress may be nil (preview) or
	// feed the Settings install progress bridge.
	ensureTTSAssets  func(ctx context.Context, cfg *config.Config, onProgress func(name string, pct float64)) error
	newTTSProvider   func(*config.Config) (tts.Provider, func(), error)
	saveConfig       func(string, any) error
	message          string

	qwenStatus        managedqwen.Status
	nativeStatus      managedqwen.NativeStatus
	qwenInstalling    bool
	qwenInstallCancel context.CancelFunc
	qwenInstallEvents *eventBridge
	ensureQwen        func(context.Context, string, managedqwen.ProgressFunc) (managedqwen.Status, error)
}

func newSettings(cfg *config.Config, providers []discovery.ProviderInfo) settingsModel {
	m := settingsModel{
		cfg:       cfg,
		providers: providers,
		newPreviewPlayer: func() audio.Engine {
			return audio.NewPlayerWithDevice(cfg.OutputDevice)
		},
		deviceChecker: audio.NewDeviceChecker(),
		ensureTTSAssets: func(ctx context.Context, cfg *config.Config, onProgress func(name string, pct float64)) error {
			return config.EnsureRuntimeAssets(ctx, cfg, config.AssetRequest{NeedTTS: true}, onProgress)
		},
		newTTSProvider: tts.NewProvider,
		saveConfig:     config.SetAndSave,
		ensureQwen:     managedqwen.Ensure,
	}
	m.refreshQwenStatus()
	m.buildProviderItems()
	m.buildModelItems()
	m.buildToolItems()
	m.buildTTSItems()
	m.buildQwenItems()
	m.buildVoiceItems()
	m.buildLanguageItems()
	m.inputItems = []string{""}
	m.outputItems = []string{""}
	return m
}

type deviceListsMsg struct {
	inputs  []string
	outputs []string
	err     error
}

func (m *settingsModel) loadDevices() tea.Cmd {
	m.devicesLoading = true
	checker := m.deviceChecker
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		inputs, err := checker.CaptureDevices(ctx)
		if err != nil {
			return deviceListsMsg{err: err}
		}
		outputs, err := checker.PlaybackDevices(ctx)
		return deviceListsMsg{inputs: inputs, outputs: outputs, err: err}
	}
}

func (m *settingsModel) buildProviderItems() {
	m.providerItems = nil
	for _, p := range m.providers {
		label := p.Name
		if !p.Available {
			label += " (not available)"
		}
		m.providerItems = append(m.providerItems, label)
	}
}

func (m *settingsModel) buildModelItems() {
	m.modelItems = nil
	for _, p := range m.providers {
		if p.Name == m.cfg.BrainProvider {
			m.modelItems = p.Models
			break
		}
	}
	if len(m.modelItems) == 0 {
		m.modelItems = []string{"default"}
	}
}

func (m *settingsModel) buildToolItems() {
	m.toolItems = []string{
		fmt.Sprintf("Local tools — %s", enabledLabel(m.cfg.VoiceToolsEnabled)),
	}
	// Agent Skills discovery via SKILL.md is Ollama-only; Claude/Grok use CLIs.
	if strings.EqualFold(m.cfg.BrainProvider, "ollama") {
		m.toolItems = append(m.toolItems,
			fmt.Sprintf("Agent Skills (Ollama) — %s", enabledLabel(m.cfg.SkillsEnabled)),
		)
	} else {
		m.toolItems = append(m.toolItems, "Agent Skills — n/a (Ollama only)")
	}
}

func enabledLabel(enabled bool) string {
	if enabled {
		return "ON ✓"
	}
	return "OFF"
}

type ttsSettingItem struct {
	provider string
	detail   string
}

func (m *settingsModel) buildTTSItems() {
	m.ttsItems = nil
	for _, spec := range tts.Providers() {
		detail := ttsProviderDetail(spec, m.cfg)
		if spec.Name == managedqwen.ProviderName {
			managed := managedqwen.UseManaged(m.cfg.QwenTTSBinary, m.cfg.QwenTTSModel)
			switch {
			case m.qwenInstalling:
				detail = "installing Qwen assets…"
			case managed && m.nativeStatus.Installed:
				detail = fmt.Sprintf("native worker · tier %s · presets ready", m.nativeStatus.DefaultTier)
			case managed:
				detail = "native package not installed · open Qwen tab or enter to install"
			}
		}
		m.ttsItems = append(m.ttsItems, ttsSettingItem{
			provider: spec.Name,
			detail:   detail,
		})
	}
}

func (m *settingsModel) buildVoiceItems() {
	m.voiceItems = nil
	if qwenUsesPresets(m.cfg, m.nativeStatus, m.qwenStatus) {
		m.voiceItems = append(m.voiceItems, qwenPresetVoices(m.cfg, m.nativeStatus)...)
		return
	}
	voices, err := tts.StaticVoices(m.cfg.TTSProvider, "", "")
	if err != nil {
		return
	}
	m.voiceItems = append(m.voiceItems, voices...)
}

func (m *settingsModel) buildLanguageItems() {
	m.languageItems = nil
	// Language list is available whenever managed/native Qwen selection is active
	// (empty binary/model), even before install completes.
	if m.cfg != nil &&
		strings.EqualFold(activeTTSProvider(m.cfg), managedqwen.ProviderName) &&
		managedqwen.UseManaged(m.cfg.QwenTTSBinary, m.cfg.QwenTTSModel) {
		m.languageItems = managedqwen.SupportedLanguages()
	}
}

func (m settingsModel) activeModel() string {
	switch m.cfg.BrainProvider {
	case "ollama":
		if m.cfg.OllamaModel != "" {
			return m.cfg.OllamaModel
		}
	case "grok":
		if m.cfg.GrokModel != "" {
			return m.cfg.GrokModel
		}
	}
	return "default"
}

func (m settingsModel) Update(msg tea.Msg) (settingsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ensureCursorVisible()

	case tea.KeyMsg:
		switch msg.String() {
		case "tab", "right", "l":
			m.section = (m.section + 1) % settingsSectionCount
			m.cursor = 0
			m.offset = 0
			m.message = ""
			if m.section == sectionMeeting && !m.routeDestsLoading && m.routeDests == nil {
				return m, m.loadRouteDestinations()
			}
		case "shift+tab", "left", "h":
			if m.section > 0 {
				m.section--
			} else {
				m.section = settingsSectionCount - 1
			}
			m.cursor = 0
			m.offset = 0
			m.message = ""
			if m.section == sectionMeeting && !m.routeDestsLoading && m.routeDests == nil {
				return m, m.loadRouteDestinations()
			}
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			max := m.currentListLen() - 1
			if m.cursor < max {
				m.cursor++
			}
		case "enter":
			if m.section == sectionMeeting && m.cursor == meetingRowRefresh {
				m.selectMeetingItem()
				return m, m.loadRouteDestinations()
			}
			return m, m.selectCurrent()
		case "p":
			if m.section == sectionVoice && m.cursor < len(m.voiceItems) {
				m.cancelPreview()
				voice := m.voiceItems[m.cursor]
				m.previewing = voice.Name
				m.previewID++
				ctx, cancel := context.WithCancel(context.Background())
				m.previewCancel = cancel
				player := m.playerForPreview()
				return m, m.previewVoice(ctx, m.previewID, voice, player)
			}
		case "esc", "q":
			m.cancelPreview()
			return m, func() tea.Msg { return settingsDoneMsg{} }
		}
		m.ensureCursorVisible()

	case voicePreviewDoneMsg:
		// Ignore completions from a preview that's already been superseded.
		if msg.id == m.previewID && msg.voice == m.previewing {
			m.previewing = ""
			m.previewCancel = nil
			if msg.message != "" {
				m.message = msg.message
			}
		}

	case qwenInstallDoneMsg:
		m.qwenInstalling = false
		m.qwenInstallCancel = nil
		if msg.err != nil {
			m.message = fmt.Sprintf("Qwen setup failed: %v", msg.err)
			m.buildTTSItems()
			m.buildQwenItems()
			break
		}
		m.qwenStatus = msg.status
		if msg.native.Root != "" {
			m.nativeStatus = msg.native
		} else {
			m.refreshQwenStatus()
		}
		if err := m.activateQwenAfterInstall(); err != nil {
			m.message = fmt.Sprintf("Qwen installed but configuration could not be saved: %v", err)
			m.buildTTSItems()
			m.buildQwenItems()
			break
		}
		m.buildTTSItems()
		m.buildQwenItems()
		m.buildVoiceItems()
		m.buildLanguageItems()
		if m.nativeStatus.Installed {
			m.message = "Native Qwen package ready; open Voice for presets, Qwen tab for tier/consent/cache"
		} else {
			m.message = "Qwen activated; open Settings → Qwen → Install package (or run models ensure --tts)"
		}

	case qwenInstallProgressMsg:
		if !m.qwenInstalling {
			break
		}
		if msg.pct > 0 {
			m.message = fmt.Sprintf("Qwen setup: %s (%d%%)", msg.stage, int(msg.pct))
		} else {
			m.message = fmt.Sprintf("Qwen setup: %s…", msg.stage)
		}
		if m.qwenInstallEvents != nil {
			return m, m.qwenInstallEvents.wait()
		}

	case qwenInstallProgressClosedMsg:
		m.qwenInstallEvents = nil

	case deviceListsMsg:
		m.devicesLoading = false
		if msg.err != nil {
			m.message = fmt.Sprintf("Audio device probe failed: %v", msg.err)
			break
		}
		m.inputItems = append([]string{""}, msg.inputs...)
		m.outputItems = append([]string{""}, msg.outputs...)

	case meetingRouteDestsMsg:
		if msg.seq != m.routeDestsSeq {
			break
		}
		m.routeDestsLoading = false
		// Soft-fail: still show configured (+ any) dests when camp list errors.
		m.routeDests = msg.dests
		if msg.err != nil {
			m.routeDestsErr = msg.err.Error()
			m.message = fmt.Sprintf("Found %d destination(s); camp list error: %v", len(msg.dests), msg.err)
			break
		}
		m.routeDestsErr = ""
		m.message = fmt.Sprintf("Found %d route destination(s)", len(msg.dests))
	}

	return m, nil
}

func (m *settingsModel) visibleRange(total int) (int, int) {
	visible := m.visibleRows()
	start := min(max(m.offset, 0), max(total-visible, 0))
	return start, min(start+visible, total)
}

func (m *settingsModel) ensureCursorVisible() {
	visible := m.visibleRows()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+visible {
		m.offset = m.cursor - visible + 1
	}
	m.offset = max(m.offset, 0)
}

func (m settingsModel) visibleRows() int {
	h := m.height
	if h <= 0 {
		// No WindowSize yet — assume a normal terminal rather than a tiny list.
		h = 24
	}
	// Compact: title, tabs, footer. Full: title, tabs, rule, status, footer.
	// The list receives every remaining row so the body tracks terminal height.
	chrome := 5
	if h < 12 {
		chrome = 3
	}
	return max(h-chrome, 1)
}

func (m *settingsModel) currentListLen() int {
	switch m.section {
	case sectionProvider:
		return len(m.providerItems)
	case sectionModel:
		return len(m.modelItems)
	case sectionTools:
		return len(m.toolItems)
	case sectionTTS:
		return len(m.ttsItems)
	case sectionQwen:
		return len(m.qwenItems)
	case sectionVoice:
		return len(m.voiceItems)
	case sectionLanguage:
		return len(m.languageItems)
	case sectionInput:
		return len(m.inputItems)
	case sectionOutput:
		return len(m.outputItems)
	case sectionSpeakers:
		return len(m.speakerItems())
	case sectionMeeting:
		return len(m.meetingItems())
	}
	return 0
}

func (m *settingsModel) selectCurrent() tea.Cmd {
	switch m.section {
	case sectionProvider:
		if m.cursor < len(m.providers) && m.providers[m.cursor].Available {
			// Mutate the live config only after the save succeeds, so a
			// failed save doesn't leave the running session on a provider
			// that was never persisted.
			name := m.providers[m.cursor].Name
			if err := config.SetAndSaveBrainProvider(m.cfg, name); err != nil {
				m.message = fmt.Sprintf("Failed to save provider: %v", err)
				return nil
			}
			m.buildModelItems()
			m.buildToolItems()
			if strings.EqualFold(name, "ollama") && m.cfg.VoiceToolsEnabled {
				m.message = fmt.Sprintf("Provider set to %s · local tools on (Settings → Tools to toggle)", name)
			} else {
				m.message = fmt.Sprintf("Provider set to %s", name)
			}
		}
	case sectionModel:
		if m.cursor < len(m.modelItems) {
			model := m.modelItems[m.cursor]
			var field *string
			var key string
			switch m.cfg.BrainProvider {
			case "ollama":
				field, key = &m.cfg.OllamaModel, "ollama_model"
			case "grok":
				field, key = &m.cfg.GrokModel, "grok_model"
			}
			if field != nil {
				if err := config.SetAndSave(key, model); err != nil {
					m.message = fmt.Sprintf("Failed to save model: %v", err)
					return nil
				}
				*field = model
			}
			m.message = fmt.Sprintf("Model set to %s", model)
		}
	case sectionTools:
		if m.cursor >= len(m.toolItems) {
			return nil
		}
		key := "voice_tools_enabled"
		value := !m.cfg.VoiceToolsEnabled
		label := "Local tools"
		if m.cursor == 1 {
			if !strings.EqualFold(m.cfg.BrainProvider, "ollama") {
				m.message = "Agent Skills apply only when brain provider is Ollama"
				return nil
			}
			key = "skills_enabled"
			value = !m.cfg.SkillsEnabled
			label = "Agent Skills"
		}
		saveConfig := m.saveConfig
		if saveConfig == nil {
			saveConfig = config.SetAndSave
		}
		if err := saveConfig(key, value); err != nil {
			m.message = fmt.Sprintf("Failed to save %s: %v", label, err)
			return nil
		}
		if key == "voice_tools_enabled" {
			m.cfg.VoiceToolsEnabled = value
		} else {
			m.cfg.SkillsEnabled = value
		}
		m.buildToolItems()
		m.message = fmt.Sprintf("%s %s; restart or re-enter conversation to apply", label, enabledLabel(value))
	case sectionTTS:
		if m.cursor < len(m.ttsItems) {
			provider := m.ttsItems[m.cursor].provider
			if provider == managedqwen.ProviderName && !m.nativeStatus.Installed &&
				managedqwen.UseManaged(m.cfg.QwenTTSBinary, m.cfg.QwenTTSModel) {
				if m.qwenInstalling {
					return nil
				}
				ctx, cancel := context.WithCancel(context.Background())
				m.qwenInstallCancel = cancel
				m.qwenInstalling = true
				m.qwenInstallEvents = newEventBridge(16)
				m.message = "Installing native Qwen3-TTS package (large download)…"
				m.buildTTSItems()
				m.buildQwenItems()
				return tea.Batch(m.qwenInstallEvents.wait(), m.installQwenAssets(ctx))
			}
			saveConfig := m.saveConfig
			if saveConfig == nil {
				saveConfig = config.SetAndSave
			}
			if provider == managedqwen.ProviderName && m.nativeStatus.Installed {
				if err := m.saveManagedQwenDefaults(); err != nil {
					m.message = fmt.Sprintf("Failed to save Qwen voice defaults: %v", err)
					return nil
				}
			}
			// Settings writes global defaults only (WI-c8884d §5.2): a
			// persona with its own tts stack keeps it — edit the persona
			// under Personas to change a specific agent's voice.
			if err := saveConfig("tts_provider", provider); err != nil {
				m.message = fmt.Sprintf("Failed to save TTS provider: %v", err)
				return nil
			}
			m.cfg.TTSProvider = provider
			m.buildTTSItems()
			m.buildQwenItems()
			m.buildVoiceItems()
			m.buildLanguageItems()
			m.message = fmt.Sprintf("Default TTS provider set to %s; applies immediately unless the persona has its own voice (edit under Personas)", provider)
		}
	case sectionQwen:
		return m.selectQwenItem()
	case sectionVoice:
		if m.cursor < len(m.voiceItems) {
			voice := m.voiceItems[m.cursor]
			key := "tts_voice"
			if strings.EqualFold(activeTTSProvider(m.cfg), managedqwen.ProviderName) {
				key = "qwen_tts_voice"
			}
			saveConfig := m.saveConfig
			if saveConfig == nil {
				saveConfig = config.SetAndSave
			}
			// Global default only — persona voices are edited under Personas.
			if err := saveConfig(key, voice.Name); err != nil {
				m.message = fmt.Sprintf("Failed to save voice: %v", err)
				return nil
			}
			if key == "qwen_tts_voice" {
				m.cfg.QwenTTSVoice = voice.Name
			} else {
				m.cfg.TTSVoice = voice.Name
			}
			m.message = fmt.Sprintf("Default voice set to %s · personas with their own voice keep it", voice.Name)
		}
	case sectionLanguage:
		if m.cursor < len(m.languageItems) {
			language := m.languageItems[m.cursor]
			saveConfig := m.saveConfig
			if saveConfig == nil {
				saveConfig = config.SetAndSave
			}
			if err := saveConfig("qwen_tts_language", language); err != nil {
				m.message = fmt.Sprintf("Failed to save language: %v", err)
				return nil
			}
			m.cfg.QwenTTSLanguage = language
			m.message = fmt.Sprintf("Qwen language set to %s", language)
		}
	case sectionInput:
		if m.cursor < len(m.inputItems) {
			name := m.inputItems[m.cursor]
			if err := config.SetAndSave("input_device", name); err != nil {
				m.message = fmt.Sprintf("Failed to save input device: %v", err)
				return nil
			}
			m.cfg.InputDevice = name
			m.message = "Microphone set to " + deviceLabel(name)
		}
	case sectionSpeakers:
		m.selectSpeakerItem()
	case sectionMeeting:
		m.selectMeetingItem()
	case sectionOutput:
		if m.cursor < len(m.outputItems) {
			name := m.outputItems[m.cursor]
			if err := config.SetAndSave("output_device", name); err != nil {
				m.message = fmt.Sprintf("Failed to save output device: %v", err)
				return nil
			}
			m.closePreview()
			m.cfg.OutputDevice = name
			m.message = "Speaker set to " + deviceLabel(name)
		}
	}
	return nil
}

type qwenInstallDoneMsg struct {
	status managedqwen.Status
	native managedqwen.NativeStatus
	err    error
}

type qwenInstallProgressMsg struct {
	stage string
	pct   float64
}

type qwenInstallProgressClosedMsg struct{}

func (m settingsModel) installManagedQwen(ctx context.Context) tea.Cmd {
	// Legacy entrypoint — prefer installQwenAssets (native-first ensure).
	return m.installQwenAssets(ctx)
}

func (m *settingsModel) activateQwenAfterInstall() error {
	if err := m.saveManagedQwenDefaults(); err != nil {
		return err
	}
	save := m.saveConfig
	if save == nil {
		save = config.SetAndSave
	}
	if err := save("tts_provider", managedqwen.ProviderName); err != nil {
		return err
	}
	m.cfg.TTSProvider = managedqwen.ProviderName
	tier := strings.TrimSpace(m.cfg.QwenTTSModelTier)
	if tier == "" {
		tier = managedqwen.DefaultModelTier
		if err := save("qwen_tts_model_tier", tier); err != nil {
			return err
		}
		m.cfg.QwenTTSModelTier = tier
	}
	return nil
}

func (m *settingsModel) saveManagedQwenDefaults() error {
	save := m.saveConfig
	if save == nil {
		save = config.SetAndSave
	}
	mode := strings.TrimSpace(m.cfg.QwenTTSMode)
	if mode == "" || mode == string(tts.VoiceModeStatic) {
		mode = string(tts.VoiceModeCustomVoice)
	}
	voice := strings.TrimSpace(m.cfg.QwenTTSVoice)
	if voice == "" || strings.EqualFold(voice, "default") {
		voice = managedqwen.DefaultVoice
	}
	language := strings.TrimSpace(m.cfg.QwenTTSLanguage)
	if language == "" {
		language = managedqwen.DefaultLanguage
	}
	values := []struct {
		key   string
		value string
	}{
		{"qwen_tts_mode", mode},
		{"qwen_tts_voice", voice},
		{"qwen_tts_language", language},
	}
	for _, item := range values {
		if err := save(item.key, item.value); err != nil {
			return err
		}
	}
	m.cfg.QwenTTSMode = mode
	m.cfg.QwenTTSVoice = voice
	m.cfg.QwenTTSLanguage = language
	return nil
}

func deviceLabel(name string) string {
	if name == "" {
		return "System default"
	}
	return name
}
