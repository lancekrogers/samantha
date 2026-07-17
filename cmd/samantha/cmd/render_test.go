package cmd

import (
	"bytes"
	"testing"
)

func runRender(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newRenderCmd(runRenderPlan, fixedConfig("af_heart", 1))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

func TestRenderCmdRejectsInvalidFlagCombos(t *testing.T) {
	cases := map[string][]string{
		"no input":        {"--out", "x.wav"},
		"no output":       {"--stdin"},
		"stdin + input":   {"--stdin", "a.md", "--out", "x.wav"},
		"out and out-dir": {"--stdin", "--out", "x.wav", "--out-dir", "out"},
		"bad format":      {"--stdin", "--out", "x.wav", "--format", "docx"},
		"stdin epub":      {"--stdin", "--out", "x.wav", "--format", "epub"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := runRender(t, args...); err == nil {
				t.Fatalf("expected validation error for args %v", args)
			}
		})
	}
}

func TestRenderCmdValidPlan(t *testing.T) {
	out, err := runRender(t, "--stdin", "--out", "notes.wav", "--voice", "af_heart")
	if err != nil {
		t.Fatalf("valid render error = %v", err)
	}
	for _, want := range []string{"Render plan", "stdin", "notes.wav", "af_heart"} {
		if !contains(out, want) {
			t.Errorf("plan output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderCmdAutoDetectsFormat(t *testing.T) {
	out, err := runRender(t, "book.epub", "--out-dir", "out/book")
	if err != nil {
		t.Fatalf("render error = %v", err)
	}
	if !contains(out, "epub") || !contains(out, "multi-file") {
		t.Errorf("plan should auto-detect epub + multi-file:\n%s", out)
	}
}

func TestRenderCmdAllowsStructuredOutDir(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"markdown out-dir", []string{"article.md", "--out-dir", "out/article"}},
		{"html out-dir", []string{"page.html", "--out-dir", "out/page"}},
		{"url out-dir", []string{"https://example.com/a", "--out-dir", "out/a"}},
		{"markdown out single", []string{"article.md", "--out", "out/article.wav"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := runRender(t, tc.args...); err != nil {
				t.Fatalf("expected valid plan for %v, got %v", tc.args, err)
			}
		})
	}
}

func TestRenderCmdRejectsTextOutDir(t *testing.T) {
	if _, err := runRender(t, "notes.txt", "--out-dir", "out/notes"); err == nil {
		t.Fatal("expected validation error for text --out-dir")
	}
}
