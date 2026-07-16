package tui

import "testing"

func TestVimPendingStateIsTypedAndInspectable(t *testing.T) {
	state := vimState{enabled: true, mode: vimNormal}
	state.pending = vimPending{kind: vimPendingOperatorFind, operator: 'd', direction: 'f'}

	if got := state.pendingLabel(); got != "df" {
		t.Fatalf("pending label = %q, want df", got)
	}
	state.clearPending()
	if state.pending.kind != vimPendingNone {
		t.Fatalf("clearPending left kind %d", state.pending.kind)
	}
}
