package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/tui/vim"
)

// promptEditor adapts the vim.Editor (ported from camp) to the persona form:
// palette-aware rendering, focus state, insert-mode arrow keys, and mapping ex
// commands onto form-level save/cancel events. It replaces the earlier minimal
// vimTextarea layer (WI-c8884d P5) with the full modal editor — visual mode,
// operators, text objects, yank/paste, undo/redo — so a long system prompt is
// actually editable in place.
type promptEditor struct {
	ed      *vim.Editor
	focused bool
	width   int
	height  int
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
}

func (p *promptEditor) Focus() { p.focused = true }
func (p *promptEditor) Blur()  { p.focused = false }

// StartInsert resets modal state for (re)entering the field; typing works
// immediately, esc drops to normal mode.
func (p *promptEditor) StartInsert() {
	p.ed.State().Reset()
	p.ed.State().EnterInsert()
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
	if !p.focused {
		cfg.CursorBlock = normalStyle
		cfg.CursorInsert = normalStyle
		cfg.Selection = normalStyle
	}
	return p.ed.View(cfg)
}

// Update routes one key through the modal editor and maps the outcome onto a
// form event. A bare esc in idle normal mode cancels the form (matching the
// previous editor); esc with pending state only clears that state.
func (p promptEditor) Update(msg tea.KeyMsg) (promptEditor, tea.Cmd, promptEvent) {
	if msg.String() == "esc" && p.idleNormal() {
		return p, nil, promptEventCancel
	}
	if p.ed.Mode() == vim.ModeInsert && p.insertArrow(msg) {
		return p, nil, promptEventNone
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
	switch p.ed.Mode() {
	case vim.ModeInsert:
		return "-- INSERT -- · esc normal · save: :w / ctrl+j"
	case vim.ModeVisual, vim.ModeVisualLine:
		return "-- VISUAL -- · y yank · d delete · c change · esc normal"
	default:
		return "NORMAL · i insert · v visual · u undo · p paste · :w save · :q cancel"
	}
}
