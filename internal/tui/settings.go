package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/discovery"
	"github.com/lancekrogers/samantha/internal/meeting"
	"github.com/lancekrogers/samantha/internal/persona"
	managedqwen "github.com/lancekrogers/samantha/internal/qwen"
	"github.com/lancekrogers/samantha/internal/tts"
)

// personaCreateRow is the trailing list entry that opens the create form.
const personaCreateRowLabel = "+ Create new persona…"

type settingsSection int

const (
	sectionPersona settingsSection = iota
	sectionProvider
	sectionModel
	sectionTools
	sectionTTS
	sectionVoice
	sectionLanguage
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
	personaItems   []*persona.Profile
	personaLoadErr string
	providerItems  []string
	modelItems     []string
	toolItems      []string
	ttsItems       []ttsSettingItem
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
	ensureTTSAssets  func(context.Context, *config.Config) error
	newTTSProvider   func(*config.Config) (tts.Provider, func(), error)
	saveConfig       func(string, any) error
	savePersonaTTS   func(*config.Config, string, string) error
	listPersonas     func() ([]*persona.Profile, error)
	usePersona       func(*config.Config, string) error
	createPersona    func(*config.Config, string) (*persona.Profile, error)
	message          string

	// Persona create form (Settings → Persona → Create new).
	personaCreating bool
	personaCreate   textinput.Model

	qwenStatus        managedqwen.Status
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
		ensureTTSAssets: func(ctx context.Context, cfg *config.Config) error {
			return config.EnsureRuntimeAssets(ctx, cfg, config.AssetRequest{NeedTTS: true}, nil)
		},
		newTTSProvider: tts.NewProvider,
		saveConfig:     config.SetAndSave,
		savePersonaTTS: persona.UpdateActiveTTS,
		listPersonas:   persona.List,
		usePersona:     persona.Use,
		createPersona:  persona.CreateAndUse,
		ensureQwen:     managedqwen.Ensure,
	}
	m.personaCreate = newPersonaCreateInput()
	m.qwenStatus = managedqwen.Inspect(config.ModelsDirFrom(cfg))
	m.buildPersonaItems()
	m.buildProviderItems()
	m.buildModelItems()
	m.buildToolItems()
	m.buildTTSItems()
	m.buildVoiceItems()
	m.buildLanguageItems()
	m.inputItems = []string{""}
	m.outputItems = []string{""}
	return m
}

