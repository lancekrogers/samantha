package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/calibre"
	"github.com/lancekrogers/samantha/internal/config"
)

func TestPickBookFoldsResults(t *testing.T) {
	m := newPickBook(&config.Config{CalibreEnabled: true})
	m, _ = m.Update(calibreResultsMsg{
		books: []calibre.Book{
			{ID: 1, Title: "Crypto 101", Authors: []string{"Krol"}, Formats: []string{"/x/c.epub"}},
			{ID: 2, Title: "AI", Authors: []string{"Norvig"}, Formats: []string{"/x/a.pdf"}},
		},
	})
	if m.focus != pickFocusList || len(m.books) != 2 {
		t.Fatalf("focus=%d books=%d", m.focus, len(m.books))
	}
	if !strings.Contains(m.View(), "Crypto 101") {
		t.Fatalf("view missing book:\n%s", m.View())
	}
}

func TestPickBookBrowseCmdUsesList(t *testing.T) {
	var gotArgs []string
	m := newPickBook(&config.Config{CalibreEnabled: true})
	m.client = calibre.Client{
		LookPath: func(string) (string, error) { return "calibredb", nil },
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			gotArgs = append([]string(nil), args...)
			return []byte(`[{"id":1,"title":"Go","authors":"D","formats":["/g.epub"],"tags":[]}]`), nil
		},
	}
	cmd := m.runBrowse()
	if cmd == nil {
		t.Fatal("expected browse cmd")
	}
	msg, ok := cmd().(calibreResultsMsg)
	if !ok || msg.err != nil || !msg.browsed || len(msg.books) != 1 {
		t.Fatalf("msg = %#v", msg)
	}
	if strings.Contains(strings.Join(gotArgs, " "), "--search") {
		t.Fatalf("browse should not pass --search: %v", gotArgs)
	}
}

