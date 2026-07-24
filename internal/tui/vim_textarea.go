package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

// vimTextarea wraps a bubbles textarea with a minimal modal layer: insert and
// normal modes, core motions, and :w/:q ex commands. Deliberately small
// (design WI-c8884d §2.5 and its risk table): full vim is not the goal —
// never losing an 8k persona prompt to a mis-keyed form is. The conversation
// composer's vim controller stays separate: it drives a custom editorBuffer,
// while this layer translates modal keys onto the textarea's own keymap.
type vimTextarea struct {
	ta   textarea.Model
	mode vimTAMode

	pendingD bool
	pendingG bool
	exActive bool
	exCmd    string
}

type vimTAMode int

const (
	vimTAInsert vimTAMode = iota
	vimTANormal
)

// vimTAEvent reports a form-level action the modal layer requested.
type vimTAEvent int

const (
	vimTAEventNone vimTAEvent = iota
	vimTAEventSave
	vimTAEventCancel
)

func newVimTextarea(ta textarea.Model) vimTextarea {
	return vimTextarea{ta: ta, mode: vimTAInsert}
}

// StartInsert resets the modal state for (re)entering the field.
func (v *vimTextarea) StartInsert() {
	v.mode = vimTAInsert
	v.pendingD, v.pendingG, v.exActive = false, false, false
	v.exCmd = ""
}

func (v *vimTextarea) Value() string           { return v.ta.Value() }
func (v *vimTextarea) SetValue(s string)       { v.ta.SetValue(s) }
func (v *vimTextarea) Focus() tea.Cmd          { return v.ta.Focus() }
func (v *vimTextarea) Blur()                   { v.ta.Blur() }
func (v *vimTextarea) SetWidth(w int)          { v.ta.SetWidth(w) }
func (v *vimTextarea) SetHeight(h int)         { v.ta.SetHeight(h) }
func (v vimTextarea) View() string             { return v.ta.View() }
func (v vimTextarea) Mode() vimTAMode          { return v.mode }
func (v vimTextarea) ExBuffer() (string, bool) { return v.exCmd, v.exActive }

// UpdateMsg forwards non-key messages (cursor blink) straight to the textarea.
func (v vimTextarea) UpdateMsg(msg tea.Msg) (vimTextarea, tea.Cmd) {
	var cmd tea.Cmd
	v.ta, cmd = v.ta.Update(msg)
	return v, cmd
}

// Update routes one key through the modal layer.
func (v vimTextarea) Update(msg tea.KeyMsg) (vimTextarea, tea.Cmd, vimTAEvent) {
	if v.exActive {
		return v.updateEx(msg), nil, v.execEx(msg)
	}
	if v.mode == vimTAInsert {
		if msg.String() == "esc" {
			v.mode = vimTANormal
			return v, nil, vimTAEventNone
		}
		var cmd tea.Cmd
		v.ta, cmd = v.ta.Update(msg)
		return v, cmd, vimTAEventNone
	}
	return v.updateNormal(msg)
}

