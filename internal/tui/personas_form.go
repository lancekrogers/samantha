package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/persona"
	"github.com/lancekrogers/samantha/pkg/voiceagent/brain"
	managedqwen "github.com/lancekrogers/samantha/pkg/voiceagent/qwen"
	"github.com/lancekrogers/samantha/pkg/voiceagent/tts"
)

const (
	personaFormName   = 0
	personaFormPrompt = 1
	personaFormStack  = 2
)

// Stack step rows.
const (
	stackRowBrainProvider = 0
	stackRowBrainModel    = 1
	stackRowTTSProvider   = 2
	stackRowVoice         = 3
	stackRowCount         = 4
)

// stackDefaultLabel is provider index 0: inherit the app-level default.
const stackDefaultLabel = "(default)"

// stackBrainProviders lists selectable brain providers for the form.
func stackBrainProviders() []string {
	out := []string{stackDefaultLabel}
	for _, spec := range brain.Providers() {
		out = append(out, spec.Name)
	}
	return out
}

// stackTTSProviders lists selectable TTS providers for the form.
func stackTTSProviders() []string {
	out := []string{stackDefaultLabel}
	for _, spec := range tts.Providers() {
		out = append(out, spec.Name)
	}
	return out
}

// providerIndex maps a stored provider name onto its list index (0 = default).
func providerIndex(list []string, name string) int {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0
	}
	for i, item := range list {
		if strings.EqualFold(item, name) {
			return i
		}
	}
	return 0
}

// providerAt returns the provider name for a list index; "" for the default.
func providerAt(list []string, idx int) string {
	if idx <= 0 || idx >= len(list) {
		return ""
	}
	return list[idx]
}

// stackVoiceCatalog returns known voice ids for a TTS provider. Empty provider
// falls through to kokoro (the product default when Settings is also unset).
func stackVoiceCatalog(provider string) []string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "qwen3-tts":
		voices := managedqwen.CustomVoices()
		out := make([]string, 0, len(voices))
		for _, v := range voices {
			out = append(out, v.Name)
		}
		return out
	case "", "kokoro":
		voices, err := tts.StaticVoices("kokoro", "", "")
		if err != nil {
			return nil
		}
		out := make([]string, 0, len(voices))
		for _, v := range voices {
			out = append(out, v.Name)
		}
		return out
	default:
		return nil
	}
}

// stackVoiceList builds the selectable voice row: (default) + provider catalog,
// plus any non-catalog voice already on the persona so edit never drops it.
func (m personasModel) stackVoiceList() []string {
	provider := providerAt(stackTTSProviders(), m.ttsProviderIdx)
	if provider == "" && m.cfg != nil {
		// When TTS is "(default)", list the voices for the app Settings provider
		// so the user still has a real catalog to pick from.
		provider = strings.TrimSpace(m.cfg.TTSProvider)
	}
	out := []string{stackDefaultLabel}
	out = append(out, stackVoiceCatalog(provider)...)
	if v := strings.TrimSpace(m.selectedVoice); v != "" {
		found := false
		for _, item := range out[1:] {
			if strings.EqualFold(item, v) {
				found = true
				break
			}
		}
		if !found {
			out = append(out, v)
		}
	}
	return out
}

// stackVoiceIndex maps selectedVoice onto stackVoiceList (0 = inherit default).
func (m personasModel) stackVoiceIndex() int {
	list := m.stackVoiceList()
	v := strings.TrimSpace(m.selectedVoice)
	if v == "" {
		return 0
	}
	for i, item := range list {
		if i == 0 {
			continue
		}
		if strings.EqualFold(item, v) {
			return i
		}
	}
	return 0
}

// cycleStackVoice moves ←/→ through the voice catalog for the current TTS provider.
func (m *personasModel) cycleStackVoice(delta int) {
	list := m.stackVoiceList()
	if len(list) == 0 {
		m.selectedVoice = ""
		return
	}
	idx := (m.stackVoiceIndex() + delta + len(list)) % len(list)
	if idx == 0 {
		m.selectedVoice = ""
		return
	}
	m.selectedVoice = list[idx]
}

