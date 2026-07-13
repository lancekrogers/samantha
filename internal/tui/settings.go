package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	ansi "github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/discovery"
	"github.com/lancekrogers/samantha/internal/tts"
)

type settingsSection int

const (
	sectionProvider settingsSection = iota
	sectionModel
	sectionVoice
	sectionInput
	sectionOutput
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
	}
	m.buildProviderItems()
	m.buildModelItems()
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
			m.section = (m.section + 1) % 5
			m.cursor = 0
			m.offset = 0
			m.message = ""
		case "shift+tab", "left", "h":
			if m.section > 0 {
				m.section--
			} else {
				m.section = sectionOutput
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
			return m, func() tea.Msg { return switchScreenMsg(screenLauncher) }
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

// cancelPreview stops any in-flight voice preview. Safe to call when idle.
func (m *settingsModel) cancelPreview() {
	if m.previewCancel != nil {
		m.previewCancel()
		m.previewCancel = nil
	}
	if m.previewPlayer != nil {
		m.previewPlayer.Stop()
	}
}

func (m *settingsModel) closePreview() {
	m.cancelPreview()
	if m.previewPlayer != nil {
		_ = m.previewPlayer.Close()
		m.previewPlayer = nil
	}
}

func (m *settingsModel) playerForPreview() audio.Engine {
	if m.previewPlayer != nil {
		return m.previewPlayer
	}
	m.previewPlayer = m.newPreviewPlayer()
	return m.previewPlayer
}

func (m *settingsModel) currentListLen() int {
	switch m.section {
	case sectionProvider:
		return len(m.providerItems)
	case sectionModel:
		return len(m.modelItems)
	case sectionVoice:
		return len(m.voiceItems)
	case sectionInput:
		return len(m.inputItems)
	case sectionOutput:
		return len(m.outputItems)
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

type voicePreviewDoneMsg struct {
	id      int64
	voice   string
	message string
}

func (m settingsModel) previewVoice(ctx context.Context, id int64, voice tts.Voice, player audio.Engine) tea.Cmd {
	// Snapshot the config before the closure runs: the returned Cmd executes on
	// its own goroutine while selectCurrent keeps mutating m.cfg on Update's.
	cfg := *m.cfg
	cfg.TTSVoice = voice.Name
	return func() tea.Msg {
		// A superseded preview (ctx cancelled) reports quietly so it doesn't
		// clobber the newer preview's message or "playing" indicator.
		quiet := voicePreviewDoneMsg{id: id, voice: voice.Name}

		if err := m.ensureTTSAssets(ctx, &cfg); err != nil {
			if errors.Is(err, context.Canceled) {
				return quiet
			}
			return voicePreviewDoneMsg{id: id, voice: voice.Name, message: fmt.Sprintf("Asset error: %v", err)}
		}

		ttsProvider, cleanup, err := m.newTTSProvider(&cfg)
		if err != nil {
			return voicePreviewDoneMsg{id: id, voice: voice.Name, message: fmt.Sprintf("TTS error: %v", err)}
		}
		if cleanup != nil {
			defer cleanup()
		}

		stream, err := ttsProvider.Synthesize(ctx, "Hi, I'm Samantha. This is how I sound.")
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return quiet
			}
			return voicePreviewDoneMsg{id: id, voice: voice.Name, message: fmt.Sprintf("Synthesize error: %v", err)}
		}

		playback, err := player.PlayStream(ctx, stream)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return quiet
			}
			return voicePreviewDoneMsg{id: id, voice: voice.Name, message: fmt.Sprintf("Playback error: %v", err)}
		}

		var result audio.PlaybackResult
		select {
		case <-ctx.Done():
			// cancelPreview already stopped the shared player. Do not stop it
			// again here, because a newer preview may now be queued on it.
			<-playback.Done()
			return quiet
		case result = <-playback.Done():
		}
		if result.Interrupted || errors.Is(result.Err, context.Canceled) {
			return quiet
		}
		if result.Err != nil {
			return voicePreviewDoneMsg{id: id, voice: voice.Name, message: fmt.Sprintf("Playback error: %v", result.Err)}
		}

		return voicePreviewDoneMsg{id: id, voice: voice.Name, message: fmt.Sprintf("Previewed %s", voice.Name)}
	}
}

