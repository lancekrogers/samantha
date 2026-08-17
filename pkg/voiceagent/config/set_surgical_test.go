package config

import (
	"fmt"
	"strings"
	"testing"
)

// fourSpaceConfig is a hand-edited file in the shapes a whole-document
// re-encoder quietly reformats: 4-space nesting, block and inline comments,
// quoted scalars, a sequence of mappings, and a blank line between sections.
const fourSpaceConfig = `# Samantha — hand edited. Indentation and comments are load bearing.
tts_provider: kokoro
brain_provider: "ollama"   # quoted on purpose
vad_silence_duration: 0.5

meeting:
    # Where routed notes land.
    route:
        mode: auto
        default: obsidian
        destinations:
            - id: obsidian
              kind: filesystem

speaker:
    live:
        window_ms: 1500
        mode: indicator
`

// TestSetKeyFileLeavesEveryOtherLineByteIdentical is M1: a write may change the
// lines of the key it was asked to write and nothing else — not the file's
// indentation style, not its comments, not its quoting, not its key order.
func TestSetKeyFileLeavesEveryOtherLineByteIdentical(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		raw     string
		wantErr string              // error code; "" means the write must succeed
		want    func(string) string // the whole file, byte for byte, after the write
	}{
		// Error cases first: a refused write must not touch the file at all.
		{
			name:    "unknown key is refused and nothing is rewritten",
			key:     "vad_silince_duration",
			raw:     "0.8",
			wantErr: CodeUnknownKey,
		},
		{
			name:    "type mismatch is refused and nothing is rewritten",
			key:     "speaker.live.window_ms",
			raw:     "soon",
			wantErr: CodeInvalidValue,
		},
		{
			name:    "enum mismatch is refused and nothing is rewritten",
			key:     "tts_provider",
			raw:     "festival",
			wantErr: CodeInvalidValue,
		},
		{
			name:    "a key another command owns is refused",
			key:     "active_persona",
			raw:     "veronica",
			wantErr: CodeNotEditable,
		},
		{
			name: "an unrelated top-level key leaves the 4-space blocks alone",
			key:  "vad_silence_duration",
			raw:  "0.8",
			want: replaceOnce("vad_silence_duration: 0.5", "vad_silence_duration: 0.8"),
		},
		{
			name: "an inline comment and its spacing survive the line that changed",
			key:  "brain_provider",
			raw:  "claude",
			want: replaceOnce(`brain_provider: "ollama"   # quoted on purpose`,
				"brain_provider: claude   # quoted on purpose"),
		},
		{
			name: "a nested key is rewritten at the file's own indentation",
			key:  "speaker.live.window_ms",
			raw:  "2000",
			want: replaceOnce("        window_ms: 1500", "        window_ms: 2000"),
		},
		{
			name: "a new key joins an existing 4-space block at its siblings' indent",
			key:  "speaker.live.threshold",
			raw:  "0.7",
			want: replaceOnce("        mode: indicator\n", "        mode: indicator\n        threshold: 0.7\n"),
		},
		{
			name: "a new nested section is created with the file's indent step",
			key:  "speaker.meeting.num_speakers",
			raw:  "3",
			want: replaceOnce("        mode: indicator\n",
				"        mode: indicator\n    meeting:\n        num_speakers: 3\n"),
		},
		{
			name: "a new top-level key is appended without disturbing the blocks",
			key:  "agent_name",
			raw:  "Veronica",
			want: func(src string) string { return src + "agent_name: Veronica\n" },
		},
		{
			name: "a list is written with the file's indent step",
			key:  "skills_disabled",
			raw:  `["pdf-fill","calibre"]`,
			want: func(src string) string {
				return src + "skills_disabled:\n    - pdf-fill\n    - calibre\n"
			},
		},
		{
			name: "a new deep key joins its section below the section's last line",
			key:  "meeting.route.body",
			raw:  "notes",
			want: replaceOnce("              kind: filesystem\n", "              kind: filesystem\n        body: notes\n"),
		},
		{
			name: "a deep key that exists is edited where it sits",
			key:  "meeting.route.mode",
			raw:  "off",
			want: replaceOnce("        mode: auto", "        mode: off"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := newInstall(t, fourSpaceConfig)
			_, err := SetKeyFile(tt.key, tt.raw)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("SetKeyFile(%s, %s) succeeded, want %s", tt.key, tt.raw, tt.wantErr)
				}
				if code := setError(t, err).Code; code != tt.wantErr {
					t.Fatalf("code = %q, want %q", code, tt.wantErr)
				}
				if got := readConfig(t, path); got != fourSpaceConfig {
					t.Fatalf("a refused write rewrote the file:\n%s", diffReport(fourSpaceConfig, got))
				}
				return
			}
			if err != nil {
				t.Fatalf("SetKeyFile(%s, %s): %v", tt.key, tt.raw, err)
			}
			want := tt.want(fourSpaceConfig)
			if got := readConfig(t, path); got != want {
				t.Fatalf("file is not byte-identical outside the written key:\n%s", diffReport(want, got))
			}
		})
	}
}