func TestPickBookSelectEmitsPath(t *testing.T) {
	m := newPickBook(&config.Config{CalibreEnabled: true, CalibrePreferFormat: "epub"})
	path := filepath.Join(t.TempDir(), "book.epub")
	if err := os.WriteFile(path, []byte("epub"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.books = []calibre.Book{
		{ID: 1, Title: "T", Formats: []string{path, "/lib/book.mobi"}},
	}
	m.focus = pickFocusList
	m.cursor = 0
	m, cmd := m.selectBook()
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if m.loadPhase != pickPreparing || m.requestID == 0 {
		t.Fatalf("phase=%v requestID=%d", m.loadPhase, m.requestID)
	}
	msg := cmd()
	picked, ok := msg.(bookPickedMsg)
	if !ok || picked.path != path || picked.requestID != m.requestID {
		t.Fatalf("msg = %#v err=%q", msg, m.errText)
	}
}

func TestPickBookMOBIOnlyShowsError(t *testing.T) {
	m := newPickBook(&config.Config{CalibreEnabled: true})
	m.books = []calibre.Book{
		{ID: 42, Title: "Mobi Only", Formats: []string{"/lib/book.mobi"}},
	}
	m.focus = pickFocusList
	m, cmd := m.selectBook()
	if cmd == nil {
		t.Fatal("selection should resolve asynchronously")
	}
	msg, ok := cmd().(bookPickedMsg)
	if !ok || msg.err == nil {
		t.Fatalf("msg = %#v", msg)
	}
	// Missing on-disk MOBI fails resolution (convert path or no-supported).
	if msg.err.Error() == "" {
		t.Fatal("expected non-empty error")
	}
}

func TestPickBookConversionErrorReturnsToPicker(t *testing.T) {
	app := NewApp(&config.Config{CalibreEnabled: true, TTSVoice: "af_heart"})
	app.screen = screenPickBook
	app.pickBook.requestID = 7
	app.pickBook.loadPhase = pickPreparing
	model, _ := app.Update(bookPickedMsg{err: errors.New("converter failed"), requestID: 7})
	a, ok := model.(App)
	if !ok {
		t.Fatalf("model type %T", model)
	}
	if a.screen != screenPickBook || a.pickBook.loadPhase != pickIdle || a.pickBook.errText != "converter failed" {
		t.Fatalf("picker state = screen %v phase=%v err=%q", a.screen, a.pickBook.loadPhase, a.pickBook.errText)
	}
}

func TestBookPickedMsgFillsAudiobookInput(t *testing.T) {
	app := NewApp(&config.Config{CalibreEnabled: true, TTSVoice: "af_heart"})
	app.screen = screenPickBook
	app.pickBook.requestID = 3
	app.audiobook.input = ""
	model, _ := app.Update(bookPickedMsg{path: "/lib/book.epub", requestID: 3})
	a, ok := model.(App)
	if !ok {
		t.Fatalf("model type %T", model)
	}
	if a.screen != screenAudiobook {
		t.Fatalf("screen = %v", a.screen)
	}
	if a.audiobook.input != "/lib/book.epub" {
		t.Fatalf("input = %q", a.audiobook.input)
	}
}

func TestBookPickedMsgIgnoresStaleRequest(t *testing.T) {
	app := NewApp(&config.Config{CalibreEnabled: true, TTSVoice: "af_heart"})
	app.screen = screenPickBook
	app.pickBook.requestID = 5
	app.audiobook.input = "keep-me"
	model, _ := app.Update(bookPickedMsg{path: "/stale.epub", requestID: 4})
	a := model.(App)
	if a.screen != screenPickBook || a.audiobook.input != "keep-me" {
		t.Fatalf("stale apply: screen=%v input=%q", a.screen, a.audiobook.input)
	}
}

func TestBookPickedMsgIgnoresWhenLeftScreen(t *testing.T) {
	app := NewApp(&config.Config{CalibreEnabled: true, TTSVoice: "af_heart"})
	app.screen = screenAudiobook
	app.pickBook.requestID = 1
	app.pickBook.loadPhase = pickPreparing
	app.audiobook.input = "original"
	model, _ := app.Update(bookPickedMsg{path: "/late.epub", requestID: 1})
	a := model.(App)
	if a.screen != screenAudiobook || a.audiobook.input != "original" {
		t.Fatalf("late apply: screen=%v input=%q", a.screen, a.audiobook.input)
	}
	if a.pickBook.loadPhase != pickPreparing {
		t.Fatalf("late result mutated phase=%v", a.pickBook.loadPhase)
	}
}

func TestPickBookPreparingBlocksSecondEnter(t *testing.T) {
	m := newPickBook(&config.Config{CalibreEnabled: true})
	m.books = []calibre.Book{{ID: 1, Title: "T", Formats: []string{"EPUB"}}}
	m.focus = pickFocusList
	m, cmd1 := m.selectBook()
	if cmd1 == nil {
		t.Fatal("first select")
	}
	firstID := m.requestID
	m, cmd2 := m.selectBook()
	if cmd2 != nil {
		t.Fatal("second select should be ignored while preparing")
	}
	if m.requestID != firstID {
		t.Fatalf("requestID changed to %d", m.requestID)
	}
	// Key enter also blocked.
	m, cmd3 := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd3 != nil {
		t.Fatal("enter should be ignored while busy")
	}
}

func TestPickBookPreparingShowsPreparationStatus(t *testing.T) {
	m := newPickBook(&config.Config{CalibreEnabled: true})
	m.books = []calibre.Book{{ID: 1, Title: "T", Formats: []string{"EPUB"}}}
	m.focus = pickFocusList
	m, cmd := m.selectBook()
	if cmd == nil {
		t.Fatal("expected preparation cmd")
	}
	view := m.View()
	if !strings.Contains(view, "Preparing audiobook input") {
		t.Fatalf("view missing preparation status:\n%s", view)
	}
	if strings.Contains(view, "Searching…") {
		t.Fatalf("view reported search while preparing:\n%s", view)
	}
}

func TestLeavingPickBookCancelsResolve(t *testing.T) {
	cancelled := false
	app := NewApp(&config.Config{CalibreEnabled: true})
	app.screen = screenPickBook
	app.pickBook.loadPhase = pickPreparing
	app.pickBook.resolveCancel = func() { cancelled = true }
	model, _ := app.Update(switchScreenMsg(screenAudiobook))
	a := model.(App)
	if !cancelled {
		t.Fatal("leaving picker did not cancel conversion")
	}
	if a.pickBook.loadPhase != pickIdle {
		t.Fatalf("picker phase = %v, want idle", a.pickBook.loadPhase)
	}
}

func TestAudiobookPickLibrarySwitch(t *testing.T) {
	m := newAudiobook(&config.Config{CalibreEnabled: true, TTSVoice: "af_heart"})
	m.cursor = abFieldPickLibrary
	_, cmd := m.activate()
	if cmd == nil {
		t.Fatal("expected switch cmd")
	}
	msg := cmd()
	if msg != switchScreenMsg(screenPickBook) {
		t.Fatalf("msg = %#v", msg)
	}
}

func TestAudiobookShowsCalibreRowsWhenDisabled(t *testing.T) {
	m := newAudiobook(&config.Config{CalibreEnabled: false, TTSVoice: "af_heart"})
	view := m.View()
	if !strings.Contains(view, "Calibre library") {
		t.Fatalf("missing Calibre library toggle:\n%s", view)
	}
	if !strings.Contains(view, "Pick from library") {
		t.Fatalf("pick row should always be visible under audiobooks:\n%s", view)
	}
	if !strings.Contains(view, "off") {
		t.Fatalf("expected calibre off state:\n%s", view)
	}
	// Pick stays on the form when disabled; activate explains how to enable.
	m.cursor = abFieldPickLibrary
	m, cmd := m.activate()
	if cmd != nil {
		t.Fatal("should not open picker while Calibre is off")
	}
	if m.errText == "" || !strings.Contains(m.errText, "Calibre is off") {
		t.Fatalf("errText = %q", m.errText)
	}
}

func TestAudiobookToggleCalibrePersists(t *testing.T) {
	cfg := &config.Config{CalibreEnabled: false, TTSVoice: "af_heart"}
	m := newAudiobook(cfg)
	var saved *bool
	m.persistCalibre = func(enabled bool) error {
		saved = &enabled
		return nil
	}
	m.cursor = abFieldCalibre
	m, cmd := m.activate()
	if cmd != nil {
		t.Fatalf("unexpected cmd %#v", cmd)
	}
	if saved == nil || !*saved {
		t.Fatalf("persistCalibre got %v, want true", saved)
	}
	if !cfg.CalibreEnabled {
		t.Fatal("cfg.CalibreEnabled should be true after toggle")
	}
	if !strings.Contains(m.message, "on") {
		t.Fatalf("message = %q", m.message)
	}
	// Toggle off again.
	m, _ = m.activate()
	if saved == nil || *saved {
		t.Fatalf("second toggle should save false, got %v", saved)
	}
	if cfg.CalibreEnabled {
		t.Fatal("cfg.CalibreEnabled should be false after second toggle")
	}
}

func TestAudiobookShowsCalibreOnWhenEnabled(t *testing.T) {
	m := newAudiobook(&config.Config{CalibreEnabled: true, TTSVoice: "af_heart"})
	view := m.View()
	if !strings.Contains(view, "Calibre library") || !strings.Contains(view, "on") {
		t.Fatalf("expected calibre on:\n%s", view)
	}
	if !strings.Contains(view, "search Calibre") {
		t.Fatalf("expected pick hint:\n%s", view)
	}
}

func TestPickBookSearchCmdUsesClient(t *testing.T) {
	m := newPickBook(&config.Config{CalibreEnabled: true})
	m.client = calibre.Client{
		LookPath: func(string) (string, error) { return "calibredb", nil },
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`[{"id":1,"title":"Go","authors":"D","formats":["/g.epub"],"tags":[]}]`), nil
		},
	}
	m.query = "go"
	cmd := m.runSearch()
	if cmd == nil {
		t.Fatal("expected search cmd")
	}
	msg := cmd()
	res, ok := msg.(calibreResultsMsg)
	if !ok || res.err != nil || len(res.books) != 1 {
		t.Fatalf("msg = %#v", msg)
	}
}

