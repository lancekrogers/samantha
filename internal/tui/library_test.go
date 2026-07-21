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

func TestLibraryBrowseFoldsResults(t *testing.T) {
	m := newLibrary(&config.Config{CalibreEnabled: true})
	m, _ = m.Update(libraryResultsMsg{
		books: []calibre.Book{
			{ID: 1, Title: "Crypto 101", Authors: []string{"Krol"}, Formats: []string{"/x/c.epub"}},
			{ID: 2, Title: "AI", Authors: []string{"Norvig"}, Formats: []string{"/x/a.pdf"}},
		},
		browsed: true,
	})
	if m.focus != libFocusList || len(m.books) != 2 {
		t.Fatalf("focus=%d books=%d", m.focus, len(m.books))
	}
	view := m.View()
	if !strings.Contains(view, "Crypto 101") {
		t.Fatalf("view missing book:\n%s", view)
	}
	if !strings.Contains(view, "title order") {
		t.Fatalf("expected browse status:\n%s", view)
	}
}

func TestLibraryDisabledOnboardingExplainsCalibre(t *testing.T) {
	m := newLibrary(&config.Config{CalibreEnabled: false})
	view := m.View()
	for _, want := range []string{"What is Calibre?", "free software", "ebook", "press e"} {
		if !strings.Contains(view, want) {
			t.Fatalf("onboarding missing %q:\n%s", want, view)
		}
	}
	// Disabled still probes for calibredb so we can say "found" vs "install".
	if cmd := m.InitCmd(); cmd == nil {
		t.Fatal("InitCmd should probe even when disabled")
	}
}

func TestLibraryEnableFromOnboarding(t *testing.T) {
	cfg := &config.Config{CalibreEnabled: false}
	m := newLibrary(cfg)
	var saved *bool
	m.persistCalibre = func(enabled bool) error {
		saved = &enabled
		return nil
	}
	// Pretend calibredb is present so enabling leaves onboarding.
	m.probed = true
	m.binaryPath = "/Applications/calibre.app/Contents/MacOS/calibredb"
	m, cmd := m.setEnabled(true)
	if saved == nil || !*saved {
		t.Fatalf("persist got %v", saved)
	}
	if !cfg.CalibreEnabled {
		t.Fatal("cfg not enabled")
	}
	if m.needsOnboarding() {
		t.Fatal("should leave onboarding when binary is known present")
	}
	if cmd == nil {
		t.Fatal("expected probe+browse after enable")
	}
}

func TestLibraryOnboardingWhenBinaryMissing(t *testing.T) {
	m := newLibrary(&config.Config{CalibreEnabled: true})
	m.probed = true
	m.binaryErr = calibre.ErrCalibreNotFound
	if !m.needsOnboarding() {
		t.Fatal("enabled without binary should show onboarding")
	}
	view := m.View()
	if !strings.Contains(view, "not found") || !strings.Contains(view, "Install Calibre") {
		t.Fatalf("view:\n%s", view)
	}
	if !strings.Contains(view, "e disable") {
		t.Fatalf("footer should offer disable when enabled:\n%s", view)
	}
}

func TestLibraryOnboardingEDisablesWhenEnabledButMissingBinary(t *testing.T) {
	cfg := &config.Config{CalibreEnabled: true}
	m := newLibrary(cfg)
	m.probed = true
	m.binaryErr = calibre.ErrCalibreNotFound
	var saved *bool
	m.persistCalibre = func(enabled bool) error {
		saved = &enabled
		return nil
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if saved == nil || *saved {
		t.Fatalf("e should disable when enabled-but-missing; saved=%v", saved)
	}
	if cfg.CalibreEnabled {
		t.Fatal("cfg should be disabled after e")
	}
	if !m.needsOnboarding() {
		t.Fatal("still onboarding after disable")
	}
	view := m.View()
	if !strings.Contains(view, "e enable") {
		t.Fatalf("footer should offer enable when off:\n%s", view)
	}
}

func TestLibraryOpenDetailAndBack(t *testing.T) {
	m := newLibrary(&config.Config{CalibreEnabled: true})
	m.books = []calibre.Book{
		{ID: 5, Title: "AI", Authors: []string{"Norvig"}, Tags: []string{"AI"}, Formats: []string{"/a.pdf"}},
	}
	m.focus = libFocusList
	m.cursor = 0
	m.client = calibre.Client{
		LookPath: func(string) (string, error) { return "calibredb", nil },
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`[{"id":5,"title":"AI","authors":"Norvig","formats":["/a.pdf"],"tags":["AI"],"comments":"<p>A textbook.</p>"}]`), nil
		},
	}
	m, cmd := m.openDetail()
	if m.pane != libPaneDetail {
		t.Fatalf("pane=%d", m.pane)
	}
	if cmd == nil {
		t.Fatal("expected metadata cmd")
	}
	msg := cmd()
	det, ok := msg.(libraryDetailMsg)
	if !ok || det.err != nil || det.book.ID != 5 {
		t.Fatalf("msg = %#v", msg)
	}
	m, _ = m.Update(det)
	view := m.View()
	if !strings.Contains(view, "Book details") || !strings.Contains(view, "A textbook.") {
		t.Fatalf("detail view:\n%s", view)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.pane != libPaneBrowse {
		t.Fatalf("expected browse after esc, pane=%d", m.pane)
	}
}

