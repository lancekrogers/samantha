package tui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/discovery"
	"github.com/lancekrogers/samantha/internal/tts"
)

type settingsSection int

const (
	sectionProvider settingsSection = iota
	sectionModel
	sectionTools
	sectionTTS
	sectionVoice
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
	voiceItems     []tts.Voice
	inputItems     []string
	outputItems    []string
	devicesLoading bool
	deviceChecker  config.VoiceDeviceChecker

	// Preview playback state.
	previewing       string
	previewID        int64
	previewCancel    context.CancelFunc
	previewPlayer    audio.Engine
	newPreviewPlayer func() audio.Engine
	ensureTTSAssets  func(context.Context, *config.Config) error
	newTTSProvider   func(*config.Config) (tts.Provider, func(), error)
	saveConfig       func(string, any) error
	message          string
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
	}
	m.buildProviderItems()
	m.buildModelItems()
	m.buildToolItems()
	m.buildTTSItems()
	m.buildVoiceItems()
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
		fmt.Sprintf("Agent Skills — %s", enabledLabel(m.cfg.SkillsEnabled)),
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
		m.ttsItems = append(m.ttsItems, ttsSettingItem{
			provider: spec.Name,
			detail:   ttsProviderDetail(spec, m.cfg),
		})
	}
}

func (m *settingsModel) buildVoiceItems() {
	m.voiceItems = nil
	voices, err := tts.StaticVoices(m.cfg.TTSProvider, "", "")
	if err != nil {
		return
	}
	m.voiceItems = append(m.voiceItems, voices...)
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
		case "shift+tab", "left", "h":
			if m.section > 0 {
				m.section--
			} else {
				m.section = sectionMeeting
			}
			m.cursor = 0
			m.offset = 0
			m.message = ""
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
			m.selectCurrent()
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

	case deviceListsMsg:
		m.devicesLoading = false
		if msg.err != nil {
			m.message = fmt.Sprintf("Audio device probe failed: %v", msg.err)
			break
		}
		m.inputItems = append([]string{""}, msg.inputs...)
		m.outputItems = append([]string{""}, msg.outputs...)
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
	if m.height <= 0 {
		return 10
	}
	// Compact: title, tabs, footer. Full: title, tabs, rule, status, footer.
	// The list receives every other row instead of reserving guessed whitespace.
	chrome := 5
	if m.height < 12 {
		chrome = 3
	}
	return max(m.height-chrome, 1)
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
	case sectionVoice:
		return len(m.voiceItems)
	case sectionInput:
		return len(m.inputItems)
	case sectionOutput:
		return len(m.outputItems)
	case sectionMeeting:
		return len(m.meetingItems())
	}
	return 0
}

func (m *settingsModel) selectCurrent() {
	switch m.section {
	case sectionProvider:
		if m.cursor < len(m.providers) && m.providers[m.cursor].Available {
			// Mutate the live config only after the save succeeds, so a
			// failed save doesn't leave the running session on a provider
			// that was never persisted.
			name := m.providers[m.cursor].Name
			if err := config.SetAndSave("brain_provider", name); err != nil {
				m.message = fmt.Sprintf("Failed to save provider: %v", err)
				return
			}
			m.cfg.BrainProvider = name
			m.buildModelItems()
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
					return
				}
				*field = model
			}
			m.message = fmt.Sprintf("Model set to %s", model)
		}
	case sectionTools:
		if m.cursor >= len(m.toolItems) {
			return
		}
		key := "voice_tools_enabled"
		value := !m.cfg.VoiceToolsEnabled
		label := "Local tools"
		if m.cursor == 1 {
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
			return
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
			saveConfig := m.saveConfig
			if saveConfig == nil {
				saveConfig = config.SetAndSave
			}
			if err := saveConfig("tts_provider", provider); err != nil {
				m.message = fmt.Sprintf("Failed to save TTS provider: %v", err)
				return
			}
			m.cfg.TTSProvider = provider
			m.buildTTSItems()
			m.buildVoiceItems()
			m.message = fmt.Sprintf("TTS provider set to %s; restart to apply", provider)
		}
	case sectionVoice:
		if m.cursor < len(m.voiceItems) {
			voice := m.voiceItems[m.cursor]
			if err := config.SetAndSave("tts_voice", voice.Name); err != nil {
				m.message = fmt.Sprintf("Failed to save voice: %v", err)
				return
			}
			m.cfg.TTSVoice = voice.Name
			m.message = fmt.Sprintf("Voice set to %s", voice.Name)
		}
	case sectionInput:
		if m.cursor < len(m.inputItems) {
			name := m.inputItems[m.cursor]
			if err := config.SetAndSave("input_device", name); err != nil {
				m.message = fmt.Sprintf("Failed to save input device: %v", err)
				return
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
				return
			}
			m.closePreview()
			m.cfg.OutputDevice = name
			m.message = "Speaker set to " + deviceLabel(name)
		}
	}
}

func deviceLabel(name string) string {
	if name == "" {
		return "System default"
	}
	return name
}
