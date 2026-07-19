package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/tts"
)

// Audiobook form choice lists — cycle with enter / ← / → instead of free text.
// Empty string for format means WAV-only (no --audio-format). Empty string for
// voice means omit --voice (config default).
var (
	audiobookSpeeds  = []string{"0.75", "0.9", "1", "1.1", "1.25", "1.5", "1.75", "2"}
	audiobookFormats = []string{"", "mp3", "m4a", "m4b", "aac", "opus"}
)

// audiobook field indices.
const (
	abFieldInput = iota
	abFieldCalibre
	abFieldPickLibrary
	abFieldOutDir
	abFieldVoice
	abFieldSpeed
	abFieldResume
	abFieldAudioFormat
	abFieldGenerate
	abFieldBack
	abFieldCount
)

type audiobookModel struct {
	cfg      *config.Config
	cursor   int
	editing  bool
	editBuf  string
	input    string
	outDir   string
	voice    string
	speed    string
	resume   bool
	audioFmt string
	command  string
	message  string
	errText  string

	// Path tab-completion cycle state (only used while editing path fields).
	pathMatches []string
	pathCycle   int

	// persistCalibre saves calibre_enabled. Injectable for tests; defaults to
	// config.SetAndSave so the toggle survives across TUI sessions.
	persistCalibre func(enabled bool) error
}

func newAudiobook(cfg *config.Config) audiobookModel {
	voice := ""
	if cfg != nil {
		voice = cfg.TTSVoice
	}
	return audiobookModel{
		cfg:    cfg,
		voice:  voice,
		speed:  "1",
		resume: true,
		persistCalibre: func(enabled bool) error {
			return config.SetAndSave("calibre_enabled", enabled)
		},
	}
}

func (m audiobookModel) calibreEnabled() bool {
	return m.cfg != nil && m.cfg.CalibreEnabled
}

func (m audiobookModel) Update(msg tea.Msg) (audiobookModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		key := msg.String()
		if m.editing {
			return m.handleEdit(key)
		}
		switch key {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < abFieldCount-1 {
				m.cursor++
			}
		case "left", "h":
			if m.cycleChoice(-1) {
				return m, nil
			}
		case "right", "l":
			if m.cycleChoice(1) {
				return m, nil
			}
		case "enter", " ":
			return m.activate()
		case "esc", "b":
			return m, func() tea.Msg { return switchScreenMsg(screenLauncher) }
		case "q":
			return m, func() tea.Msg { return quitMsg{} }
		}
	}
	return m, nil
}

// cycleChoice advances Voice / Speed / Audio format options. Returns true when
// the focused field is a choice field.
func (m *audiobookModel) cycleChoice(delta int) bool {
	switch m.cursor {
	case abFieldVoice:
		m.voice = cycleString(m.voiceOptions(), m.voice, delta)
	case abFieldSpeed:
		m.speed = cycleString(audiobookSpeeds, m.speed, delta)
	case abFieldAudioFormat:
		m.audioFmt = cycleString(audiobookFormats, m.audioFmt, delta)
	default:
		return false
	}
	m.command = ""
	m.message = ""
	m.errText = ""
	return true
}

// voiceOptions is the cycle list for Voice: empty = config default, then catalog
// names. A configured voice missing from the catalog is kept so it stays selectable.
func (m audiobookModel) voiceOptions() []string {
	names := tts.VoiceNames()
	opts := make([]string, 0, len(names)+2)
	opts = append(opts, "") // config default / omit --voice
	seen := map[string]bool{"": true}
	if m.voice != "" {
		// Keep an unknown/custom voice in the list so it is not lost when cycling.
		found := false
		for _, n := range names {
			if n == m.voice {
				found = true
				break
			}
		}
		if !found {
			opts = append(opts, m.voice)
			seen[m.voice] = true
		}
	}
	for _, n := range names {
		if seen[n] {
			continue
		}
		opts = append(opts, n)
		seen[n] = true
	}
	return opts
}