func TestPickBookEmptyQueryBrowses(t *testing.T) {
	m := newPickBook(&config.Config{CalibreEnabled: true})
	m.client = calibre.Client{
		LookPath: func(string) (string, error) { return "calibredb", nil },
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`[]`), nil
		},
	}
	cmd := m.runSearch()
	if cmd == nil {
		t.Fatal("empty query should browse")
	}
	msg, ok := cmd().(calibreResultsMsg)
	if !ok || !msg.browsed {
		t.Fatalf("msg = %#v, want browsed result", msg)
	}
}

func TestPickBookSearchError(t *testing.T) {
	m := newPickBook(&config.Config{CalibreEnabled: true})
	m.loadPhase = pickSearching
	m, _ = m.Update(calibreResultsMsg{err: errors.New("boom")})
	if m.errText != "boom" || m.loadPhase != pickIdle {
		t.Fatalf("err=%q phase=%v", m.errText, m.loadPhase)
	}
}

func TestPickBookKeyEnterRunsSearch(t *testing.T) {
	m := newPickBook(&config.Config{CalibreEnabled: true})
	m.client = calibre.Client{
		LookPath: func(string) (string, error) { return "calibredb", nil },
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`[]`), nil
		},
	}
	m.editing = true
	m.focus = pickFocusQuery
	m.editBuf = "crypto"
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected search")
	}
	if m.query != "crypto" {
		t.Fatalf("query = %q", m.query)
	}
}