func TestLibrarySendToAudiobook(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.epub")
	if err := os.WriteFile(path, []byte("epub"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newLibrary(&config.Config{CalibreEnabled: true, CalibrePreferFormat: "epub"})
	m.books = []calibre.Book{
		{ID: 1, Title: "T", Formats: []string{path}},
	}
	m.focus = libFocusList
	m.cursor = 0
	m, cmd := m.sendToAudiobook(m.books[0])
	if cmd == nil {
		t.Fatalf("expected cmd, err=%q", m.errText)
	}
	if !m.preparing {
		t.Fatal("expected preparing state during resolve")
	}
	msg := cmd()
	got, ok := msg.(libraryAudiobookMsg)
	if !ok || got.path != path || got.err != nil || got.requestID != m.requestID {
		t.Fatalf("msg = %#v", msg)
	}
}

func TestLibraryAudiobookMsgFillsForm(t *testing.T) {
	app := NewApp(&config.Config{CalibreEnabled: true, TTSVoice: "af_heart"})
	app.screen = screenLibrary
	app.library.requestID = 7
	app.library.preparing = true
	model, _ := app.Update(libraryAudiobookMsg{path: "/lib/book.epub", requestID: 7})
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

func TestLibraryAudiobookMsgIgnoresStaleOrWrongScreen(t *testing.T) {
	app := NewApp(&config.Config{CalibreEnabled: true, TTSVoice: "af_heart"})
	app.screen = screenLauncher
	app.library.requestID = 3
	model, _ := app.Update(libraryAudiobookMsg{path: "/lib/book.epub", requestID: 3})
	a := model.(App)
	if a.screen != screenLauncher {
		t.Fatalf("stale success changed screen to %v", a.screen)
	}

	app = NewApp(&config.Config{CalibreEnabled: true, TTSVoice: "af_heart"})
	app.screen = screenLibrary
	app.library.requestID = 9
	app.library.preparing = true
	model, _ = app.Update(libraryAudiobookMsg{path: "/lib/other.epub", requestID: 1})
	a = model.(App)
	if a.screen != screenLibrary {
		t.Fatalf("mismatched requestID left screen=%v", a.screen)
	}
	if a.audiobook.input == "/lib/other.epub" {
		t.Fatal("stale path should not fill audiobook form")
	}
}

func TestLibraryMOBIOnlyShowsError(t *testing.T) {
	m := newLibrary(&config.Config{CalibreEnabled: true})
	m.books = []calibre.Book{
		{ID: 42, Title: "Mobi Only", Formats: []string{"/lib/book.mobi"}},
	}
	m.focus = libFocusList
	m, cmd := m.sendToAudiobook(m.books[0])
	if cmd == nil {
		t.Fatal("expected async prepare cmd even when convert will fail")
	}
	if !m.preparing {
		t.Fatal("expected preparing during convert/export")
	}
	msg := cmd()
	got, ok := msg.(libraryAudiobookMsg)
	if !ok || got.err == nil {
		t.Fatalf("msg = %#v, want error", msg)
	}
	// Apply like the App handler would.
	app := NewApp(&config.Config{CalibreEnabled: true})
	app.screen = screenLibrary
	app.library = m
	model, _ := app.Update(got)
	a := model.(App)
	if a.screen != screenLibrary {
		t.Fatalf("error should stay on library, screen=%v", a.screen)
	}
	if a.library.errText == "" {
		t.Fatal("expected errText on library after failed prepare")
	}
	if a.library.preparing {
		t.Fatal("preparing should clear after result")
	}
}

func TestFormatExtListBareFormats(t *testing.T) {
	got := formatExtList([]string{"EPUB", "MOBI", "/lib/book.pdf"})
	if got != "epub, mobi, pdf" {
		t.Fatalf("formatExtList = %q", got)
	}
	if formatExtList(nil) != "none" {
		t.Fatalf("empty = %q", formatExtList(nil))
	}
}

func TestLibraryBrowseCmdUsesList(t *testing.T) {
	var gotArgs []string
	m := newLibrary(&config.Config{CalibreEnabled: true})
	m.client = calibre.Client{
		LookPath: func(string) (string, error) { return "calibredb", nil },
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			gotArgs = append([]string{}, args...)
			return []byte(`[{"id":1,"title":"Go","authors":"D","formats":["/g.epub"],"tags":[]}]`), nil
		},
	}
	cmd := m.runBrowse()
	if cmd == nil {
		t.Fatal("expected browse cmd")
	}
	msg := cmd()
	res, ok := msg.(libraryResultsMsg)
	if !ok || res.err != nil || !res.browsed || len(res.books) != 1 {
		t.Fatalf("msg = %#v", msg)
	}
	joined := strings.Join(gotArgs, " ")
	if strings.Contains(joined, "--search") {
		t.Fatalf("browse should omit search: %v", gotArgs)
	}
}

func TestLibrarySearchCmdUsesSearch(t *testing.T) {
	var gotArgs []string
	m := newLibrary(&config.Config{CalibreEnabled: true})
	m.query = "crypto"
	m.client = calibre.Client{
		LookPath: func(string) (string, error) { return "calibredb", nil },
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			gotArgs = append([]string{}, args...)
			return []byte(`[]`), nil
		},
	}
	cmd := m.runQuery()
	if cmd == nil {
		t.Fatal("expected search cmd")
	}
	_ = cmd()
	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "--search crypto") {
		t.Fatalf("search args: %v", gotArgs)
	}
}