// cycleString moves through options, wrapping at the ends. Empty current value
// starts at the first option when stepping forward, or the last when stepping
// backward.
func cycleString(options []string, current string, delta int) string {
	if len(options) == 0 {
		return current
	}
	idx := -1
	for i, o := range options {
		if o == current {
			idx = i
			break
		}
	}
	if idx < 0 {
		if delta >= 0 {
			return options[0]
		}
		return options[len(options)-1]
	}
	idx = (idx + delta) % len(options)
	if idx < 0 {
		idx += len(options)
	}
	return options[idx]
}

func (m audiobookModel) handleEdit(key string) (audiobookModel, tea.Cmd) {
	switch key {
	case "enter":
		m.applyEdit()
		m.editing = false
		m.editBuf = ""
		m.clearPathCompletion()
	case "esc":
		m.editing = false
		m.editBuf = ""
		m.clearPathCompletion()
	case "backspace", "ctrl+h":
		if len(m.editBuf) > 0 {
			// Delete one Unicode character, not one byte.
			_, size := utf8.DecodeLastRuneInString(m.editBuf)
			m.editBuf = m.editBuf[:len(m.editBuf)-size]
		}
		m.clearPathCompletion()
	case "tab":
		if m.cursor == abFieldInput || m.cursor == abFieldOutDir {
			m.applyPathCompletion()
		}
	default:
		if isEditableInsert(key) {
			m.editBuf += key
			m.clearPathCompletion()
		}
	}
	return m, nil
}

func (m *audiobookModel) clearPathCompletion() {
	m.pathMatches = nil
	m.pathCycle = 0
	// Keep validation/message text from generate; only clear completion hints.
	if strings.HasPrefix(m.message, "matches:") || strings.HasPrefix(m.message, "no path matches") {
		m.message = ""
	}
}

func (m *audiobookModel) applyPathCompletion() {
	dirsOnly := m.cursor == abFieldOutDir
	m.errText = ""

	// When multiple matches are already cached and the buffer is still within
	// that set (common prefix or a full match), cycle without re-querying.
	// Re-querying from a full match would collapse to a single entry.
	if len(m.pathMatches) > 1 && pathStillInMatchSet(m.editBuf, m.pathMatches) {
		if m.pathCycle < 0 || m.pathCycle >= len(m.pathMatches) {
			m.pathCycle = 0
		}
		m.editBuf = m.pathMatches[m.pathCycle]
		m.pathCycle = (m.pathCycle + 1) % len(m.pathMatches)
		m.setPathMatchMessage(m.pathMatches)
		return
	}

	completed, matches := completeFilesystemPath(m.editBuf, dirsOnly)
	m.editBuf = completed
	m.pathMatches = matches
	m.pathCycle = 0
	m.setPathMatchMessage(matches)
}

func (m *audiobookModel) setPathMatchMessage(matches []string) {
	switch len(matches) {
	case 0:
		m.message = "no path matches"
	case 1:
		m.message = ""
	default:
		names := make([]string, 0, len(matches))
		for _, match := range matches {
			names = append(names, filepath.Base(strings.TrimSuffix(match, string(filepath.Separator))))
		}
		if len(names) > 6 {
			names = append(names[:6], "…")
		}
		m.message = "matches: " + strings.Join(names, "  ")
	}
}

func pathStillInMatchSet(buf string, matches []string) bool {
	if buf == longestCommonPathPrefix(matches) {
		return true
	}
	for _, match := range matches {
		if buf == match {
			return true
		}
	}
	return false
}

// isEditableInsert reports whether key should be appended to a free-text field.
// Bubble Tea special keys use names like "tab", "ctrl+c"; printable input is
// the character (or pasted runes) itself.
func isEditableInsert(key string) bool {
	if key == "" {
		return false
	}
	switch key {
	case "enter", "esc", "backspace", "delete", "tab", "up", "down", "left", "right",
		"home", "end", "pgup", "pgdown", "space":
		// "space" is sometimes emitted as a named key; real space is " ".
		return false
	}
	if strings.HasPrefix(key, "ctrl+") || strings.HasPrefix(key, "alt+") || strings.HasPrefix(key, "shift+") {
		return false
	}
	// Reject other named multi-letter control tokens (e.g. "f1").
	if len(key) > 1 && !strings.Contains(key, "/") && !strings.Contains(key, " ") && !strings.ContainsAny(key, `~\.-_`) {
		// Allow paths/pastes with punctuation; block pure alpha named keys.
		if isNamedKey(key) {
			return false
		}
	}
	return true
}

