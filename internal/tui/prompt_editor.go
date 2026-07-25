package tui

import (
	"fmt"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/prompts"
	"github.com/lancekrogers/samantha/internal/tui/vim"
)

// promptEditor adapts the vim.Editor (ported from camp) to the persona form:
// palette-aware rendering, focus state, insert-mode arrow keys, and mapping ex
// commands onto form-level save/cancel events. It replaces the earlier minimal
// vimTextarea layer (WI-c8884d P5) with the full modal editor — visual mode,
// operators, text objects, yank/paste, undo/redo — so a long system prompt is
// actually editable in place.
//
// Placeholder completion (insert mode): type `{`, then Tab to cycle known
// template variables; Enter inserts the selected name plus a closing `}`.
// Complete tokens like {agent_name} are colorized in the view.
type promptEditor struct {
	ed      *vim.Editor
	focused bool
	width   int
	height  int

	// Placeholder tab-completion state (insert mode only).
	phActive     bool
	phBraceCol   int // column of the opening '{' on the cursor line
	phBraceLine  int
	phCandidates []string
	phIndex      int
}

// promptEvent reports a form-level action the editor requested.
type promptEvent int

const (
	promptEventNone promptEvent = iota
	promptEventSave
	promptEventCancel
)

func newPromptEditor() promptEditor {
	return promptEditor{ed: vim.NewEditor(""), width: 60, height: 8}
}

func (p *promptEditor) Value() string { return p.ed.Content() }

// SetValue replaces the draft, dropping cursor/undo history from the previous
// persona so u can never resurrect another profile's prompt.
func (p *promptEditor) SetValue(s string) {
	p.ed = vim.NewEditor(s)
	p.ed.SetSize(p.width, p.height)
	p.clearPlaceholderCompletion()
}

func (p *promptEditor) Focus() { p.focused = true }
func (p *promptEditor) Blur()  { p.focused = false }

// StartInsert resets modal state for (re)entering the field; typing works
// immediately, esc drops to normal mode.
func (p *promptEditor) StartInsert() {
	p.ed.State().Reset()
	p.ed.State().EnterInsert()
	p.clearPlaceholderCompletion()
}

func (p *promptEditor) SetWidth(w int) {
	p.width = max(w, 10)
	p.ed.SetSize(p.width, p.height)
}

func (p *promptEditor) SetHeight(h int) {
	p.height = max(h, 1)
	p.ed.SetSize(p.width, p.height)
}

func (p promptEditor) Mode() vim.Mode { return p.ed.Mode() }

// ExBuffer returns the pending ex command text and whether command mode is live.
func (p promptEditor) ExBuffer() (string, bool) {
	return p.ed.CommandBuffer(), p.ed.IsCommandMode()
}

// View renders the editor. A blurred field paints no cursor or selection so
// the form's other steps don't show a phantom reverse-video cell.
func (p promptEditor) View() string {
	cfg := vim.DefaultViewConfig()
	cfg.NormalText = normalStyle
	cfg.LineNumber = dimStyle
	cfg.CommandLine = dimStyle
	cfg.Selection = selectionStyle
	cfg.PlaceholderKnown = placeholderKnownStyle
	cfg.PlaceholderUnknown = placeholderUnknownStyle
	cfg.KnownPlaceholders = knownPlaceholderSet()
	if !p.focused {
		cfg.CursorBlock = normalStyle
		cfg.CursorInsert = normalStyle
		cfg.Selection = normalStyle
	}
	return p.ed.View(cfg)
}

func knownPlaceholderSet() map[string]bool {
	names := prompts.PlaceholderNames()
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out
}

// Update routes one key through the modal editor and maps the outcome onto a
// form event. A bare esc in idle normal mode cancels the form (matching the
// previous editor); esc with pending state only clears that state.
func (p promptEditor) Update(msg tea.KeyMsg) (promptEditor, tea.Cmd, promptEvent) {
	if msg.String() == "esc" && p.idleNormal() {
		return p, nil, promptEventCancel
	}

	// Placeholder completion intercepts Tab/Enter/Esc in insert mode.
	if p.ed.Mode() == vim.ModeInsert {
		if handled, next := p.handlePlaceholderKeys(msg); handled {
			return next, nil, promptEventNone
		}
	}

	if p.ed.Mode() == vim.ModeInsert && p.insertArrow(msg) {
		p.clearPlaceholderCompletion()
		return p, nil, promptEventNone
	}
	// Any non-completion key that mutates content drops the cycle state;
	// handlePlaceholderKeys already no-ops when the brace context is gone.
	if p.phActive && !isPlaceholderNavKey(msg) {
		// Typing more of a name keeps completion useful only while the prefix
		// still matches; rebuild on next Tab. Clear so Enter inserts newline.
		if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace || msg.Type == tea.KeyBackspace || msg.Type == tea.KeyDelete {
			p.clearPlaceholderCompletion()
		}
	}

	cmd, _ := p.ed.Update(msg)
	switch strings.TrimSpace(cmd) {
	case "w", "wq", "x":
		return p, nil, promptEventSave
	case "q", "q!":
		return p, nil, promptEventCancel
	}
	return p, nil, promptEventNone
}