func newPersonaStackInput(placeholder string) textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = placeholder
	ti.CharLimit = 64
	ti.Width = 40
	return ti
}

func (m *personasModel) resizeForm() {
	inner := m.formBoxWidth() - 4
	m.nameInput.Width = max(inner-2, 10)
	// The editor draws a 4-column line-number gutter inside the box.
	m.promptTA.SetWidth(max(inner-5, 20))
	m.brainModelInput.Width = max(inner-12, 10)
	// Leave room for chrome (pills, field boxes, help) so the prompt editor
	// is never clipped out of the form body.
	h := min(max(m.height-18, 3), 12)
	m.promptTA.SetHeight(h)
}

// isPersonaFormSaveKey reports keys that commit the create/edit form.
// ctrl+s alone is unreliable: many terminals still implement software flow
// control (XOFF) and swallow it before Bubble Tea sees it. Prefer ctrl+j /
// ctrl+enter / alt+s / f2 — those actually reach the program.
func isPersonaFormSaveKey(key string) bool {
	switch key {
	case "ctrl+s", "ctrl+j", "ctrl+enter", "alt+enter", "alt+s", "f2":
		return true
	default:
		return false
	}
}

func (m personasModel) updateForm(msg tea.KeyMsg) (personasModel, tea.Cmd) {
	key := msg.String()
	switch {
	case key == "esc":
		// The prompt step owns esc: insert→normal first; only an idle
		// normal-mode esc (surfacing as promptEventCancel below) leaves the form.
		if m.formStep == personaFormPrompt {
			break
		}
		m.cancelForm()
		m.message = "Edit cancelled"
		return m, nil
	case isPersonaFormSaveKey(key):
		return m.submitForm()
	case key == "tab", key == "shift+tab":
		// On the prompt step Tab is the placeholder-completion key whenever the
		// editor has a `{…}` to complete; only fall through to field navigation
		// when it does not, otherwise the completion is unreachable in the form.
		if m.formStep == personaFormPrompt && m.promptTA.completionActive() {
			break
		}
		if key == "shift+tab" {
			switch m.formStep {
			case personaFormPrompt:
				return m.focusNameStep()
			case personaFormStack:
				return m.focusPromptStep()
			default:
				return m.focusStackStep()
			}
		}
		switch m.formStep {
		case personaFormName:
			return m.focusPromptStep()
		case personaFormPrompt:
			return m.focusStackStep()
		default:
			return m.focusNameStep()
		}
	case key == "enter":
		if m.formStep == personaFormName {
			// Name done → prompt. Save is intentionally not Enter (multi-line
			// prompt needs Enter for newlines).
			return m.focusPromptStep()
		}
		// Enter falls through to the prompt editor (newline in insert mode,
		// save in normal mode); on the stack step it advances rows.
		if m.formStep == personaFormStack {
			m.setStackRow((m.stackRow + 1) % stackRowCount)
			return m, nil
		}
	}
	if m.formStep == personaFormStack {
		return m.updateStackStep(msg)
	}

	var cmd tea.Cmd
	if m.formStep == personaFormName {
		m.nameInput, cmd = m.nameInput.Update(msg)
		return m, cmd
	}
	var ev promptEvent
	m.promptTA, cmd, ev = m.promptTA.Update(msg)
	switch ev {
	case promptEventSave:
		return m.submitForm()
	case promptEventAccept:
		// :w — leave the prompt editor and continue the wizard (Model & voice).
		// Submitting the whole form from here used to skip that step and leave
		// users staring at a closed form with no way to set provider/voice.
		return m.focusStackStep()
	case promptEventCancel:
		m.cancelForm()
		m.message = "Edit cancelled"
	}
	return m, cmd
}