func TestLibrarySearchError(t *testing.T) {
	m := newLibrary(&config.Config{CalibreEnabled: true})
	m, _ = m.Update(libraryResultsMsg{err: errors.New("boom")})
	if m.errText != "boom" || m.loading {
		t.Fatalf("err=%q loading=%v", m.errText, m.loading)
	}
}

func TestLibraryIgnoresStaleResults(t *testing.T) {
	m := newLibrary(&config.Config{CalibreEnabled: true})
	m.query = "first"
	_ = m.runQuery()
	firstID := m.requestID
	m.query = "second"
	_ = m.runQuery()
	secondID := m.requestID
	if firstID == secondID {
		t.Fatal("requests should have distinct IDs")
	}

	m, _ = m.Update(libraryResultsMsg{
		requestID: firstID,
		books:     []calibre.Book{{ID: 1, Title: "stale"}},
	})
	if len(m.books) != 0 {
		t.Fatalf("stale result applied: %+v", m.books)
	}

	m, _ = m.Update(libraryResultsMsg{
		requestID: secondID,
		books:     []calibre.Book{{ID: 2, Title: "current"}},
	})
	if len(m.books) != 1 || m.books[0].ID != 2 {
		t.Fatalf("current result missing: %+v", m.books)
	}
}

func TestLibraryIgnoresDetailAfterBack(t *testing.T) {
	m := newLibrary(&config.Config{CalibreEnabled: true})
	m.books = []calibre.Book{{ID: 5, Title: "AI"}}
	m.focus = libFocusList
	var cmd tea.Cmd
	m, cmd = m.openDetail()
	detailID := m.requestID
	if cmd == nil {
		t.Fatal("expected metadata command")
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m, _ = m.Update(libraryDetailMsg{
		requestID: detailID,
		book:      calibre.Book{ID: 5, Title: "AI", Comments: "stale detail"},
	})
	if m.pane != libPaneBrowse || m.detailOK {
		t.Fatalf("stale detail reopened pane: pane=%d detailOK=%v", m.pane, m.detailOK)
	}
}

func TestLibrarySwitchFromAppLoadsBrowse(t *testing.T) {
	app := NewApp(&config.Config{CalibreEnabled: true, TTSVoice: "af_heart"})
	app.width, app.height = 80, 24
	// switchScreen rebuilds library from config; InitCmd probes + browses when enabled.
	model, cmd := app.Update(switchScreenMsg(screenLibrary))
	a, ok := model.(App)
	if !ok {
		t.Fatalf("model type %T", model)
	}
	if a.screen != screenLibrary {
		t.Fatalf("screen = %v", a.screen)
	}
	if cmd == nil {
		t.Fatal("expected InitCmd (probe + browse) on library switch")
	}
}

func TestLibraryProbeMsgUpdatesStatus(t *testing.T) {
	m := newLibrary(&config.Config{CalibreEnabled: false})
	m, _ = m.Update(libraryProbeMsg{path: "/opt/calibre/calibredb"})
	if !m.probed || m.binaryPath == "" || m.binaryErr != nil {
		t.Fatalf("probe fold failed: %+v", m)
	}
	view := m.View()
	if !strings.Contains(view, "/opt/calibre/calibredb") {
		t.Fatalf("view missing binary path:\n%s", view)
	}
}

func TestWrapWords(t *testing.T) {
	lines := wrapWords("one two three four", 10)
	if len(lines) < 2 {
		t.Fatalf("lines = %v", lines)
	}
	if wrapWords("  ", 10) != nil {
		t.Fatal("empty")
	}
}