func isNamedKey(key string) bool {
	switch key {
	case "enter", "esc", "escape", "backspace", "delete", "tab", "up", "down", "left", "right",
		"home", "end", "pgup", "pgdown", "space", "insert":
		return true
	}
	if len(key) >= 2 && key[0] == 'f' {
		// f1..f12
		allDigits := true
		for i := 1; i < len(key); i++ {
			if key[i] < '0' || key[i] > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			return true
		}
	}
	return false
}

// completeFilesystemPath tab-completes a filesystem path once against the OS.
//
// dirsOnly skips non-directory entries (used for output directory).
// On multiple matches it returns the longest common prefix (so the first Tab
// extends the typed path). Cycling through full matches is handled by the
// audiobook model, which caches the match set.
func completeFilesystemPath(input string, dirsOnly bool) (string, []string) {
	matches := pathCompletionMatches(input, dirsOnly)
	if len(matches) == 0 {
		return input, nil
	}
	if len(matches) == 1 {
		return matches[0], matches
	}
	lcp := longestCommonPathPrefix(matches)
	// Prefer extending the buffer when the common prefix is longer.
	if len(lcp) >= len(input) {
		return lcp, matches
	}
	return input, matches
}

func pathCompletionMatches(input string, dirsOnly bool) []string {
	home, _ := os.UserHomeDir()
	keepTilde := strings.HasPrefix(input, "~")
	expanded := expandHome(input, home)

	dir, prefix := splitPathForCompletion(expanded)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var matches []string
	for _, entry := range entries {
		name := entry.Name()
		if prefix == "" && strings.HasPrefix(name, ".") {
			continue
		}
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		isDir := entry.IsDir()
		if !isDir {
			// Follow symlinks to directories so "out" → "outdir/" works.
			if info, err := entry.Info(); err == nil && info.Mode()&os.ModeSymlink != 0 {
				if fi, err := os.Stat(filepath.Join(dir, name)); err == nil && fi.IsDir() {
					isDir = true
				}
			}
		}
		if dirsOnly && !isDir {
			continue
		}
		full := filepath.Join(dir, name)
		if isDir {
			full += string(filepath.Separator)
		}
		if keepTilde {
			full = collapseHome(full, home)
		}
		matches = append(matches, full)
	}
	return matches
}

func splitPathForCompletion(path string) (dir, prefix string) {
	if path == "" {
		return ".", ""
	}
	// Trailing separator means "complete inside this directory".
	if strings.HasSuffix(path, string(filepath.Separator)) || strings.HasSuffix(path, "/") {
		return path, ""
	}
	dir = filepath.Dir(path)
	prefix = filepath.Base(path)
	if dir == "." && !strings.Contains(path, string(filepath.Separator)) && !strings.Contains(path, "/") {
		// Relative bare name in cwd.
		return ".", prefix
	}
	return dir, prefix
}

func expandHome(path, home string) string {
	if home == "" {
		return path
	}
	sep := string(filepath.Separator)
	// Bare "~" means "inside home" for completion (list/complete children).
	if path == "~" {
		return home + sep
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~"+sep) {
		rest := path[2:]
		// filepath.Join cleans and drops trailing separators. Preserve a trailing
		// separator so splitPathForCompletion treats "~/dir/" as "list inside
		// dir" rather than "complete the name dir" (which re-matches only dir).
		trailing := strings.HasSuffix(path, "/") || strings.HasSuffix(path, sep)
		if rest == "" {
			return home + sep
		}
		expanded := filepath.Join(home, rest)
		if trailing && !strings.HasSuffix(expanded, sep) {
			expanded += sep
		}
		return expanded
	}
	return path
}

