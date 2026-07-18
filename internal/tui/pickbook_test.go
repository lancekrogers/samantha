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
	msg := cmd()
	picked, ok := msg.(bookPickedMsg)
	if !ok || picked.path != path {
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
	if cmd != nil {
		t.Fatal("should not emit pick for MOBI-only")
	}
	if m.errText == "" || !strings.Contains(m.errText, "supported format") {
		t.Fatalf("errText = %q", m.errText)
	}
}

func TestBookPickedMsgFillsAudiobookInput(t *testing.T) {
	app := NewApp(&config.Config{CalibreEnabled: true, TTSVoice: "af_heart"})
	app.screen = screenPickBook
	app.audiobook.input = ""
	model, _ := app.Update(bookPickedMsg{path: "/lib/book.epub"})
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

func TestAudiobookHidesPickWhenDisabled(t *testing.T) {
	m := newAudiobook(&config.Config{CalibreEnabled: false, TTSVoice: "af_heart"})
	view := m.View()
	if strings.Contains(view, "Pick from library") {
		t.Fatalf("pick row should be hidden:\n%s", view)
	}
	// Navigation skips the pick field.
	if next := m.nextField(abFieldInput); next != abFieldOutDir {
		t.Fatalf("next after input = %d", next)
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

func TestPickBookSearchError(t *testing.T) {
	m := newPickBook(&config.Config{CalibreEnabled: true})
	m, _ = m.Update(calibreResultsMsg{err: errors.New("boom")})
	if m.errText != "boom" || m.searching {
		t.Fatalf("err=%q searching=%v", m.errText, m.searching)
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