// TestSetKeyFileRoundTripsAfterASurgicalWrite guards the other half of M1: a
// file the writer edited line by line must still load as the value that was
// written.
func TestSetKeyFileRoundTripsAfterASurgicalWrite(t *testing.T) {
	newInstall(t, fourSpaceConfig)
	mustSet(t, "speaker.live.window_ms", "2000")
	mustSet(t, "speaker.meeting.num_speakers", "3")
	mustSet(t, "vad_silence_duration", "0.8")

	cfg, err := LoadRaw()
	if err != nil {
		t.Fatalf("LoadRaw: %v", err)
	}
	if cfg.Speaker.Live.WindowMS != 2000 {
		t.Errorf("speaker.live.window_ms = %d, want 2000", cfg.Speaker.Live.WindowMS)
	}
	if cfg.Speaker.Meeting.NumSpeakers != 3 {
		t.Errorf("speaker.meeting.num_speakers = %d, want 3", cfg.Speaker.Meeting.NumSpeakers)
	}
	if cfg.VADSilenceDuration != 0.8 {
		t.Errorf("vad_silence_duration = %v, want 0.8", cfg.VADSilenceDuration)
	}
}

func replaceOnce(from, to string) func(string) string {
	return func(src string) string {
		out := strings.Replace(src, from, to, 1)
		if out == src {
			panic("test fixture does not contain " + from)
		}
		return out
	}
}

// diffReport names the first differing line of two documents and prints what
// was produced, so a failure reads as a regression rather than a wall of YAML.
func diffReport(want, got string) string {
	wantLines, gotLines := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(wantLines) || i < len(gotLines); i++ {
		w, g := lineAt(wantLines, i), lineAt(gotLines, i)
		if w != g {
			return fmt.Sprintf("first difference at line %d\n  want: %q\n  got:  %q\n--- got ---\n%s", i+1, w, g, got)
		}
	}
	return "--- got ---\n" + got
}

func lineAt(lines []string, i int) string {
	if i < len(lines) {
		return lines[i]
	}
	return "<missing>"
}

// blockConfig holds a key whose value is a literal block. yaml.v3 reports such
// a scalar's line as the line its `|` sits on and says nothing about the body,
// so a writer that trusts the node's line strands the rest of the block in the
// file. Found by self-review against the built binary.
const blockConfig = `compact_prompt: |
  line one
  line two

  # indented, so part of the block and not a comment
tts_provider: kokoro
speaker:
    live:
        window_ms: 1500
`

func TestSetKeyFileReplacesAMultiLineScalarWhole(t *testing.T) {
	tests := []struct {
		name string
		key  string
		raw  string
		want string
	}{
		{
			name: "the block itself is replaced, body and all",
			key:  "compact_prompt",
			raw:  "hello",
			want: "compact_prompt: hello\ntts_provider: kokoro\nspeaker:\n    live:\n        window_ms: 1500\n",
		},
		{
			name: "a key after the block is edited without disturbing it",
			key:  "tts_provider",
			raw:  "qwen3-tts",
			want: strings.Replace(blockConfig, "tts_provider: kokoro", "tts_provider: qwen3-tts", 1),
		},
		{
			name: "a key under the block is edited without disturbing it",
			key:  "speaker.live.window_ms",
			raw:  "2000",
			want: strings.Replace(blockConfig, "        window_ms: 1500", "        window_ms: 2000", 1),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := newInstall(t, blockConfig)
			if _, err := SetKeyFile(tt.key, tt.raw); err != nil {
				t.Fatalf("SetKeyFile(%s, %s): %v", tt.key, tt.raw, err)
			}
			if got := readConfig(t, path); got != tt.want {
				t.Fatalf("multi-line scalar mishandled:\n%s", diffReport(tt.want, got))
			}
		})
	}
}