// updateStackStep routes keys inside the model/voice step: ↑/↓ move rows,
// ←/→ cycle providers/voices on those rows, typing goes into the model field.
func (m personasModel) updateStackStep(msg tea.KeyMsg) (personasModel, tea.Cmd) {
	key := msg.String()
	switch key {
	case "up":
		m.setStackRow((m.stackRow + stackRowCount - 1) % stackRowCount)
		return m, nil
	case "down":
		m.setStackRow((m.stackRow + 1) % stackRowCount)
		return m, nil
	case "left", "right":
		delta := 1
		if key == "left" {
			delta = -1
		}
		switch m.stackRow {
		case stackRowBrainProvider:
			list := stackBrainProviders()
			m.brainProviderIdx = (m.brainProviderIdx + delta + len(list)) % len(list)
			return m, nil
		case stackRowTTSProvider:
			list := stackTTSProviders()
			m.ttsProviderIdx = (m.ttsProviderIdx + delta + len(list)) % len(list)
			// Voice catalog is provider-specific: drop a selection that is no
			// longer in the new list so we don't keep a Kokoro id under Qwen.
			m.clampSelectedVoice()
			return m, nil
		case stackRowVoice:
			m.cycleStackVoice(delta)
			return m, nil
		}
	}

	var cmd tea.Cmd
	if m.stackRow == stackRowBrainModel {
		m.brainModelInput, cmd = m.brainModelInput.Update(msg)
	}
	return m, cmd
}

// clampSelectedVoice clears selectedVoice when it is not in the current
// provider's catalog (and is not a sticky non-catalog value we want to keep
// only when the provider still matches). After a TTS provider change, unknown
// or other-provider voices fall back to "(default)".
func (m *personasModel) clampSelectedVoice() {
	v := strings.TrimSpace(m.selectedVoice)
	if v == "" {
		return
	}
	provider := providerAt(stackTTSProviders(), m.ttsProviderIdx)
	if provider == "" && m.cfg != nil {
		provider = strings.TrimSpace(m.cfg.TTSProvider)
	}
	for _, name := range stackVoiceCatalog(provider) {
		if strings.EqualFold(name, v) {
			// Canonicalize to the catalog spelling (e.g. Uncle_Fu vs uncle_fu).
			m.selectedVoice = name
			return
		}
	}
	m.selectedVoice = ""
}

// setStackRow moves focus between stack rows, focusing the model text input
// only on that row (voice is cycled with ←/→, not typed).
func (m *personasModel) setStackRow(row int) {
	m.stackRow = row
	m.brainModelInput.Blur()
	if row == stackRowBrainModel {
		m.brainModelInput.Focus()
	}
}

func (m personasModel) focusStackStep() (personasModel, tea.Cmd) {
	m.formStep = personaFormStack
	m.nameInput.Blur()
	m.promptTA.Blur()
	m.setStackRow(stackRowBrainProvider)
	m.message = "Model & voice · ←/→ pick provider or voice · ↑/↓ rows · (default) inherits Settings"
	return m, nil
}

// formBrain returns the stack step's brain selection ("" fields = inherit).
func (m *personasModel) formBrain() persona.Brain {
	return persona.Brain{
		Provider: providerAt(stackBrainProviders(), m.brainProviderIdx),
		Model:    strings.TrimSpace(m.brainModelInput.Value()),
	}
}

// formTTS returns the stack step's TTS selection ("" fields = inherit).
func (m *personasModel) formTTS() persona.TTS {
	return persona.TTS{
		Provider: providerAt(stackTTSProviders(), m.ttsProviderIdx),
		Voice:    strings.TrimSpace(m.selectedVoice),
	}
}

