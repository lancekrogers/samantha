package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/brain"
	"github.com/lancekrogers/samantha/internal/events"
	"github.com/lancekrogers/samantha/internal/speaker"
)

type slashCommandID int

const (
	commandHelp slashCommandID = iota
	commandClear
	commandCompact
	commandSession
	commandMute
	commandUnmute
	commandMic
	commandAudio
	commandActivity
	commandSettings
	commandVoice
	commandSpeakers
	commandCopy
	commandVim
	commandQuit
)

type slashCommand struct {
	id          slashCommandID
	name        string
	usage       string
	description string
	aliases     []string
}

// slashCommands is the single source of truth for command execution, help,
// completion, and the dynamic command palette.
var slashCommands = []slashCommand{
	{id: commandHelp, name: "/help", usage: "/help [command]", description: "Show commands or help for one command", aliases: []string{"/?", "/commands"}},
	{id: commandClear, name: "/clear", usage: "/clear", description: "Clear this conversation", aliases: []string{"/c"}},
	{id: commandCompact, name: "/compact", usage: "/compact", description: "Summarize the conversation and continue from the summary"},
	{id: commandSession, name: "/session", usage: "/session", description: "Show who owns the model session and how big it is"},
	{id: commandMute, name: "/mute", usage: "/mute", description: "Pause voice input"},
	{id: commandUnmute, name: "/unmute", usage: "/unmute", description: "Resume voice input"},
	{id: commandMic, name: "/mic", usage: "/mic", description: "Toggle voice input"},
	{id: commandAudio, name: "/audio", usage: "/audio", description: "Toggle voice output", aliases: []string{"/speaker"}},
	{id: commandActivity, name: "/activity", usage: "/activity", description: "Switch between chat and activity", aliases: []string{"/timeline"}},
	{id: commandSettings, name: "/settings", usage: "/settings", description: "Open TUI settings"},
	{id: commandVoice, name: "/voice", usage: "/voice", description: "Return to voice mode after fallback", aliases: []string{"/v"}},
	{id: commandSpeakers, name: "/speakers", usage: "/speakers [on|off|status|name|names]", description: "Live speaker labels, renames, and status"},
	{id: commandCopy, name: "/copy", usage: "/copy [all]", description: "Copy last reply (or full chat) to the clipboard", aliases: []string{"/yank"}},
	{id: commandVim, name: "/vim", usage: "/vim [on|off|insert]", description: "Toggle modal Vim editing"},
	{id: commandQuit, name: "/quit", usage: "/quit", description: "Exit Samantha", aliases: []string{"/q", "/exit"}},
}

func commandToken(value string) string {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "/") || strings.Contains(value, "\n") {
		return ""
	}
	if i := strings.IndexAny(value, " \t"); i >= 0 {
		value = value[:i]
	}
	return strings.ToLower(value)
}

func commandForToken(token string) (slashCommand, bool) {
	for _, command := range slashCommands {
		if token == command.name || slices.Contains(command.aliases, token) {
			return command, true
		}
	}
	return slashCommand{}, false
}

func parseSlashCommand(value string) (slashCommand, []string, bool, bool) {
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, "/") || strings.Contains(trimmed, "\n") {
		return slashCommand{}, nil, false, false
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return slashCommand{}, nil, false, true
	}
	command, found := commandForToken(strings.ToLower(fields[0]))
	return command, fields[1:], found, true
}

func matchingSlashCommands(value string) []slashCommand {
	token := commandToken(value)
	if token == "" {
		return nil
	}
	var matches []slashCommand
	for _, command := range slashCommands {
		if strings.HasPrefix(command.name, token) {
			matches = append(matches, command)
			continue
		}
		for _, alias := range command.aliases {
			if strings.HasPrefix(alias, token) {
				matches = append(matches, command)
				break
			}
		}
	}
	return matches
}

// suggestSlashCommand returns a close match for an unknown token (prefix on
// names/aliases), or "" when nothing is close enough to suggest.
func suggestSlashCommand(token string) string {
	token = strings.ToLower(strings.TrimSpace(token))
	if token == "" || !strings.HasPrefix(token, "/") {
		return ""
	}
	// Prefer longest shared prefix among command names and aliases.
	best, bestLen := "", 0
	consider := func(candidate string) {
		n := sharedPrefixLen(token, candidate)
		// Require a meaningful stem (e.g. "/se" → "/settings", not "/" alone).
		if n >= 3 && n > bestLen {
			best, bestLen = candidate, n
		}
	}
	for _, command := range slashCommands {
		consider(command.name)
		for _, alias := range command.aliases {
			// Suggest the canonical name when an alias is the closer match.
			n := sharedPrefixLen(token, alias)
			if n >= 3 && n > bestLen {
				best, bestLen = command.name, n
			}
		}
	}
	return best
}