func isPlaceholderNavKey(msg tea.KeyMsg) bool {
	switch msg.Type {
	case tea.KeyTab, tea.KeyShiftTab, tea.KeyEnter, tea.KeyEscape:
		return true
	}
	return false
}

// handlePlaceholderKeys implements { → Tab cycle → Enter commit.
// Returns handled=true when the key was consumed for completion.
func (p *promptEditor) handlePlaceholderKeys(msg tea.KeyMsg) (bool, promptEditor) {
	switch msg.Type {
	case tea.KeyTab, tea.KeyShiftTab:
		return p.cyclePlaceholder(msg.Type == tea.KeyShiftTab), *p
	case tea.KeyEnter:
		if !p.phActive {
			return false, *p
		}
		p.commitPlaceholder()
		return true, *p
	case tea.KeyEscape:
		if !p.phActive {
			return false, *p
		}
		// Leave the opening `{` (and any typed prefix); drop the preview name.
		p.revertPlaceholderPreview()
		p.clearPlaceholderCompletion()
		return true, *p
	default:
		return false, *p
	}
}

// cyclePlaceholder starts or advances tab-completion after an unclosed `{`.
func (p *promptEditor) cyclePlaceholder(reverse bool) bool {
	line := p.ed.CurrentLine()
	cur := p.ed.Cursor()
	braceCol, partial, ok := openBracePrefix(line, cur.Col)
	if !ok {
		return false
	}

	sameCtx := p.phActive && p.phBraceLine == cur.Line && p.phBraceCol == braceCol && len(p.phCandidates) > 0
	if !sameCtx {
		// Fresh context: filter by typed prefix. If the partial is already a
		// full known name (re-opening after a preview), offer the whole catalog.
		p.phCandidates = prompts.FilterPlaceholders(partial)
		if len(p.phCandidates) == 0 {
			if prompts.IsKnownPlaceholder(partial) {
				p.phCandidates = prompts.PlaceholderNames()
			} else {
				return false
			}
		}
		p.phIndex = 0
		if reverse {
			p.phIndex = len(p.phCandidates) - 1
		}
		p.phActive = true
		p.phBraceCol = braceCol
		p.phBraceLine = cur.Line
	} else if reverse {
		p.phIndex--
		if p.phIndex < 0 {
			p.phIndex = len(p.phCandidates) - 1
		}
	} else {
		p.phIndex = (p.phIndex + 1) % len(p.phCandidates)
	}

	name := p.phCandidates[p.phIndex]
	// Replace everything after `{` up to cursor with the candidate name
	// (no closing brace yet — Enter commits `}`).
	p.ed.ReplaceLineRange(braceCol+1, cur.Col, name)
	p.ed.EnsureCursorVisible()
	return true
}

// commitPlaceholder inserts the closing `}` after the previewed name.
func (p *promptEditor) commitPlaceholder() {
	if !p.phActive || len(p.phCandidates) == 0 {
		p.clearPlaceholderCompletion()
		return
	}
	line := p.ed.CurrentLine()
	cur := p.ed.Cursor()
	// Ensure name is present after brace, then add `}` if missing.
	name := p.phCandidates[p.phIndex]
	braceCol := p.phBraceCol
	if braceCol < 0 || braceCol >= len(line) || line[braceCol] != '{' {
		// Brace lost — insert full token at cursor.
		p.ed.InsertAtCursor("{" + name + "}")
		p.clearPlaceholderCompletion()
		return
	}
	// Replace [brace+1, cur) with name, then append `}` if next char isn't.
	p.ed.ReplaceLineRange(braceCol+1, cur.Col, name)
	cur = p.ed.Cursor()
	line = p.ed.CurrentLine()
	if cur.Col >= len(line) || line[cur.Col] != '}' {
		p.ed.InsertAtCursor("}")
	} else {
		// Move past existing `}`.
		p.ed.SetCursorInsert(vim.Position{Line: cur.Line, Col: cur.Col + 1})
	}
	p.clearPlaceholderCompletion()
	p.ed.EnsureCursorVisible()
}

