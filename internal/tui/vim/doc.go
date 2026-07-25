// Package vim is a self-contained vim-style modal text editor for Bubble Tea
// TUIs: normal/insert/visual/command modes, counts, operators (d/c/y), text
// objects, f/t motions, registers via a single yank buffer, and snapshot
// undo/redo. Ported from camp's internal/tui/vim editor with the syntax
// highlighter and theme coupling removed; hosts style the view through
// ViewConfig and interpret ex commands (:w, :q!) themselves.
//
// The editor is not a tea.Model: Update takes a tea.KeyMsg and returns the
// submitted ex-command text (if any), leaving the event loop to the host.
package vim