// prefillStack seeds the stack step from a profile (nil = defaults).
func (m *personasModel) prefillStack(p *persona.Profile) {
	if p == nil {
		m.brainProviderIdx = 0
		m.ttsProviderIdx = 0
		m.brainModelInput.SetValue("")
		m.selectedVoice = ""
		return
	}
	m.brainProviderIdx = providerIndex(stackBrainProviders(), p.Brain.Provider)
	m.ttsProviderIdx = providerIndex(stackTTSProviders(), p.TTS.Provider)
	m.brainModelInput.SetValue(strings.TrimSpace(p.Brain.Model))
	m.selectedVoice = strings.TrimSpace(p.TTS.Voice)
	// Canonicalize known voices to catalog spelling; keep unknown as sticky
	// options so a hand-edited yaml value is not silently wiped.
	if m.selectedVoice != "" {
		provider := providerAt(stackTTSProviders(), m.ttsProviderIdx)
		if provider == "" && m.cfg != nil {
			provider = strings.TrimSpace(m.cfg.TTSProvider)
		}
		for _, name := range stackVoiceCatalog(provider) {
			if strings.EqualFold(name, m.selectedVoice) {
				m.selectedVoice = name
				break
			}
		}
	}
}

func (m *personasModel) cancelForm() {
	m.formMode = ""
	m.formStep = personaFormName
	m.editID = ""
	m.nameInput.Blur()
	m.nameInput.SetValue("")
	m.promptTA.Blur()
	m.promptTA.SetValue("")
	m.prefillStack(nil)
	m.brainModelInput.Blur()
	m.stackRow = stackRowBrainProvider
}

func (m personasModel) focusNameStep() (personasModel, tea.Cmd) {
	m.formStep = personaFormName
	m.promptTA.Blur()
	m.nameInput.Focus()
	return m, textinput.Blink
}

func (m personasModel) focusPromptStep() (personasModel, tea.Cmd) {
	if strings.TrimSpace(m.nameInput.Value()) == "" {
		m.message = "Enter a display name first"
		return m, nil
	}
	m.formStep = personaFormPrompt
	m.nameInput.Blur()
	// Ensure a usable draft: an empty prompt body gets the seed identity.
	if strings.TrimSpace(m.promptTA.Value()) == "" {
		if text := m.resolveSeedPrompt(); text != "" {
			m.promptTA.SetValue(text)
		}
	}
	m.promptTA.StartInsert()
	m.promptTA.Focus()
	m.message = "Edit the system prompt · esc for NORMAL · :w next step · :wq / ctrl+j save form · :q cancel"
	return m, nil
}

func (m *personasModel) resolveDefaultPrompt() string {
	if m.defaultPrompt == nil {
		return ""
	}
	text, err := m.defaultPrompt()
	if err != nil {
		return ""
	}
	return text
}

// resolveSeedPrompt returns the draft for an empty prompt field: the minimal
// starter for new personas, the full built-in identity when editing (so a
// failed load never silently downgrades an existing persona to the starter).
func (m *personasModel) resolveSeedPrompt() string {
	if m.formMode == "create" && m.starterPrompt != nil {
		if text, err := m.starterPrompt(); err == nil && strings.TrimSpace(text) != "" {
			return text
		}
	}
	return m.resolveDefaultPrompt()
}

func (m *personasModel) beginCreate() tea.Cmd {
	m.formMode = "create"
	m.formStep = personaFormName
	m.editID = ""
	m.message = "Name the agent, then edit the system prompt · save with ctrl+j / alt+s / f2"
	m.resizeForm()
	m.nameInput.SetValue("")
	m.nameInput.Focus()
	m.promptTA.SetValue(m.resolveSeedPrompt())
	m.promptTA.Blur()
	m.prefillStack(nil) // "(default)" = clone current globals at create time
	return textinput.Blink
}

func (m *personasModel) beginEdit() tea.Cmd {
	if m.loadErr != "" || m.cursor < 0 || m.cursor >= len(m.items) {
		return nil
	}
	p := m.items[m.cursor]
	if p == nil {
		return nil
	}
	m.formMode = "edit"
	m.formStep = personaFormName
	m.editID = p.ID
	m.message = "Edit name + system prompt (used by the brain as the real persona system prompt)"
	m.resizeForm()
	m.nameInput.SetValue(p.DisplayName)
	m.nameInput.Focus()
	text, loadNote := m.loadEditPrompt(p)
	m.promptTA.SetValue(text)
	if loadNote != "" {
		m.message = loadNote
	}
	m.promptTA.Blur()
	m.prefillStack(p)
	return textinput.Blink
}