// revertPlaceholderPreview restores `{` + original empty/partial by leaving
// only the opening brace (drops the cycled name preview).
func (p *promptEditor) revertPlaceholderPreview() {
	if !p.phActive {
		return
	}
	cur := p.ed.Cursor()
	line := p.ed.CurrentLine()
	if p.phBraceLine != cur.Line || p.phBraceCol < 0 || p.phBraceCol >= len(line) {
		return
	}
	p.ed.ReplaceLineRange(p.phBraceCol+1, cur.Col, "")
}

func (p *promptEditor) clearPlaceholderCompletion() {
	p.phActive = false
	p.phBraceCol = 0
	p.phBraceLine = 0
	p.phCandidates = nil
	p.phIndex = 0
}

// openBracePrefix finds an unclosed `{` before col on the same line.
// partial is the identifier-ish text between `{` and col.
func openBracePrefix(line string, col int) (braceCol int, partial string, ok bool) {
	if col < 0 {
		col = 0
	}
	if col > len(line) {
		col = len(line)
	}
	// Walk left for the nearest `{` that is not already closed before col.
	for i := col - 1; i >= 0; i-- {
		switch line[i] {
		case '}':
			// Closed token — stop; no open brace for completion.
			return 0, "", false
		case '{':
			// Characters after `{` up to col must be a valid (possibly empty) prefix.
			partial = line[i+1 : col]
			if !isPlaceholderPartial(partial) {
				return 0, "", false
			}
			return i, partial, true
		}
	}
	return 0, "", false
}

func isPlaceholderPartial(s string) bool {
	if s == "" {
		return true
	}
	for i, r := range s {
		if i == 0 {
			if r != '_' && !unicode.IsLetter(r) {
				return false
			}
			continue
		}
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// idleNormal reports normal mode with nothing pending — the only state where
// esc means "leave the editor" instead of "clear what I started".
func (p promptEditor) idleNormal() bool {
	s := p.ed.State()
	return s.Mode == vim.ModeNormal &&
		!s.HasPendingOperator() &&
		s.PendingKey == 0 &&
		!s.AwaitingChar && !s.AwaitingReplace && !s.AwaitingTextObj &&
		!s.HasCount
}

// insertArrow moves the cursor on arrow keys in insert mode, which the ported
// editor deliberately omits but a form field can't do without.
func (p *promptEditor) insertArrow(msg tea.KeyMsg) bool {
	lines := strings.Split(p.ed.Content(), "\n")
	cur := p.ed.Cursor()
	switch msg.Type {
	case tea.KeyLeft:
		if cur.Col > 0 {
			cur.Col--
		}
	case tea.KeyRight:
		if cur.Col < len(lines[cur.Line]) {
			cur.Col++
		}
	case tea.KeyUp:
		if cur.Line > 0 {
			cur.Line--
			cur.Col = min(cur.Col, len(lines[cur.Line]))
		}
	case tea.KeyDown:
		if cur.Line < len(lines)-1 {
			cur.Line++
			cur.Col = min(cur.Col, len(lines[cur.Line]))
		}
	default:
		return false
	}
	p.ed.SetCursorInsert(cur)
	p.ed.EnsureCursorVisible()
	return true
}

// modeChip renders the mode name in its status color for the field title.
func (p promptEditor) modeChip() string {
	switch p.ed.Mode() {
	case vim.ModeInsert:
		return statusStyle.Render("INSERT")
	case vim.ModeVisual:
		return speakStyle.Render("VISUAL")
	case vim.ModeVisualLine:
		return speakStyle.Render("V-LINE")
	case vim.ModeCommand:
		return warningStyle.Render("COMMAND")
	default:
		return headerStyle.Render("NORMAL")
	}
}

// modeline is the one-line editor status for the form footer.
func (p promptEditor) modeline() string {
	if ex, active := p.ExBuffer(); active {
		return ":" + ex
	}
	if p.phActive && len(p.phCandidates) > 0 {
		name := p.phCandidates[p.phIndex]
		return fmt.Sprintf(
			"var %s %s · tab cycle · enter insert {…} · esc cancel",
			placeholderKnownStyle.Render("{"+name+"}"),
			dimStyle.Render(fmt.Sprintf("%d/%d", p.phIndex+1, len(p.phCandidates))),
		)
	}
	switch p.ed.Mode() {
	case vim.ModeInsert:
		return "-- INSERT -- · { tab variables · esc normal · save: :w / ctrl+j"
	case vim.ModeVisual, vim.ModeVisualLine:
		return "-- VISUAL -- · y yank · d delete · c change · esc normal"
	default:
		return "NORMAL · i insert · v visual · u undo · p paste · :w save · :q cancel"
	}
}
