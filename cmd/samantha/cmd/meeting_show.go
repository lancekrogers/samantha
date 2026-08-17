package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/meeting"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

// meetingShowOptions carries one `meeting show` invocation.
type meetingShowOptions struct {
	JSON     bool
	Document bool
}

// meetingShowReport is the `--json` contract: the index entry plus the
// canonical document, so a caller gets the metadata and the notes in one read
// instead of shelling out twice.
type meetingShowReport struct {
	Meeting  meeting.BundleEntry `json:"meeting"`
	Document string              `json:"document"`
}

func newMeetingShowCmd() *cobra.Command {
	var opts meetingShowOptions
	cmd := &cobra.Command{
		Use:   "show <bundle-id|path>",
		Short: "Show one past meeting: its summary and its notes",
		Long: `Show one meeting bundle.

The argument is a bundle id from ` + "`samantha meeting list`" + `, a path to the
.meeting directory, or a path to the meeting.md or events.jsonl inside one.

--document writes the raw meeting.md to stdout and nothing else, so it pipes
into a pager or an editor. It wins over --json.

Examples:
  samantha meeting show weekly-sync-20260816-101500.meeting
  samantha meeting show weekly-sync-20260816-101500.meeting --json
  samantha meeting show ~/meetings/standup-20260816-090000.meeting --document`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		// Read-only, like `meeting list`: config.Load writes migration files.
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadRaw()
			if err != nil {
				return err
			}
			return runMeetingShow(cmd, config.MeetingsDirFrom(cfg), args[0], opts)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&opts.JSON, "json", false, "Emit the meeting and its document as one JSON object on stdout")
	f.BoolVar(&opts.Document, "document", false, "Write the raw meeting.md to stdout (ignores --json)")
	return cmd
}

func runMeetingShow(cmd *cobra.Command, meetingsDir, ref string, opts meetingShowOptions) error {
	entry, ok := meeting.ResolveBundle(cmd.Context(), meetingsDir, ref)
	if !ok {
		return unknownBundleError(cmd.ErrOrStderr(), ref, opts.JSON)
	}
	document, err := readBundleDocument(entry)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	switch {
	case opts.Document:
		_, err := io.WriteString(out, document)
		return err
	case opts.JSON:
		encoder := json.NewEncoder(out)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(meetingShowReport{Meeting: entry, Document: document}); err != nil {
			return fmt.Errorf("meeting show: write json: %w", err)
		}
		return nil
	default:
		writeMeetingShowText(out, entry)
		return nil
	}
}

// readBundleDocument reads the canonical notes. A bundle whose document is not
// written yet is shown without one rather than refused: the counts, the route
// receipt, and the paths are all still true.
func readBundleDocument(entry meeting.BundleEntry) (string, error) {
	raw, err := os.ReadFile(filepath.Join(entry.Bundle, meetinglog.BundleDocumentName))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("meeting show: read %s: %w", meetinglog.BundleDocumentName, err)
	}
	return string(raw), nil
}

// unknownBundleError reports a reference that names no meeting, in the shape
// the caller can read: a JSON object under --json, a sentence otherwise.
func unknownBundleError(stderr io.Writer, ref string, jsonOut bool) error {
	err := fmt.Errorf("meeting: unknown bundle %q", ref)
	if jsonOut {
		fmt.Fprintf(stderr, "{\"error\":%s}\n", mustJSONString(err.Error()))
	}
	return err
}

func mustJSONString(s string) string {
	raw, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(raw)
}

func writeMeetingShowText(w io.Writer, entry meeting.BundleEntry) {
	fmt.Fprintf(w, "%s\n", entry.Description)
	fmt.Fprintf(w, "  started    %s\n", entry.StartedAt.Local().Format("2006-01-02 15:04:05"))
	if !entry.EndedAt.IsZero() {
		fmt.Fprintf(w, "  ended      %s (%s)\n",
			entry.EndedAt.Local().Format("2006-01-02 15:04:05"), compactDuration(entry.DurationSeconds))
	}
	fmt.Fprintf(w, "  state      %s\n", entry.State)
	if entry.Source != "" {
		fmt.Fprintf(w, "  source     %s\n", entry.Source)
	}
	fmt.Fprintf(w, "  counts     %d utterances, %d notes, %d bookmarks, %d errors\n",
		entry.Utterances, entry.Notes, entry.Bookmarks, entry.Errors)
	if entry.SpeakerStatus != "" {
		fmt.Fprintf(w, "  speakers   %s (%d)\n", entry.SpeakerStatus, entry.SpeakerCount)
	}
	if route := routeSummary(entry.Route); route != "" {
		fmt.Fprintf(w, "  route      %s\n", route)
	}
	if entry.AudioFile != "" {
		fmt.Fprintf(w, "  audio      %s\n", entry.AudioFile)
	}
	fmt.Fprintf(w, "  notes      %s\n", entry.Document)
	fmt.Fprintf(w, "  bundle     %s\n", entry.Bundle)
}
