package tui

import (
	"strings"
	"testing"

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
