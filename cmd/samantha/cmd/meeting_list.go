package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/meeting"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

// `meeting list` is the history the TUI never had: what was recorded on this
// machine, and whether its notes were filed. It reads bundles off disk and
// derives nothing of its own — the same index GET /v1/meetings serves.

// meetingListOptions carries one `meeting list` invocation.
type meetingListOptions struct {
	JSON    bool
	Limit   int
	Since   string
	Pending bool
}

// meetingListReport is the `--json` contract. It is the wire's index response
// minus live_id: the CLI reads disk, and disk cannot know what is recording.
type meetingListReport struct {
	MeetingsDir string                `json:"meetings_dir"`
	Count       int                   `json:"count"`
	Truncated   bool                  `json:"truncated"`
	Meetings    []meeting.BundleEntry `json:"meetings"`
}

func newMeetingListCmd() *cobra.Command {
	var opts meetingListOptions
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List past meeting bundles, newest first",
		Long: `List the meeting bundles under the meetings directory, newest first.

Each row reports what was recorded and where the notes went. --pending narrows
the list to meetings whose route is still undelivered and would be retried by
` + "`samantha meeting sweep`" + `.

--since accepts an RFC3339 timestamp or a lookback window (14d, 48h, 90m).

An empty or missing meetings directory lists nothing and exits 0 — no history
is not an error.

Examples:
  samantha meeting list
  samantha meeting list --json --limit 20
  samantha meeting list --since 14d --pending`,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		// Read-only: config.Load applies the persona overlay and writes
		// migration files, which a listing has no business doing.
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.LoadRaw()
			if err != nil {
				return err
			}
			return runMeetingList(cmd, config.MeetingsDirFrom(cfg), opts)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&opts.JSON, "json", false, "Emit the listing as one JSON object on stdout")
	f.IntVar(&opts.Limit, "limit", 0, "Maximum meetings to list (default 200, max 1000)")
	f.StringVar(&opts.Since, "since", "", "Only meetings started since this time (RFC3339 or a window like 14d)")
	f.BoolVar(&opts.Pending, "pending", false, "Only meetings whose route is still undelivered")
	return cmd
}

func runMeetingList(cmd *cobra.Command, meetingsDir string, opts meetingListOptions) error {
	indexOpts := meeting.IndexOptions{Limit: opts.Limit}
	if opts.Since != "" {
		since, err := parseSince(opts.Since, time.Now())
		if err != nil {
			return err
		}
		indexOpts.Since = since
	}
	entries, truncated, err := meeting.Index(cmd.Context(), meetingsDir, indexOpts)
	if err != nil {
		return err
	}
	if opts.Pending {
		entries = pendingOnly(entries)
	}
	if opts.JSON {
		return writeMeetingListJSON(cmd.OutOrStdout(), meetingsDir, entries, truncated)
	}
	writeMeetingListText(cmd.OutOrStdout(), meetingsDir, entries, truncated)
	return nil
}

// pendingOnly keeps the meetings the sweep would retry right now.
func pendingOnly(entries []meeting.BundleEntry) []meeting.BundleEntry {
	kept := make([]meeting.BundleEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Route != nil && entry.Route.Retryable {
			kept = append(kept, entry)
		}
	}
	return kept
}

func writeMeetingListJSON(w io.Writer, meetingsDir string, entries []meeting.BundleEntry, truncated bool) error {
	if entries == nil {
		entries = []meeting.BundleEntry{}
	}
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(meetingListReport{
		MeetingsDir: meetingsDir,
		Count:       len(entries),
		Truncated:   truncated,
		Meetings:    entries,
	}); err != nil {
		return fmt.Errorf("meeting list: write json: %w", err)
	}
	return nil
}

func writeMeetingListText(w io.Writer, meetingsDir string, entries []meeting.BundleEntry, truncated bool) {
	if len(entries) == 0 {
		fmt.Fprintf(w, "No meetings in %s\n", meetingsDir)
		return
	}
	for _, entry := range entries {
		fmt.Fprintln(w, meetingListLine(entry))
	}
	if truncated {
		fmt.Fprintln(w, "… more meetings not shown (raise --limit)")
	}
}

// meetingListLine renders one row: when, how long, what, who, and where the
// notes went — the four questions a history list exists to answer.
func meetingListLine(entry meeting.BundleEntry) string {
	fields := []string{
		entry.StartedAt.Local().Format("2006-01-02 15:04"),
		compactDuration(entry.DurationSeconds),
		entry.Description,
	}
	if entry.SpeakerCount == 1 {
		fields = append(fields, "1 speaker")
	} else if entry.SpeakerCount > 1 {
		fields = append(fields, fmt.Sprintf("%d speakers", entry.SpeakerCount))
	}
	if route := routeSummary(entry.Route); route != "" {
		fields = append(fields, route)
	}
	// An unfinished bundle is stated, never quietly listed as if complete.
	if entry.State != meeting.BundleStateReady {
		fields = append(fields, "["+entry.State+"]")
	}
	return strings.Join(fields, "  ")
}

// routeSummary is the "where did the notes go" half of a row.
func routeSummary(route *meeting.RouteStatus) string {
	if route == nil || route.Status == meeting.RouteStatusNone {
		return ""
	}
	dest := route.DestinationID
	if dest == "" {
		dest = "(unknown destination)"
	}
	if route.Status == meeting.RouteStatusFailed && route.Attempts > 0 {
		return fmt.Sprintf("→ %s (failed, %d attempt(s))", dest, route.Attempts)
	}
	return fmt.Sprintf("→ %s (%s)", dest, route.Status)
}

// compactDuration renders a recording length the way a person reads it.
func compactDuration(seconds int64) string {
	if seconds <= 0 {
		return "—"
	}
	d := time.Duration(seconds) * time.Second
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// parseSince accepts an absolute RFC3339 timestamp or a lookback window. The
// window form is what anyone actually types ("what happened this fortnight"),
// and `d` is spelled out because Go's duration parser stops at hours.
func parseSince(raw string, now time.Time) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if at, err := time.Parse(time.RFC3339, raw); err == nil {
		return at, nil
	}
	if days, ok := strings.CutSuffix(raw, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n < 0 {
			return time.Time{}, fmt.Errorf("meeting list: --since %q is neither RFC3339 nor a window like 14d", raw)
		}
		return now.Add(-time.Duration(n) * 24 * time.Hour), nil
	}
	window, err := time.ParseDuration(raw)
	if err != nil || window < 0 {
		return time.Time{}, fmt.Errorf("meeting list: --since %q is neither RFC3339 nor a window like 14d", raw)
	}
	return now.Add(-window), nil
}
