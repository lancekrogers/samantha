package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/config"
)

// meetingOptions carries the resolved `meeting record` invocation.
type meetingOptions struct {
	Description string
	OutDir      string
	STTProvider string
	NoTUI       bool
	JSON        bool
	StopPhrases []string
}

// defaultStopPhrases end a recording when spoken. Matching is exact
// full-utterance equality after normalization (never substring), mirroring
// internal/app's exitPhrases semantics — a meeting that merely *mentions*
// stopping must not stop.
var defaultStopPhrases = []string{"stop recording", "end meeting", "stop listening"}

// newMeetingCmd builds the `samantha meeting` command group.
func newMeetingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "meeting",
		Short: "Record meeting transcripts (STT only, no assistant)",
	}
	cmd.AddCommand(newMeetingRecordCmd())
	return cmd
}

func newMeetingRecordCmd() *cobra.Command {
	var opts meetingOptions
	cmd := &cobra.Command{
		Use:   "record",
		Short: "Listen and write everything heard to a timestamped log file",
		Long: `Record a meeting transcript: listens continuously (no Brain, no TTS, no
speaker output) and appends one line per utterance to a timestamped log
file, synced per line so a crash never loses what was already heard.

Interactive runs without --description prompt once for a meeting
description; --description, --no-tui, or a non-TTY stdin/stdout skip the
prompt so automation can never hang on it.

On a TTY (and not --json/--no-tui), recording opens a full-screen TUI with
the same live voice EQ as the conversation screen, a scrolling transcript,
and elapsed time. --no-tui and --json keep the plain line-oriented sinks.

Stop with q / Ctrl+C or by saying one of the stop phrases ("stop recording",
"end meeting", "stop listening" — exact phrase, not substring; --stop-phrase
adds more).

Examples:
  samantha meeting record
  samantha meeting record --description "Weekly planning sync"
  samantha meeting record --description "Standup" --out-dir ~/notes/meetings --json
  samantha meeting record --description "CI log" --no-tui`,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			description, cancelled, err := resolveMeetingDescription(
				opts.Description, opts.NoTUI,
				isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd()),
				promptMeetingDescription,
			)
			if err != nil {
				return err
			}
			if cancelled {
				return nil // form dismissed before anything was recorded
			}
			opts.Description = description
			return runMeetingRecord(cmd, opts)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.Description, "description", "", "Meeting description (skips the interactive prompt)")
	f.StringVar(&opts.OutDir, "out-dir", "", "Directory for the log file (default: "+config.MeetingsDir()+")")
	f.StringVar(&opts.STTProvider, "stt-provider", "", "One-shot STT provider override for this recording")
	f.BoolVar(&opts.NoTUI, "no-tui", false, "Skip interactive description prompt and full-screen recorder TUI")
	f.BoolVar(&opts.JSON, "json", false, "Emit one JSON line per utterance plus a final JSON summary on stdout")
	f.StringArrayVar(&opts.StopPhrases, "stop-phrase", nil, "Additional spoken phrase that stops the recording (repeatable)")
	return cmd
}

// resolveMeetingDescription applies the TTY decision tree from the design:
// an explicit description wins; otherwise the prompt runs only when allowed
// AND both stdin and stdout are TTYs (huh reads stdin and draws on stdout —
// a detached process must never block on a form). cancelled reports the user
// dismissing the form, which is a clean exit, not an error.
func resolveMeetingDescription(flag string, noTUI, tty bool, prompt func() (string, error)) (description string, cancelled bool, err error) {
	if s := strings.TrimSpace(flag); s != "" {
		return s, false, nil
	}
	if noTUI || !tty {
		return "meeting", false, nil
	}
	s, err := prompt()
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return "", true, nil
		}
		return "", false, err
	}
	if s = strings.TrimSpace(s); s == "" {
		s = "meeting"
	}
	return s, false, nil
}

func promptMeetingDescription() (string, error) {
	var description string
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().
			Title("Meeting description").
			Placeholder("Weekly planning sync").
			Value(&description),
	))
	if err := form.Run(); err != nil {
		return "", err
	}
	return description, nil
}

// meetingSlug kebab-cases a description for the filename, capped at 60 chars.
func meetingSlug(description string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(description) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 60 {
		s = strings.Trim(s[:60], "-")
	}
	if s == "" {
		return "meeting"
	}
	return s
}

// meetingFilename joins the slug with the codebase's sortable timestamp
// layout (the same one session.generateID uses).
func meetingFilename(description string, now time.Time) string {
	return fmt.Sprintf("%s-%s.log", meetingSlug(description), now.Format("20060102-150405"))
}

// normalizeStopPhrase mirrors internal/app's normalizeCommand: lowercase,
// trim, strip trailing punctuation that STT output carries.
func normalizeStopPhrase(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.TrimSpace(strings.TrimRight(s, ".,!?"))
}

// stopPhraseSet builds the normalized match set from defaults + extras.
func stopPhraseSet(extra []string) map[string]bool {
	set := make(map[string]bool, len(defaultStopPhrases)+len(extra))
	for _, p := range defaultStopPhrases {
		set[normalizeStopPhrase(p)] = true
	}
	for _, p := range extra {
		if n := normalizeStopPhrase(p); n != "" {
			set[n] = true
		}
	}
	return set
}