func (v vimTextarea) updateNormal(msg tea.KeyMsg) (vimTextarea, tea.Cmd, vimTAEvent) {
	key := msg.String()

	if v.pendingD {
		v.pendingD = false
		if key == "d" {
			v.deleteLine()
		}
		return v, nil, vimTAEventNone
	}
	if v.pendingG {
		v.pendingG = false
		if key == "g" {
			v.jumpTop()
		}
		return v, nil, vimTAEventNone
	}

	switch key {
	case "esc":
		// Normal-mode esc leaves the editor entirely — the form cancels.
		return v, nil, vimTAEventCancel
	case "i":
		v.mode = vimTAInsert
	case "a":
		v.key(tea.KeyRight)
		v.mode = vimTAInsert
	case "I":
		v.key(tea.KeyHome)
		v.mode = vimTAInsert
	case "A":
		v.key(tea.KeyEnd)
		v.mode = vimTAInsert
	case "o":
		v.key(tea.KeyEnd)
		v.key(tea.KeyEnter)
		v.mode = vimTAInsert
	case "O":
		v.key(tea.KeyHome)
		v.key(tea.KeyEnter)
		v.key(tea.KeyUp)
		v.mode = vimTAInsert
	case "h", "left":
		v.key(tea.KeyLeft)
	case "j", "down", "enter":
		v.key(tea.KeyDown)
	case "k", "up":
		v.key(tea.KeyUp)
	case "l", "right":
		v.key(tea.KeyRight)
	case "0", "^":
		v.key(tea.KeyHome)
	case "$":
		v.key(tea.KeyEnd)
	case "w":
		v.altKey('f') // textarea WordForward
	case "b":
		v.altKey('b') // textarea WordBackward
	case "x":
		v.key(tea.KeyDelete)
	case "D":
		v.ctrlKey(tea.KeyCtrlK) // DeleteAfterCursor
	case "d":
		v.pendingD = true
	case "g":
		v.pendingG = true
	case "G":
		v.jumpBottom()
	case ":":
		v.exActive = true
		v.exCmd = ""
	}
	return v, nil, vimTAEventNone
}

// updateEx edits the ex-command buffer; execEx decides its outcome.
func (v vimTextarea) updateEx(msg tea.KeyMsg) vimTextarea {
	switch msg.String() {
	case "esc":
		v.exActive = false
		v.exCmd = ""
	case "enter":
		v.exActive = false
	case "backspace":
		if v.exCmd != "" {
			v.exCmd = v.exCmd[:len(v.exCmd)-1]
		} else {
			v.exActive = false
		}
	default:
		if msg.Type == tea.KeyRunes && !msg.Alt {
			v.exCmd += string(msg.Runes)
		}
	}
	return v
}

func (v vimTextarea) execEx(msg tea.KeyMsg) vimTAEvent {
	if msg.String() != "enter" {
		return vimTAEventNone
	}
	switch strings.TrimSpace(v.exCmd) {
	case "w", "wq", "x":
		return vimTAEventSave
	case "q", "q!":
		return vimTAEventCancel
	default:
		return vimTAEventNone
	}
}

func (v *vimTextarea) key(t tea.KeyType) {
	v.ta, _ = v.ta.Update(tea.KeyMsg{Type: t})
}

func (v *vimTextarea) altKey(r rune) {
	v.ta, _ = v.ta.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: true})
}

func (v *vimTextarea) ctrlKey(t tea.KeyType) {
	v.ta, _ = v.ta.Update(tea.KeyMsg{Type: t})
}

// deleteLine implements dd: clear the line's content, then remove the line
// itself — joining forward, or upward when it is the last line (vim keeps no
// trailing empty line behind a deleted tail).
func (v *vimTextarea) deleteLine() {
	v.key(tea.KeyHome)
	v.ctrlKey(tea.KeyCtrlK)
	if v.ta.Line() == v.ta.LineCount()-1 && v.ta.LineCount() > 1 {
		v.key(tea.KeyBackspace)
	} else {
		v.key(tea.KeyDelete)
	}
}

// jumpTop / jumpBottom walk line-by-line; prompt bodies are small enough that
// a bounded loop beats depending on keymap entries that vary across versions.
func (v *vimTextarea) jumpTop() {
	for i := 0; i < v.ta.LineCount(); i++ {
		v.key(tea.KeyUp)
	}
	v.key(tea.KeyHome)
}

func (v *vimTextarea) jumpBottom() {
	for i := 0; i < v.ta.LineCount(); i++ {
		v.key(tea.KeyDown)
	}
	v.key(tea.KeyEnd)
}

// modeline renders the editor status for the form footer.
func (v vimTextarea) modeline() string {
	if v.exActive {
		return ":" + v.exCmd
	}
	if v.mode == vimTANormal {
		return "NORMAL · i insert · :w save · :q cancel · esc leave"
	}
	return "-- INSERT -- · esc for NORMAL"
}
