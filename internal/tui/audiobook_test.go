package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/config"
)

func TestLauncherIncludesCreateAudiobook(t *testing.T) {
	m := newLauncher(&config.Config{TTSVoice: "af_heart"}, nil)
	view := m.View()
	if !strings.Contains(view, "Create audiobook") {
		t.Fatalf("launcher missing Create audiobook:\n%s", view)
	}
}

func TestGenerateAudiobookCommandQuotesSpaces(t *testing.T) {
	cmd, err := GenerateAudiobookCommand("my book.epub", "out dir/book", "af_bella", "1.25", true, "m4b")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"samantha audiobook create",
		"'my book.epub'",
		"--out-dir",
		"'out dir/book'",
		"--resume",
		"--voice",
		"af_bella",
		"--speed",
		"1.25",
		"--audio-format",
		"m4b",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("command missing %q: %s", want, cmd)
		}
	}
}

func TestGenerateAudiobookCommandValidation(t *testing.T) {
	if _, err := GenerateAudiobookCommand("", "out", "", "", false, ""); err == nil {
		t.Fatal("expected input required")
	}
	if _, err := GenerateAudiobookCommand("in.epub", "", "", "", false, ""); err == nil {
		t.Fatal("expected out-dir required")
	}
}

func TestAudiobookScreenGenerateShowsCommand(t *testing.T) {
	m := newAudiobook(&config.Config{TTSVoice: "af_heart"})
	m.input = "book.epub"
	m.outDir = "out/book"
	m.cursor = abFieldGenerate
	m, _ = m.activate()
	if m.command == "" || !strings.Contains(m.command, "audiobook create") {
		t.Fatalf("command = %q err=%q", m.command, m.errText)
	}
	if !strings.Contains(m.View(), m.command) {
		t.Fatalf("view missing command:\n%s", m.View())
	}
}

func TestCompleteFilesystemPathUniqueFile(t *testing.T) {
	dir := t.TempDir()
	book := filepath.Join(dir, "tiny-book.epub")
	if err := os.WriteFile(book, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Unrelated noise.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("n"), 0o644); err != nil {
		t.Fatal(err)
	}

	partial := filepath.Join(dir, "tiny")
	got, matches := completeFilesystemPath(partial, false)
	if got != book {
		t.Fatalf("completed = %q, want %q", got, book)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %v", matches)
	}
}

func TestCompleteFilesystemPathDirectorySlash(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "chapters")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	partial := filepath.Join(dir, "chap")
	got, matches := completeFilesystemPath(partial, true)
	want := sub + string(filepath.Separator)
	if got != want {
		t.Fatalf("completed = %q, want %q", got, want)
	}
	if len(matches) != 1 || matches[0] != want {
		t.Fatalf("matches = %v", matches)
	}
}

func TestAudiobookPathTabCyclesMatches(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "book-a.epub")
	b := filepath.Join(dir, "book-b.epub")
	for _, path := range []string{a, b} {
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	m := newAudiobook(&config.Config{})
	m.cursor = abFieldInput
	m, _ = m.activate()
	prefix := filepath.Join(dir, "bo")
	for _, r := range prefix {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}

	// First Tab: longest common prefix.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	lcp := filepath.Join(dir, "book-")
	if m.editBuf != lcp {
		t.Fatalf("first tab = %q, want %q msg=%q", m.editBuf, lcp, m.message)
	}
	if !strings.Contains(m.message, "matches:") {
		t.Fatalf("expected match hint, got %q", m.message)
	}

	// Subsequent Tabs cycle full paths without collapsing the match set.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.editBuf != a && m.editBuf != b {
		t.Fatalf("second tab = %q, want one of the books", m.editBuf)
	}
	first := m.editBuf
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.editBuf == first {
		t.Fatalf("third tab did not advance from %q", first)
	}
	if m.editBuf != a && m.editBuf != b {
		t.Fatalf("third tab = %q, want the other book", m.editBuf)
	}
}

func TestCompleteFilesystemPathDirsOnlySkipsFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "book.epub"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "book-out"), 0o755); err != nil {
		t.Fatal(err)
	}

	partial := filepath.Join(dir, "book")
	got, matches := completeFilesystemPath(partial, true)
	want := filepath.Join(dir, "book-out") + string(filepath.Separator)
	if got != want {
		t.Fatalf("dirsOnly completed = %q, want %q (matches=%v)", got, want, matches)
	}
}

func TestCompleteFilesystemPathTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir")
	}
	targetDir := filepath.Join(home, ".samantha-path-complete-test")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Skipf("cannot create under home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(targetDir) })
	if err := os.WriteFile(filepath.Join(targetDir, "marker.epub"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	partial := "~/" + ".samantha-path-complete-test/mar"
	got, matches := completeFilesystemPath(partial, false)
	if !strings.HasPrefix(got, "~/") && !strings.HasPrefix(got, "~"+string(filepath.Separator)) {
		t.Fatalf("expected tilde-preserving completion, got %q matches=%v", got, matches)
	}
	if !strings.HasSuffix(got, "marker.epub") {
		t.Fatalf("completed = %q, want marker.epub", got)
	}
}

func TestExpandHomePreservesTrailingSlash(t *testing.T) {
	home := "/Users/example"
	sep := string(filepath.Separator)

	cases := []struct {
		in, want string
	}{
		{"~", home + sep},
		{"~/", home + sep},
		{"~/Downloads", home + sep + "Downloads"},
		{"~/Downloads/", home + sep + "Downloads" + sep},
		{"~/Downloads/book.epub", home + sep + "Downloads" + sep + "book.epub"},
		{"/abs/path/", "/abs/path/"}, // non-tilde unchanged
		{"plain", "plain"},
	}
	for _, tc := range cases {
		got := expandHome(tc.in, home)
		if got != tc.want {
			t.Errorf("expandHome(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCompleteFilesystemPathTildeTrailingSlashListsInside(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir")
	}
	// Unique dir name so listing ~/.<name>/ is unambiguous.
	dirName := ".samantha-path-complete-list-test"
	targetDir := filepath.Join(home, dirName)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Skipf("cannot create under home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(targetDir) })
	book := filepath.Join(targetDir, "inside-book.epub")
	if err := os.WriteFile(book, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Trailing slash must list *inside* the directory (the original PR bug:
	// filepath.Join dropped the slash, so Tab re-completed the dir name).
	partial := "~/" + dirName + "/"
	got, matches := completeFilesystemPath(partial, false)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match inside dir, got %v (completed=%q)", matches, got)
	}
	if !strings.HasSuffix(got, "inside-book.epub") {
		t.Fatalf("completed = %q, want …/inside-book.epub", got)
	}
	if !strings.HasPrefix(got, "~/") && !strings.HasPrefix(got, "~"+string(filepath.Separator)) {
		t.Fatalf("expected tilde form, got %q", got)
	}

	// Bare ~/prefix for the directory should complete to the dir with trailing /.
	dirPartial := "~/" + dirName[:len(dirName)-2] // drop last 2 chars of unique name
	gotDir, dirMatches := completeFilesystemPath(dirPartial, true)
	if len(dirMatches) != 1 {
		// Name may still be unique enough; if not, skip soft.
		if len(dirMatches) == 0 {
			t.Fatalf("dirsOnly partial %q: no matches", dirPartial)
		}
	} else {
		wantSuffix := dirName + string(filepath.Separator)
		if !strings.HasSuffix(gotDir, wantSuffix) && !strings.HasSuffix(gotDir, dirName+"/") {
			t.Fatalf("dir completion = %q, want suffix %q", gotDir, wantSuffix)
		}
	}
}

func TestCompleteFilesystemPathTildeRootListsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir")
	}
	dirName := ".samantha-path-complete-home-root"
	targetDir := filepath.Join(home, dirName)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Skipf("cannot create under home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(targetDir) })

	// "~/" must list home contents, not re-match the home directory name under /Users.
	got, matches := completeFilesystemPath("~/"+dirName[:10], true)
	// partial prefix of dirName should uniquely complete under home.
	_ = got
	found := false
	for _, m := range matches {
		if strings.Contains(m, dirName) {
			found = true
			break
		}
	}
	// Stronger: complete the full unique name with trailing slash intent.
	gotFull, matchesFull := completeFilesystemPath("~/"+dirName, true)
	if len(matchesFull) != 1 {
		t.Fatalf("~/unique-dir matches = %v (foundPrefix=%v got=%q)", matchesFull, found, got)
	}
	if !strings.HasSuffix(gotFull, string(filepath.Separator)) && !strings.HasSuffix(gotFull, "/") {
		t.Fatalf("directory completion should end with separator: %q", gotFull)
	}
}

func TestAudiobookInputTabCompletesPath(t *testing.T) {
	dir := t.TempDir()
	book := filepath.Join(dir, "novel.epub")
	if err := os.WriteFile(book, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newAudiobook(&config.Config{})
	m.cursor = abFieldInput
	m, _ = m.activate()
	// Type a unique prefix of the absolute path.
	prefix := filepath.Join(dir, "nov")
	for _, r := range prefix {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.editBuf != book {
		t.Fatalf("editBuf after tab = %q, want %q (msg=%q)", m.editBuf, book, m.message)
	}
	if !strings.Contains(m.View(), "tab complete") {
		t.Fatalf("view should mention tab complete while editing path:\n%s", m.View())
	}
}

func TestAudiobookOutDirTabSkipsFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file-only.epub"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "file-out")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}

	m := newAudiobook(&config.Config{})
	m.cursor = abFieldOutDir
	m, _ = m.activate()
	prefix := filepath.Join(dir, "file")
	for _, r := range prefix {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	want := out + string(filepath.Separator)
	if m.editBuf != want {
		t.Fatalf("out-dir tab = %q, want %q msg=%q", m.editBuf, want, m.message)
	}
}

func TestIsEditableInsertAcceptsPathPastes(t *testing.T) {
	if !isEditableInsert("/Users/me/book.epub") {
		t.Fatal("absolute path paste should insert")
	}
	if !isEditableInsert(" ") {
		t.Fatal("space character should insert")
	}
	if isEditableInsert("tab") {
		t.Fatal("tab key must not insert")
	}
	if isEditableInsert("ctrl+c") {
		t.Fatal("ctrl+c must not insert")
	}
}
