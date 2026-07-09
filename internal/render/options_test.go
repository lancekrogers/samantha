package render

import "testing"

func TestOptionsValidate(t *testing.T) {
	cases := map[string]struct {
		opts    Options
		wantErr bool
	}{
		"stdin to file":       {Options{Stdin: true, Out: "x.wav", Format: FormatAuto}, false},
		"input to file":       {Options{Input: "a.md", Out: "x.wav", Format: FormatAuto}, false},
		"input to dir":        {Options{Input: "b.epub", OutDir: "out", Format: FormatAuto}, false},
		"stdin + input":       {Options{Stdin: true, Input: "a.md", Out: "x.wav"}, true},
		"no input":            {Options{Out: "x.wav"}, true},
		"no output":           {Options{Stdin: true}, true},
		"out and out-dir":     {Options{Stdin: true, Out: "x.wav", OutDir: "out"}, true},
		"bad format":          {Options{Stdin: true, Out: "x.wav", Format: Format("docx")}, true},
		"stdin epub rejected": {Options{Stdin: true, Out: "x.wav", Format: FormatEPUB}, true},
		"stdin url rejected":  {Options{Stdin: true, Out: "x.wav", Format: FormatURL}, true},
		"negative speed":      {Options{Stdin: true, Out: "x.wav", Format: FormatText, Speed: -1}, true},
		"epub to single file": {Options{Input: "b.epub", Out: "x.wav", Format: FormatAuto}, true},
		"markdown to dir":     {Options{Input: "a.md", OutDir: "out", Format: FormatAuto}, false},
		"html to dir":         {Options{Input: "a.html", OutDir: "out", Format: FormatAuto}, false},
		"url to dir":          {Options{Input: "https://example.com/a", OutDir: "out", Format: FormatAuto}, false},
		"text to dir":         {Options{Input: "a.txt", OutDir: "out", Format: FormatAuto}, true},
		"markdown to file":    {Options{Input: "a.md", Out: "x.wav", Format: FormatAuto}, false},
		"seg cap too small":   {Options{Stdin: true, Out: "x.wav", Format: FormatText, MaxSegmentChars: 50}, true},
		"seg cap ok":          {Options{Stdin: true, Out: "x.wav", Format: FormatText, MaxSegmentChars: 200}, false},
		"bad pause":           {Options{Stdin: true, Out: "x.wav", Format: FormatText, PauseHeading: "soon"}, true},
		"pause ok":            {Options{Stdin: true, Out: "x.wav", Format: FormatText, PauseHeading: "750ms"}, false},
		"bad code blocks":     {Options{Input: "a.md", Out: "x.wav", Format: FormatAuto, CodeBlocks: "summarize"}, true},
		"code blocks read":    {Options{Input: "a.md", Out: "x.wav", Format: FormatAuto, CodeBlocks: "read"}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := tc.opts.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestOptionsResolveFormat(t *testing.T) {
	cases := map[string]Format{
		"a.md":                  FormatMarkdown,
		"a.markdown":            FormatMarkdown,
		"page.html":             FormatHTML,
		"page.htm":              FormatHTML,
		"book.epub":             FormatEPUB,
		"https://example.com/p": FormatURL,
		"http://example.com/p":  FormatURL,
		"notes.txt":             FormatText,
		"noext":                 FormatText,
	}
	for input, want := range cases {
		if got := (Options{Input: input, Format: FormatAuto}).ResolveFormat(); got != want {
			t.Errorf("ResolveFormat(%q) = %q, want %q", input, got, want)
		}
	}
	if got := (Options{Stdin: true, Format: FormatAuto}).ResolveFormat(); got != FormatText {
		t.Errorf("stdin ResolveFormat = %q, want text", got)
	}
	// An explicit format wins over inference.
	if got := (Options{Input: "a.md", Format: FormatHTML}).ResolveFormat(); got != FormatHTML {
		t.Errorf("explicit format = %q, want html", got)
	}
}

func TestOptionsMultiFile(t *testing.T) {
	if (Options{Out: "x.wav"}).MultiFile() {
		t.Error("single --out should not be multi-file")
	}
	if !(Options{OutDir: "out"}).MultiFile() {
		t.Error("--out-dir should be multi-file")
	}
}

func TestValidateRejectsStdinPDF(t *testing.T) {
	opts := Options{Stdin: true, Format: FormatPDF, Out: "x.wav"}
	if err := opts.Validate(); err == nil {
		t.Fatal("expected --format pdf --stdin to be rejected")
	}
}
