package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/calibre"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/render"
)

func runAudiobook(t *testing.T, run renderRunner, load configLoader, args ...string) (string, error) {
	t.Helper()
	cmd := newAudiobookCmd(run, load)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

// fixedConfig returns a configLoader with a deterministic voice/speed, so
// tests never touch the real config file.
func fixedConfig(voice string, speed float64) configLoader {
	return func() (*config.Config, error) {
		return &config.Config{TTSVoice: voice, SpeechSpeed: speed}, nil
	}
}

func fixedQwenConfig() configLoader {
	return func() (*config.Config, error) {
		return &config.Config{
			TTSProvider: "qwen3-tts", QwenTTSMode: "customvoice",
			QwenTTSVoice: "Vivian", QwenTTSLanguage: "Auto",
		}, nil
	}
}

// forbidRender returns a renderRunner that fails the test if invoked; preview
// must stay read-only.
func forbidRender(t *testing.T) renderRunner {
	return func(*cobra.Command, render.Options) error {
		t.Fatal("preview must not invoke the render runner")
		return nil
	}
}

func TestAudiobookCreateRejectsInvalidInvocations(t *testing.T) {
	cases := map[string]struct {
		args    []string
		wantErr string
	}{
		"missing input":   {args: []string{"create", "--out-dir", "out/book"}, wantErr: "provide an EPUB or PDF input path"},
		"missing out-dir": {args: []string{"create", "book.epub"}, wantErr: "provide --out-dir DIR"},
		"markdown input":  {args: []string{"create", "notes.md", "--out-dir", "out/book"}, wantErr: "only EPUB or PDF input is supported"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := runAudiobook(t, runRenderPlan, fixedConfig("af_heart", 1), tc.args...)
			if err == nil {
				t.Fatalf("expected validation error for args %v", tc.args)
			}
			if !contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q should contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestAudiobookCreateMapsToRenderOptions(t *testing.T) {
	var got render.Options
	capture := func(cmd *cobra.Command, opts render.Options) error {
		got = opts
		return nil
	}

	_, err := runAudiobook(t, capture, fixedConfig("af_heart", 1),
		"create", "book.epub",
		"--out-dir", "out/book",
		"--resume", "--voice", "af_heart", "--speed", "1.2",
		"--audio-format", "mp3", "--encoder", "ffmpeg",
		"--json", "--manifest", "m.json", "--overwrite")
	if err != nil {
		t.Fatalf("audiobook create error = %v", err)
	}

	want := render.Options{
		Input:       "book.epub",
		Format:      render.FormatEPUB,
		OutDir:      "out/book",
		Voice:       "af_heart",
		Speed:       1.2,
		Manifest:    "m.json",
		JSON:        true,
		Resume:      true,
		Overwrite:   true,
		AudioFormat: "mp3",
		EncoderBin:  "ffmpeg",
	}
	if got != want {
		t.Errorf("mapped options = %+v, want %+v", got, want)
	}
}

func TestAudiobookCreateMapsQwenVoiceAndLanguage(t *testing.T) {
	var got render.Options
	capture := func(_ *cobra.Command, opts render.Options) error {
		got = opts
		return nil
	}
	_, err := runAudiobook(t, capture, fixedQwenConfig(),
		"create", "book.epub", "--out-dir", "out/book",
		"--voice", "Ryan", "--language", "English")
	if err != nil {
		t.Fatal(err)
	}
	if got.Voice != "Ryan" || got.Language != "English" {
		t.Fatalf("Qwen render options = voice %q language %q, want Ryan/English", got.Voice, got.Language)
	}
}

func TestAudiobookPreviewRejectsInvalidInvocations(t *testing.T) {
	cases := map[string]struct {
		args    []string
		wantErr string
	}{
		"missing input":   {args: []string{"preview", "--out-dir", "out/book"}, wantErr: "provide an EPUB or PDF input path"},
		"missing out-dir": {args: []string{"preview", "book.epub"}, wantErr: "audiobook preview: provide --out-dir DIR"},
		"markdown input":  {args: []string{"preview", "notes.md", "--out-dir", "out/book"}, wantErr: "only EPUB or PDF input is supported"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := runAudiobook(t, forbidRender(t), fixedConfig("af_heart", 1), tc.args...)
			if err == nil {
				t.Fatalf("expected validation error for args %v", tc.args)
			}
			if !contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q should contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestAudiobookPreviewReportsResolvedPlan(t *testing.T) {
	out, err := runAudiobook(t, forbidRender(t), fixedConfig("af_heart", 1.2),
		"preview", "book.epub", "--out-dir", "out/book", "--resume")
	if err != nil {
		t.Fatalf("audiobook preview error = %v", err)
	}
	for _, want := range []string{
		"epub",
		"book.epub",
		"out/book",
		"out/book/manifest.json",
		"af_heart",
		"1.2",
		"resume:   true",
		"nothing was rendered",
	} {
		if !contains(out, want) {
			t.Errorf("preview output missing %q:\n%s", want, out)
		}
	}
}

func TestAudiobookPreviewRenderCommandQuotesPaths(t *testing.T) {
	out, err := runAudiobook(t, forbidRender(t), fixedConfig("", 0),
		"preview", "my book.epub", "--out-dir", "out dir/book",
		"--resume", "--audio-format", "mp3")
	if err != nil {
		t.Fatalf("audiobook preview error = %v", err)
	}
	want := "samantha render 'my book.epub' --out-dir 'out dir/book' --resume --audio-format mp3"
	if !contains(out, want) {
		t.Errorf("preview output missing command %q:\n%s", want, out)
	}
}

func TestAudiobookPreviewJSONEmitsStableFields(t *testing.T) {
	out, err := runAudiobook(t, forbidRender(t), fixedConfig("af_heart", 1),
		"preview", "book.epub", "--out-dir", "out/book", "--voice", "bf_alice", "--json")
	if err != nil {
		t.Fatalf("audiobook preview error = %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("preview --json output is not valid JSON: %v\n%s", err, out)
	}
	want := map[string]any{
		"input":          "book.epub",
		"format":         "epub",
		"output_dir":     "out/book",
		"manifest":       "out/book/manifest.json",
		"voice":          "bf_alice",
		"language":       "",
		"speed":          1.0,
		"resume":         false,
		"audio_format":   "",
		"encoder":        "",
		"render_command": "samantha render book.epub --out-dir out/book --voice bf_alice --speed 1 --json",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("preview JSON = %#v, want %#v", got, want)
	}
}

func TestAudiobookFromLibraryDisabled(t *testing.T) {
	load := func() (*config.Config, error) {
		return &config.Config{CalibreEnabled: false}, nil
	}
	_, err := runAudiobook(t, forbidRender(t), load,
		"create", "--from-library", "Crypto 101", "--out-dir", "out/x")
	if err == nil || !contains(err.Error(), "disabled") {
		t.Fatalf("err = %v", err)
	}
}

func TestAudiobookFromLibraryMutualExclusive(t *testing.T) {
	load := func() (*config.Config, error) {
		return &config.Config{CalibreEnabled: true}, nil
	}
	_, err := runAudiobook(t, forbidRender(t), load,
		"create", "book.epub", "--from-library", "Crypto", "--out-dir", "out/x")
	if err == nil || !contains(err.Error(), "mutually exclusive") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveFromLibraryPathSubstitution(t *testing.T) {
	load := func() (*config.Config, error) {
		return &config.Config{
			CalibreEnabled:      true,
			CalibrePreferFormat: "epub",
		}, nil
	}
	// Without a real/fake calibredb this will fail at Resolve — assert clear error.
	_, err := runAudiobook(t, forbidRender(t), load,
		"create", "--from-library", "zzzz-no-such-book-xyz", "--out-dir", "out/x")
	if err == nil {
		t.Fatal("expected resolve error")
	}
}

func TestResolveLibraryBookSubstitutesPath(t *testing.T) {
	epubPath := filepath.Join(t.TempDir(), "Crypto 101.epub")
	if err := os.WriteFile(epubPath, []byte("epub"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := calibre.Client{
		Prefer:   "epub",
		LookPath: func(string) (string, error) { return "calibredb", nil },
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(fmt.Sprintf(`[{"id":7,"title":"Crypto 101","authors":"Krol","formats":[%q,"/lib/Crypto 101.mobi"],"tags":[]}]`, epubPath)), nil
		},
	}
	path, format, err := resolveLibraryBook(context.Background(), client, "Crypto 101")
	if err != nil {
		t.Fatal(err)
	}
	if path != epubPath || format != render.Format("epub") {
		t.Fatalf("path=%q format=%q", path, format)
	}
}

func TestResolveLibraryBookMOBIOnly(t *testing.T) {
	client := calibre.Client{
		LookPath: func(string) (string, error) { return "calibredb", nil },
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`[{"id":1,"title":"M","authors":"A","formats":["/m.mobi"],"tags":[]}]`), nil
		},
	}
	_, _, err := resolveLibraryBook(context.Background(), client, "M")
	if err == nil || !contains(err.Error(), "supported format") {
		t.Fatalf("err = %v", err)
	}
}
