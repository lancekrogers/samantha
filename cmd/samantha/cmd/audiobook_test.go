package cmd

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/render"
)

func runAudiobook(t *testing.T, run renderRunner, args ...string) (string, error) {
	t.Helper()
	cmd := newAudiobookCmd(run)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

func TestAudiobookCreateRejectsInvalidInvocations(t *testing.T) {
	cases := map[string]struct {
		args    []string
		wantErr string
	}{
		"missing input":   {args: []string{"create", "--out-dir", "out/book"}, wantErr: "provide an EPUB input path"},
		"missing out-dir": {args: []string{"create", "book.epub"}, wantErr: "provide --out-dir DIR"},
		"markdown input":  {args: []string{"create", "notes.md", "--out-dir", "out/book"}, wantErr: "only EPUB input is supported yet"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := runAudiobook(t, runRenderPlan, tc.args...)
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

	_, err := runAudiobook(t, capture,
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