func sharedPrefixLen(a, b string) int {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

func (m *conversationModel) executeSlashCommand(command slashCommand, args []string) tea.Cmd {
	if command.id != commandHelp && command.id != commandVim && command.id != commandSpeakers && command.id != commandCopy && len(args) > 0 {
		m.commandError(fmt.Sprintf("%s does not take arguments", command.name))
		return m.resumeListening()
	}

	switch command.id {
	case commandHelp:
		m.showCommandHelp(args)
		return m.resumeListening()
	case commandClear:
		if m.deps.clearHistory != nil {
			m.deps.clearHistory()
		}
		m.emit(events.ConversationCleared{})
		return m.resumeListening()
	case commandCompact:
		return m.requestCompact()
	case commandSession:
		m.emit(events.Info{Message: m.sessionSummary()})
		return m.resumeListening()
	case commandMute:
		return m.setInputMuted(true)
	case commandUnmute:
		return m.setInputMuted(false)
	case commandMic:
		return m.toggleInputMuted()
	case commandAudio:
		m.toggleOutputMuted()
		return m.resumeListening()
	case commandActivity:
		m.activityFocused = !m.activityFocused
		return m.resumeListening()
	case commandSettings:
		return func() tea.Msg { return switchScreenMsg(screenSettings) }
	case commandVoice:
		if m.deps.voice && !m.voiceEnabled {
			m.voiceEnabled = true
			m.voiceFailures = 0
			m.emit(events.Info{Message: "Switching back to voice mode."})
		}
		return m.resumeListening()
	case commandSpeakers:
		m.configureLiveSpeakers(args)
		return m.resumeListening()
	case commandCopy:
		m.runCopyCommand(args)
		return m.resumeListening()
	case commandVim:
		m.configureVim(args)
		return m.resumeListening()
	case commandQuit:
		m.quitting = true
		return tea.Quit
	default:
		return m.resumeListening()
	}
}

// runCopyCommand handles /copy [all|last]. Default is last assistant reply.
func (m *conversationModel) runCopyCommand(args []string) {
	if len(args) == 0 {
		m.copyLastAssistant()
		return
	}
	if len(args) > 1 {
		m.commandError("usage: /copy [all]")
		return
	}
	switch strings.ToLower(args[0]) {
	case "all", "chat", "full":
		m.copyPlainChat()
	case "last", "reply":
		m.copyLastAssistant()
	default:
		m.commandError("usage: /copy [all]")
	}
}

// sessionSummary renders one Info line for /session: who owns the model
// conversation (harness CLI session vs in-process chat) and how big it is.
func (m *conversationModel) sessionSummary() string {
	if m.deps.brainSession == nil {
		return "session: unavailable for this provider"
	}
	s, ok := m.deps.brainSession()
	if !ok {
		return "session: unavailable for this provider"
	}
	switch s.Kind {
	case brain.SessionKindHarness:
		id := s.ID
		if id == "" {
			// With turns on record and still no id, resume simply is not
			// wired for this provider — "yet" would read as stuck capture.
			if s.Turns > 0 {
				return fmt.Sprintf("session: harness · no live CLI session · %d turns kept", s.Turns)
			}
			return fmt.Sprintf("session: harness · no CLI session yet · %d turns kept", s.Turns)
		}
		if len(id) > 8 {
			id = id[:8]
		}
		if s.PromptTokens > 0 {
			return fmt.Sprintf("session: harness · id %s · last prompt %d tok · %d turns kept", id, s.PromptTokens, s.Turns)
		}
		return fmt.Sprintf("session: harness · id %s · %d turns kept", id, s.Turns)
	default:
		return fmt.Sprintf("session: local chat · est next prompt %d tok · %d turns kept", s.PromptTokens, s.Turns)
	}
}

func (m *conversationModel) configureLiveSpeakers(args []string) {
	if len(args) > 0 {
		switch strings.ToLower(args[0]) {
		case "name":
			m.renameLiveSpeaker(args[1:])
			return
		case "names":
			m.listSpeakerNames()
			return
		}
	}

	if m.liveSpeaker == nil {
		m.commandNotice("Live speaker analysis is unavailable in this runtime.")
		return
	}
	if len(args) > 1 {
		m.commandError("usage: /speakers [on|off|status|name <id> <name>|names]")
		return
	}
	stats := m.liveSpeaker.Stats()
	if len(args) == 0 || strings.EqualFold(args[0], "status") {
		m.liveSpeakerStats = stats
		m.liveSpeakerStatsKnown = true
		m.commandNotice(m.liveSpeakerStatusDetail(stats))
		return
	}
	var enabled bool
	switch strings.ToLower(args[0]) {
	case "on", "enable", "enabled":
		enabled = true
	case "off", "disable", "disabled":
		enabled = false
	default:
		m.commandError("usage: /speakers [on|off|status|name <id> <name>|names]")
		return
	}
	m.liveSpeaker.SetEnabled(enabled)
	if !enabled {
		m.stickyLive.Clear()
	}
	stats = m.liveSpeaker.Stats()
	m.liveSpeakerStats = stats
	m.liveSpeakerStatsKnown = true
	m.commandNotice(m.liveSpeakerStatusDetail(stats))
}

func (m *conversationModel) renameLiveSpeaker(args []string) {
	if m.speakerNames == nil {
		m.commandError("speaker renames are unavailable in this runtime")
		return
	}
	if len(args) < 2 {
		m.commandError("usage: /speakers name <id|N> <display name>")
		return
	}
	id := args[0]
	name := strings.TrimSpace(strings.Join(args[1:], " "))
	if err := m.speakerNames.Set(id, name); err != nil {
		m.commandError(err.Error())
		return
	}
	norm := speaker.NormalizeID(id)
	if name == "" {
		m.commandNotice(fmt.Sprintf("cleared name for %s", norm))
		return
	}
	m.commandNotice(fmt.Sprintf("%s → %s (model prompts use this name)", norm, name))
}

func (m *conversationModel) listSpeakerNames() {
	if m.speakerNames == nil {
		m.commandNotice("no speaker renames in this session")
		return
	}
	snap := m.speakerNames.Snapshot()
	if len(snap) == 0 {
		m.commandNotice("no speaker renames yet — try /speakers name 1 YourName")
		return
	}
	parts := make([]string, 0, len(snap))
	for _, b := range snap {
		parts = append(parts, fmt.Sprintf("%s=%s", b.ID, b.Name))
	}
	m.commandNotice("names: " + strings.Join(parts, " · "))
}

func (m *conversationModel) liveSpeakerStatusDetail(stats speaker.LiveStats) string {
	display := m.displaySpeakerLabel(stats.LastLabel)
	message := liveSpeakerFooterLabelNamed(stats, display)
	if stats.Processed > 0 {
		message += fmt.Sprintf(" · processed %d", stats.Processed)
	}
	if stats.QueueDepth > 0 || stats.Dropped > 0 {
		message += fmt.Sprintf(" · queue %d/%d · dropped %d", stats.QueueDepth, stats.Capacity, stats.Dropped)
	}
	if stats.LastError != "" {
		message += " · " + stats.LastError
	}
	if m.speakerNames != nil {
		if n := len(m.speakerNames.Snapshot()); n > 0 {
			message += fmt.Sprintf(" · %d named", n)
		}
	}
	return message
}

func liveSpeakerStatusDetail(stats speaker.LiveStats) string {
	message := liveSpeakerFooterLabel(stats)
	if stats.Processed > 0 {
		message += fmt.Sprintf(" · processed %d", stats.Processed)
	}
	if stats.QueueDepth > 0 || stats.Dropped > 0 {
		message += fmt.Sprintf(" · queue %d/%d · dropped %d", stats.QueueDepth, stats.Capacity, stats.Dropped)
	}
	if stats.LastError != "" {
		message += " · " + stats.LastError
	}
	return message
}

func (m *conversationModel) showCommandHelp(args []string) {
	if len(args) > 1 {
		m.commandError("usage: /help [command]")
		return
	}
	if len(args) == 1 {
		token := strings.ToLower(args[0])
		if !strings.HasPrefix(token, "/") {
			token = "/" + token
		}
		command, found := commandForToken(token)
		if !found {
			m.commandError("unknown command " + token)
			return
		}
		m.commandNotice(command.usage + " — " + command.description)
		return
	}

	lines := []string{"Slash commands:"}
	for _, command := range slashCommands {
		label := command.usage
		if len(command.aliases) > 0 {
			label += " (" + strings.Join(command.aliases, ", ") + ")"
		}
		lines = append(lines, fmt.Sprintf("  %-32s %s", label, command.description))
	}
	m.commandNotice(strings.Join(lines, "\n"))
}

func (m *conversationModel) commandNotice(message string) {
	m.appendActivity("command", strings.Split(message, "\n")[0], 0)
	m.appendTranscript(dimStyle.Render("  " + message))
}

func (m *conversationModel) commandError(message string) {
	m.appendActivity("command", "error: "+message, 0)
	m.appendTranscript(errorStyle.Render("  " + message))
}