func (m settingsModel) View() string {
	var b strings.Builder
	width := m.renderWidth()
	compact := m.height > 0 && m.height < 12

	b.WriteString(headerStyle.Render("  Settings"))
	b.WriteString("\n")

	tabs := []string{"Provider", "Model", "Voice", "Input", "Output"}
	var tabLine strings.Builder
	for i, tab := range tabs {
		style := dimStyle
		if settingsSection(i) == m.section {
			style = selectedStyle
		}
		if i > 0 {
			tabLine.WriteString("  ")
		}
		tabLine.WriteString(style.Render(tab))
	}
	b.WriteString(ansi.Truncate("  "+tabLine.String(), width, "…"))
	b.WriteString("\n")
	if !compact {
		b.WriteString(dimStyle.Render(strings.Repeat("─", max(width, 1))))
		b.WriteString("\n")
	}

	// Render active section list.
	switch m.section {
	case sectionProvider:
		start, end := m.visibleRange(len(m.providerItems))
		for i := start; i < end; i++ {
			item := m.providerItems[i]
			active := ""
			if i < len(m.providers) && m.providers[i].Name == m.cfg.BrainProvider {
				active = " ✓"
			}
			m.renderItem(&b, i, item+active)
		}

	case sectionModel:
		start, end := m.visibleRange(len(m.modelItems))
		for i := start; i < end; i++ {
			item := m.modelItems[i]
			active := ""
			if item == m.activeModel() {
				active = " ✓"
			}
			m.renderItem(&b, i, item+active)
		}

	case sectionVoice:
		if len(m.voiceItems) == 0 {
			b.WriteString(dimStyle.Render("  No browsable voices for the active TTS provider."))
			b.WriteString("\n")
			break
		}
		start, end := m.visibleRange(len(m.voiceItems))
		for i := start; i < end; i++ {
			v := m.voiceItems[i]
			active := ""
			if v.Name == m.cfg.TTSVoice {
				active = " ✓"
			}
			preview := ""
			if v.Name == m.previewing {
				preview = " ♫ playing..."
			}
			label := fmt.Sprintf("%-16s %s / %s%s%s", v.Name, v.Gender, v.Locale, active, preview)
			m.renderItem(&b, i, label)
		}

	case sectionInput:
		m.renderDevices(&b, m.inputItems, m.cfg.InputDevice)

	case sectionOutput:
		m.renderDevices(&b, m.outputItems, m.cfg.OutputDevice)
	}

	help := "  ←/→ section • ↑/↓ navigate • enter select"
	if m.section == sectionVoice {
		help += " • p preview"
	}
	help += " • esc back"
	if compact {
		footer := dimStyle.Render(help)
		if m.message != "" {
			footer = statusStyle.Render("  " + m.message)
		}
		b.WriteString(ansi.Truncate(footer, width, "…"))
	} else {
		if m.message != "" {
			b.WriteString(ansi.Truncate("  "+statusStyle.Render(m.message), width, "…"))
		} else {
			b.WriteString(" ")
		}
		b.WriteString("\n")
		b.WriteString(dimStyle.Render(ansi.Truncate(help, width, "…")))
	}

	return b.String()
}

func (m *settingsModel) renderItem(b *strings.Builder, idx int, label string) {
	cursor := "  "
	style := normalStyle
	if idx == m.cursor {
		cursor = "▸ "
		style = selectedStyle
	}
	line := "  " + cursor + style.Render(label)
	b.WriteString(ansi.Truncate(line, m.renderWidth(), "…") + "\n")
}

func (m settingsModel) renderWidth() int {
	if m.width <= 0 {
		return 80
	}
	return m.width
}

func (m *settingsModel) renderDevices(b *strings.Builder, items []string, active string) {
	if m.devicesLoading {
		b.WriteString(dimStyle.Render("  Discovering audio devices..."))
		b.WriteString("\n")
		return
	}
	start, end := m.visibleRange(len(items))
	for i := start; i < end; i++ {
		item := items[i]
		label := deviceLabel(item)
		if item == active {
			label += " ✓"
		}
		m.renderItem(b, i, label)
	}
}
