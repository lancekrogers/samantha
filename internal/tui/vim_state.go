package tui

// vimState is the small state machine that sits between key presses and
// editor actions. Pending sequences are typed data instead of strings that
// callers must parse and concatenate.
type vimState struct {
	enabled bool
	mode    vimInputMode
	pending vimPending
}

type vimInputMode int

const (
	vimInsert vimInputMode = iota
	vimNormal
	vimVisual
)

type vimPendingKind int

const (
	vimPendingNone vimPendingKind = iota
	vimPendingG
	vimPendingFind
	vimPendingReplace
	vimPendingOperator
	vimPendingOperatorFind
)

type vimPending struct {
	kind      vimPendingKind
	operator  byte
	direction byte
}

func (s *vimState) clearPending() {
	s.pending = vimPending{}
}

func (s vimState) pendingLabel() string {
	switch s.pending.kind {
	case vimPendingG:
		return "g"
	case vimPendingFind:
		return "find " + string(s.pending.direction)
	case vimPendingReplace:
		return "replace"
	case vimPendingOperator:
		return string(s.pending.operator)
	case vimPendingOperatorFind:
		return string([]byte{s.pending.operator, s.pending.direction})
	default:
		return ""
	}
}