// loadEditPrompt loads the system prompt body for the editor without ever
// substituting the embedded samantha default for a different persona.
func (m *personasModel) loadEditPrompt(p *persona.Profile) (text, note string) {
	if p == nil {
		return "", "No persona selected"
	}
	if m.loadPromptForProfile != nil {
		got, err := m.loadPromptForProfile(p)
		if err == nil {
			return got, ""
		}
		note = fmt.Sprintf("Could not load system prompt: %v · edit and save to create prompts/persona/%s.yaml", err, p.ID)
	} else if m.loadPrompt != nil {
		name := strings.TrimSpace(p.Prompts.Persona)
		if name == "" {
			name = p.ID
		}
		if got, err := m.loadPrompt(name); err == nil {
			return got, ""
		}
		if p.ID != "" && name != p.ID {
			if got, err := m.loadPrompt(p.ID); err == nil {
				return got, ""
			}
		}
		note = fmt.Sprintf("System prompt %q not found · edit and save to create prompts/persona/%s.yaml", name, p.ID)
	}
	// Empty body — never inject the default samantha identity into another
	// persona's editor. Create form still seeds the default via beginCreate.
	return "", note
}

func (m personasModel) submitForm() (personasModel, tea.Cmd) {
	name := strings.TrimSpace(m.nameInput.Value())
	if name == "" {
		m.message = "Enter a display name"
		m.formStep = personaFormName
		m.nameInput.Focus()
		return m, textinput.Blink
	}
	prompt := strings.TrimSpace(m.promptTA.Value())
	if prompt == "" {
		// Name-only create/edit still needs a real identity document for the brain.
		prompt = strings.TrimSpace(m.resolveSeedPrompt())
	}
	if prompt == "" {
		m.message = "Enter a system prompt (this is the persona identity the brain loads)"
		m.formStep = personaFormPrompt
		m.promptTA.Focus()
		return m, nil
	}

	switch m.formMode {
	case "create":
		create := m.createPersona
		if create == nil {
			create = persona.CreateAndUseWithOpts
		}
		p, err := create(m.cfg, persona.CreateOpts{
			DisplayName:  name,
			SystemPrompt: prompt,
			Brain:        m.formBrain(),
			TTS:          m.formTTS(),
		})
		if err != nil {
			m.message = fmt.Sprintf("Failed to create: %v", err)
			return m, nil
		}
		m.cancelForm()
		m.reload()
		for i, item := range m.items {
			if item != nil && item.ID == p.ID {
				m.cursor = i
				break
			}
		}
		m.ensureVisible()
		m.message = fmt.Sprintf("Created %s (%s) · system prompt → prompts/persona/%s.yaml · restart chat to apply", p.DisplayName, p.ID, p.ID)
	case "edit":
		if m.saveName != nil {
			if _, err := m.saveName(m.editID, name); err != nil {
				m.message = fmt.Sprintf("Failed to save name: %v", err)
				return m, nil
			}
		}
		if m.savePrompt != nil {
			if _, err := m.savePrompt(m.editID, prompt); err != nil {
				m.message = fmt.Sprintf("Failed to save prompt: %v", err)
				return m, nil
			}
		}
		if m.saveStack != nil {
			if _, err := m.saveStack(m.editID, m.formBrain(), m.formTTS()); err != nil {
				m.message = fmt.Sprintf("Failed to save model/voice: %v", err)
				return m, nil
			}
		}
		// If this is the active persona, refresh display name / prompt ref on cfg.
		if m.cfg != nil && persona.ActiveID(m.cfg) == m.editID {
			if m.usePersona != nil {
				_ = m.usePersona(m.cfg, m.editID)
			}
		}
		id := m.editID
		m.cancelForm()
		m.reload()
		m.message = fmt.Sprintf("Updated %s · prompts/persona/%s.yaml · start a new chat for the brain to load it", id, id)
	}
	return m, nil
}