func newPersonaCreateInput() textinput.Model {
	ti := textinput.New()
	ti.Placeholder = "Research buddy"
	ti.CharLimit = 64
	ti.Width = 40
	ti.Prompt = "  Name: "
	return ti
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

func (m *settingsModel) buildPersonaItems() {
	m.personaItems = nil
	m.personaLoadErr = ""
	list := m.listPersonas
	if list == nil {
		list = persona.List
	}
	items, err := list()
	if err != nil {
		m.personaLoadErr = err.Error()
		return
	}
	m.personaItems = items
}

// personaListLabel formats one row for the Persona section.
func personaListLabel(p *persona.Profile) string {
	if p == nil {
		return ""
	}
	label := p.DisplayName
	if label == "" {
		label = p.ID
	}
	if p.ID != "" && p.DisplayName != "" && !strings.EqualFold(p.DisplayName, p.ID) {
		label = fmt.Sprintf("%s (%s)", p.DisplayName, p.ID)
	}
	detail := strings.TrimSpace(p.TTS.Provider)
	if v := strings.TrimSpace(p.TTS.Voice); v != "" {
		if detail != "" {
			detail += " · " + v
		} else {
			detail = v
		}
	}
	if detail != "" {
		label += " — " + detail
	}
	return label
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
				detail = "installing managed runtime and CustomVoice model…"
			case managed && m.qwenStatus.Installed:
				detail = fmt.Sprintf("managed CustomVoice · %d preset voices · ready", len(managedqwen.CustomVoices()))
			case managed:
				detail = "not installed · enter to install preset voices"
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
	if strings.EqualFold(activeTTSProvider(m.cfg), managedqwen.ProviderName) && m.qwenStatus.Installed &&
		managedqwen.UseManaged(m.cfg.QwenTTSBinary, m.cfg.QwenTTSModel) {
		for _, voice := range managedqwen.CustomVoices() {
			m.voiceItems = append(m.voiceItems, tts.Voice{
				Name: voice.Name, FriendlyName: voice.Description,
				Gender: "preset", Locale: voice.NativeLanguage,
			})
		}
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
	if strings.EqualFold(activeTTSProvider(m.cfg), managedqwen.ProviderName) &&
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
		m.personaCreate.Width = max(m.width-16, 20)
		m.ensureCursorVisible()

	case tea.KeyMsg:
		// Persona create form captures keys until submit/cancel.
		if m.personaCreating {
			return m.updatePersonaCreate(msg)
		}
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
			break
		}
		m.qwenStatus = msg.status
		if err := m.activateManagedQwen(); err != nil {
			m.message = fmt.Sprintf("Qwen installed but configuration could not be saved: %v", err)
			m.buildTTSItems()
			break
		}
		m.buildTTSItems()
		m.buildVoiceItems()
		m.buildLanguageItems()
		m.message = "Qwen preset voices installed and activated; open Voice to preview and select"

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

func (m settingsModel) updatePersonaCreate(msg tea.KeyMsg) (settingsModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.cancelPersonaCreate()
		m.message = "Create cancelled"
		return m, nil
	case "enter":
		name := strings.TrimSpace(m.personaCreate.Value())
		if name == "" {
			m.message = "Enter a display name for the new persona"
			return m, nil
		}
		create := m.createPersona
		if create == nil {
			create = persona.CreateAndUse
		}
		p, err := create(m.cfg, name)
		if err != nil {
			m.message = fmt.Sprintf("Failed to create persona: %v", err)
			return m, nil
		}
		m.cancelPersonaCreate()
		m.qwenStatus = managedqwen.Inspect(config.ModelsDirFrom(m.cfg))
		m.buildPersonaItems()
		m.buildTTSItems()
		m.buildVoiceItems()
		m.buildLanguageItems()
		// Focus the new persona row.
		for i, item := range m.personaItems {
			if item != nil && item.ID == p.ID {
				m.cursor = i
				break
			}
		}
		m.message = fmt.Sprintf("Created and activated %s (%s)", p.DisplayName, p.ID)
		return m, nil
	}
	var cmd tea.Cmd
	m.personaCreate, cmd = m.personaCreate.Update(msg)
	return m, cmd
}

func (m *settingsModel) beginPersonaCreate() {
	m.personaCreating = true
	m.message = ""
	if m.personaCreate.CharLimit == 0 {
		m.personaCreate = newPersonaCreateInput()
	}
	m.personaCreate.SetValue("")
	m.personaCreate.Width = max(m.width-16, 20)
	m.personaCreate.Focus()
}

func (m *settingsModel) cancelPersonaCreate() {
	m.personaCreating = false
	m.personaCreate.Blur()
	m.personaCreate.SetValue("")
}

func (m *settingsModel) currentListLen() int {
	switch m.section {
	case sectionPersona:
		if m.personaLoadErr != "" {
			return 1
		}
		// Always reserve a trailing "Create new" row.
		n := len(m.personaItems)
		if n == 0 {
			return 1 // create-only when empty / error already handled
		}
		return n + 1

	case sectionProvider:
		return len(m.providerItems)
	case sectionModel:
		return len(m.modelItems)
	case sectionTools:
		return len(m.toolItems)
	case sectionTTS:
		return len(m.ttsItems)
	case sectionVoice:
		return len(m.voiceItems)
	case sectionLanguage:
		return len(m.languageItems)
	case sectionInput:
		return len(m.inputItems)
	case sectionOutput:
		return len(m.outputItems)
	case sectionMeeting:
		return len(m.meetingItems())
	}
	return 0
}

func (m *settingsModel) selectCurrent() tea.Cmd {
	switch m.section {
	case sectionPersona:
		if m.personaLoadErr != "" {
			return nil
		}
		// Trailing create row (or sole row when empty).
		if len(m.personaItems) == 0 || m.cursor == len(m.personaItems) {
			m.beginPersonaCreate()
			return nil
		}
		if m.cursor < 0 || m.cursor >= len(m.personaItems) {
			return nil
		}
		p := m.personaItems[m.cursor]
		if p == nil {
			return nil
		}
		use := m.usePersona
		if use == nil {
			use = persona.Use
		}
		if err := use(m.cfg, p.ID); err != nil {
			m.message = fmt.Sprintf("Failed to switch persona: %v", err)
			return nil
		}
		// Voice list depends on the persona's TTS provider.
		m.qwenStatus = managedqwen.Inspect(config.ModelsDirFrom(m.cfg))
		m.buildPersonaItems()
		m.buildTTSItems()
		m.buildVoiceItems()
		m.buildLanguageItems()
		m.message = fmt.Sprintf("Active persona: %s", p.DisplayName)
		if p.DisplayName == "" {
			m.message = fmt.Sprintf("Active persona: %s", p.ID)
		}
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
			m.message = fmt.Sprintf("Provider set to %s", name)
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
			if provider == managedqwen.ProviderName && managedqwen.UseManaged(m.cfg.QwenTTSBinary, m.cfg.QwenTTSModel) && !m.qwenStatus.Installed {
				if m.qwenInstalling {
					return nil
				}
				ctx, cancel := context.WithCancel(context.Background())
				m.qwenInstallCancel = cancel
				m.qwenInstalling = true
				m.qwenInstallEvents = newEventBridge(16)
				m.message = "Installing the managed Qwen runtime and preset voices; this is a large first-time download…"
				m.buildTTSItems()
				return tea.Batch(m.qwenInstallEvents.wait(), m.installManagedQwen(ctx))
			}
			saveConfig := m.saveConfig
			if saveConfig == nil {
				saveConfig = config.SetAndSave
			}
			if provider == managedqwen.ProviderName && managedqwen.UseManaged(m.cfg.QwenTTSBinary, m.cfg.QwenTTSModel) && m.qwenStatus.Installed {
				if err := m.saveManagedQwenDefaults(); err != nil {
					m.message = fmt.Sprintf("Failed to save Qwen voice defaults: %v", err)
					return nil
				}
			}
			voice := m.cfg.TTSVoice
			if provider == managedqwen.ProviderName {
				voice = m.cfg.QwenTTSVoice
			}
			if strings.TrimSpace(m.cfg.ActivePersona) != "" {
				savePersonaTTS := m.savePersonaTTS
				if savePersonaTTS == nil {
					savePersonaTTS = persona.UpdateActiveTTS
				}
				if err := savePersonaTTS(m.cfg, provider, voice); err != nil {
					m.message = fmt.Sprintf("Failed to save persona TTS provider: %v", err)
					return nil
				}
			} else if err := saveConfig("tts_provider", provider); err != nil {
				m.message = fmt.Sprintf("Failed to save TTS provider: %v", err)
				return nil
			}
			m.cfg.TTSProvider = provider
			m.buildTTSItems()
			m.buildVoiceItems()
			m.buildLanguageItems()
			m.message = fmt.Sprintf("TTS provider set to %s; applies immediately when you return to conversation", provider)
		}
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
			if strings.TrimSpace(m.cfg.ActivePersona) != "" {
				savePersonaTTS := m.savePersonaTTS
				if savePersonaTTS == nil {
					savePersonaTTS = persona.UpdateActiveTTS
				}
				if err := savePersonaTTS(m.cfg, activeTTSProvider(m.cfg), voice.Name); err != nil {
					m.message = fmt.Sprintf("Failed to save persona voice: %v", err)
					return nil
				}
			} else if err := saveConfig(key, voice.Name); err != nil {
				m.message = fmt.Sprintf("Failed to save voice: %v", err)
				return nil
			}
			if key == "qwen_tts_voice" {
				m.cfg.QwenTTSVoice = voice.Name
			} else {
				m.cfg.TTSVoice = voice.Name
			}
			m.message = fmt.Sprintf("Voice set to %s", voice.Name)
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
	err    error
}

type qwenInstallProgressMsg struct {
	stage string
	pct   float64
}

type qwenInstallProgressClosedMsg struct{}

func (m settingsModel) installManagedQwen(ctx context.Context) tea.Cmd {
	ensure := m.ensureQwen
	if ensure == nil {
		ensure = managedqwen.Ensure
	}
	modelsDir := config.ModelsDirFrom(m.cfg)
	events := m.qwenInstallEvents
	return func() tea.Msg {
		status, err := ensure(ctx, modelsDir, func(stage string, pct float64) {
			if events != nil {
				events.send(qwenInstallProgressMsg{stage: stage, pct: pct})
			}
		})
		if events != nil {
			events.send(qwenInstallProgressClosedMsg{})
		}
		return qwenInstallDoneMsg{status: status, err: err}
	}
}

func (m *settingsModel) activateManagedQwen() error {
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