func TestUnsetKeyFileRemovesAMultiLineScalarWhole(t *testing.T) {
	path := newInstall(t, blockConfig)

	if _, err := UnsetKeyFile("compact_prompt"); err != nil {
		t.Fatalf("UnsetKeyFile: %v", err)
	}
	want := "tts_provider: kokoro\nspeaker:\n    live:\n        window_ms: 1500\n"
	if got := readConfig(t, path); got != want {
		t.Fatalf("block body left behind by unset:\n%s", diffReport(want, got))
	}
}

// A CRLF file keeps its line endings: the lines this writer does not touch keep
// theirs because the split leaves the "\r" on them, and the line it writes has
// to be given the same one or the file comes back with two kinds.
func TestSetKeyFileKeepsTheFilesLineEndings(t *testing.T) {
	source := "tts_provider: kokoro\r\nvad_silence_duration: 0.5\r\nspeaker:\r\n    live:\r\n        window_ms: 1500\r\n"
	path := newInstall(t, source)

	mustSet(t, "vad_silence_duration", "0.8")
	mustSet(t, "speaker.live.threshold", "0.7")

	got := readConfig(t, path)
	if strings.Contains(strings.ReplaceAll(got, "\r\n", ""), "\n") {
		t.Errorf("a bare LF line survived in a CRLF file:\n%q", got)
	}
	want := strings.Replace(source, "vad_silence_duration: 0.5", "vad_silence_duration: 0.8", 1)
	want = strings.Replace(want, "        window_ms: 1500\r\n", "        window_ms: 1500\r\n        threshold: 0.7\r\n", 1)
	if got != want {
		t.Fatalf("CRLF document not preserved:\n%s", diffReport(want, got))
	}
}

// A flow collection lives on one line, so there is no line belonging to a key
// inside it and nothing for a line-by-line writer to edit. Refusing — naming
// the section and the fix — is the honest answer: the alternative is
// re-encoding the document, which is the thing this writer exists to stop.
// A flow *value* is not a flow section and must still be written.
func TestSetKeyFileAndFlowStyle(t *testing.T) {
	const flowConfig = "tts_provider: kokoro\nspeaker: {live: {window_ms: 1500}}\nskills_disabled: []\n"

	t.Run("a key inside a flow section is refused and nothing is rewritten", func(t *testing.T) {
		path := newInstall(t, flowConfig)

		_, err := SetKeyFile("speaker.live.window_ms", "2000")
		setErr := setError(t, err)
		if setErr.Code != CodeWriteFailed {
			t.Fatalf("code = %q, want %q", setErr.Code, CodeWriteFailed)
		}
		if !strings.Contains(setErr.Message, "flow style") || !strings.Contains(setErr.Message, "block style") {
			t.Errorf("message %q does not name the problem and the fix", setErr.Message)
		}
		if got := readConfig(t, path); got != flowConfig {
			t.Errorf("a refused write rewrote the file:\n%s", got)
		}
	})

	t.Run("a flow value is replaced like any other value", func(t *testing.T) {
		path := newInstall(t, flowConfig)

		mustSet(t, "skills_disabled", `["pdf-fill","calibre"]`)

		want := strings.Replace(flowConfig, "skills_disabled: []\n", "skills_disabled:\n  - pdf-fill\n  - calibre\n", 1)
		if got := readConfig(t, path); got != want {
			t.Fatalf("flow value not replaced cleanly:\n%s", diffReport(want, got))
		}
	})

	t.Run("a flow section does not set the file's indent width", func(t *testing.T) {
		// `speaker: {live: {window_ms: 1500}}` puts window_ms at column 18 and
		// live at column 11, which reads as a 7-space step if columns on one
		// line are trusted. Every new list and section would have been written
		// 7 spaces in.
		path := newInstall(t, "tts_provider: kokoro\nspeaker: {live: {window_ms: 1500}}\n")

		mustSet(t, "skills_disabled", `["pdf-fill"]`)

		if got := readConfig(t, path); !strings.Contains(got, "\n  - pdf-fill\n") {
			t.Errorf("indent step taken from a flow collection:\n%q", got)
		}
	})
}