func collapseHome(path, home string) string {
	if home == "" {
		return path
	}
	sep := string(filepath.Separator)
	if path == home || path == home+sep {
		return "~" + strings.TrimPrefix(path, home)
	}
	prefix := home + sep
	if strings.HasPrefix(path, prefix) {
		return "~" + sep + path[len(prefix):]
	}
	return path
}

func longestCommonPathPrefix(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	prefix := paths[0]
	for _, p := range paths[1:] {
		for !strings.HasPrefix(p, prefix) {
			if prefix == "" {
				return ""
			}
			// Drop one byte carefully for UTF-8.
			_, size := utf8.DecodeLastRuneInString(prefix)
			prefix = prefix[:len(prefix)-size]
		}
	}
	return prefix
}

func (m *audiobookModel) applyEdit() {
	// Only path fields open free-text edit mode; choice fields cycle instead.
	switch m.cursor {
	case abFieldInput:
		m.input = strings.TrimSpace(m.editBuf)
	case abFieldOutDir:
		m.outDir = strings.TrimSpace(m.editBuf)
	}
	m.command = ""
	m.errText = ""
	m.message = ""
}

func (m audiobookModel) activate() (audiobookModel, tea.Cmd) {
	switch m.cursor {
	case abFieldInput, abFieldOutDir:
		// Paths need free-text (+ tab complete). Voice/speed/format cycle.
		m.editing = true
		m.editBuf = m.fieldValue(m.cursor)
		m.clearPathCompletion()
	case abFieldVoice, abFieldSpeed, abFieldAudioFormat:
		m.cycleChoice(1)
	case abFieldCalibre:
		return m.toggleCalibre()
	case abFieldPickLibrary:
		if !m.calibreEnabled() {
			m.errText = "Calibre is off — toggle Calibre library on above (or: samantha config calibre_enabled true)"
			m.message = ""
			return m, nil
		}
		return m, func() tea.Msg { return switchScreenMsg(screenPickBook) }
	case abFieldResume:
		m.resume = !m.resume
		m.command = ""
		m.message = ""
	case abFieldGenerate:
		cmd, err := m.generateCommand()
		if err != nil {
			m.errText = err.Error()
			m.command = ""
			m.message = ""
		} else {
			m.errText = ""
			m.command = cmd
			m.message = "Command generated (not executed). Copy and run in a shell."
		}
	case abFieldBack:
		return m, func() tea.Msg { return switchScreenMsg(screenLauncher) }
	}
	return m, nil
}

// toggleCalibre flips calibre_enabled, persists it, and updates the live config
// so Pick from library works immediately without restarting the TUI.
func (m audiobookModel) toggleCalibre() (audiobookModel, tea.Cmd) {
	if m.cfg == nil {
		m.errText = "config not loaded"
		return m, nil
	}
	next := !m.cfg.CalibreEnabled
	save := m.persistCalibre
	if save == nil {
		save = func(enabled bool) error {
			return config.SetAndSave("calibre_enabled", enabled)
		}
	}
	if err := save(next); err != nil {
		m.errText = fmt.Sprintf("Failed to save calibre_enabled: %v", err)
		m.message = ""
		return m, nil
	}
	m.cfg.CalibreEnabled = next
	m.errText = ""
	m.command = ""
	if next {
		m.message = "Calibre library on (saved). Use Pick from library to search."
	} else {
		m.message = "Calibre library off (saved)."
	}
	return m, nil
}

func (m audiobookModel) fieldValue(i int) string {
	switch i {
	case abFieldInput:
		return m.input
	case abFieldOutDir:
		return m.outDir
	case abFieldVoice:
		return m.voice
	case abFieldSpeed:
		return m.speed
	case abFieldAudioFormat:
		return m.audioFmt
	default:
		return ""
	}
}

// GenerateAudiobookCommand builds a shell-quoted samantha audiobook create line.
// It never executes the command.
func GenerateAudiobookCommand(input, outDir, voice, speed string, resume bool, audioFormat string) (string, error) {
	if strings.TrimSpace(input) == "" {
		return "", fmt.Errorf("input path is required")
	}
	if strings.TrimSpace(outDir) == "" {
		return "", fmt.Errorf("output directory is required")
	}
	if speed != "" {
		if _, err := strconv.ParseFloat(speed, 64); err != nil {
			return "", fmt.Errorf("speed must be a number")
		}
	}
	parts := []string{"samantha", "audiobook", "create", shellQuote(input), "--out-dir", shellQuote(outDir)}
	if resume {
		parts = append(parts, "--resume")
	}
	if v := strings.TrimSpace(voice); v != "" {
		parts = append(parts, "--voice", shellQuote(v))
	}
	if s := strings.TrimSpace(speed); s != "" {
		if f, err := strconv.ParseFloat(s, 64); err == nil && f > 0 {
			parts = append(parts, "--speed", shellQuote(s))
		}
	}
	if af := strings.TrimSpace(audioFormat); af != "" {
		parts = append(parts, "--audio-format", shellQuote(af))
	}
	return strings.Join(parts, " "), nil
}

func (m audiobookModel) generateCommand() (string, error) {
	return GenerateAudiobookCommand(m.input, m.outDir, m.voice, m.speed, m.resume, m.audioFmt)
}

// shellQuote quotes s for POSIX shells when needed.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t\n'\"\\$`!*?[]{}();<>|&") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (m audiobookModel) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("  Create audiobook"))
	b.WriteString("\n")
	b.WriteString(subtitleStyle.Render("  Generate a shell command (does not run it)"))
	b.WriteString("\n\n")

	type row struct {
		field int
		label string
		value string
	}
	calibreState := map[bool]string{true: "on", false: "off"}[m.calibreEnabled()]
	rows := []row{
		{abFieldInput, "Input path", displayOr(m.input, "(required)")},
		{abFieldCalibre, "Calibre library", calibreState},
		{abFieldPickLibrary, "Pick from library", pickLibraryHint(m.calibreEnabled())},
		{abFieldOutDir, "Output dir", displayOr(m.outDir, "(required)")},
		{abFieldVoice, "Voice", displayOr(m.voice, "(config default)") + choiceHint(m.cursor == abFieldVoice)},
		{abFieldSpeed, "Speed", displayOr(m.speed, "1") + choiceHint(m.cursor == abFieldSpeed)},
		{abFieldResume, "Resume", map[bool]string{true: "on", false: "off"}[m.resume]},
		{abFieldAudioFormat, "Audio format", displayOr(m.audioFmt, "(wav only)") + choiceHint(m.cursor == abFieldAudioFormat)},
		{abFieldGenerate, "Generate command", ""},
		{abFieldBack, "Back to launcher", ""},
	}
	for _, r := range rows {
		cursor := "  "
		style := normalStyle
		if r.field == m.cursor {
			cursor = "▸ "
			style = selectedStyle
		}
		line := r.label
		if r.value != "" {
			val := r.value
			if m.editing && r.field == m.cursor {
				val = m.editBuf + "█"
			}
			line = fmt.Sprintf("%-18s %s", r.label, val)
		}
		b.WriteString("  " + cursor + style.Render(line) + "\n")
	}

	if m.errText != "" {
		b.WriteString("\n")
		b.WriteString(errorStyle.Render("  " + m.errText))
		b.WriteString("\n")
	}
	if m.message != "" {
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  " + m.message))
		b.WriteString("\n")
	}
	if m.command != "" {
		b.WriteString("\n")
		b.WriteString(selectedStyle.Render("  " + m.command))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	if m.editing {
		b.WriteString(dimStyle.Render("  type path • tab complete • enter save • esc cancel"))
	} else {
		b.WriteString(dimStyle.Render("  ↑/↓ navigate • ←/→ or enter cycle choices • enter paths/toggle/pick • b back"))
	}
	b.WriteString("\n")
	return b.String()
}

func choiceHint(focused bool) string {
	if !focused {
		return ""
	}
	return "  ←/→"
}

func displayOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func pickLibraryHint(enabled bool) string {
	if enabled {
		return "search Calibre…"
	}
	return "(enable Calibre first)"
}
