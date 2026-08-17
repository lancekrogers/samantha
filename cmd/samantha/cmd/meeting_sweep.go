package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/meeting"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

// `meeting sweep` is the retry loop the TUI runs at launch, exposed so any
// front end can trigger it and report what happened. Delivery is at-least-once
// by design: the durable route_plan / routed / route_failed events on each
// bundle are the record, and this command only drives and reports them.

// outcomePending marks a bundle a dry run would have retried.
const outcomePending = "pending"

type meetingSweepOptions struct {
	JSON   bool
	DryRun bool
}

// meetingSweepResult is one bundle the sweep touched.
type meetingSweepResult struct {
	Bundle        string `json:"bundle"`
	ID            string `json:"id"`
	DestinationID string `json:"destination_id"`
	Outcome       string `json:"outcome"`
	Detail        string `json:"detail"`
	Error         string `json:"error"`
}

// meetingSweepReport is the `--json` contract.
type meetingSweepReport struct {
	Attempted int                  `json:"attempted"`
	Routed    int                  `json:"routed"`
	Failed    int                  `json:"failed"`
	DryRun    bool                 `json:"dry_run,omitempty"`
	Results   []meetingSweepResult `json:"results"`
}

func newMeetingSweepCmd() *cobra.Command {
	var opts meetingSweepOptions
	cmd := &cobra.Command{
		Use:   "sweep",
		Short: "Retry meeting notes whose routing never completed",
		Long: `Deliver meeting notes that were planned but never filed.

A bundle qualifies when it carries a route_plan, has finished (session_end),
has no routed event, has fewer than 3 recorded failures, and ended within the
last 14 days — the same bundles ` + "`samantha meeting list --pending`" + ` shows.

--dry-run reports what would be retried and files nothing.

Individual route failures do not fail the command: the failure is recorded on
the bundle and reported in the payload, and the next sweep tries again. Only a
sweep that could not run at all (unreadable config or meetings directory)
exits non-zero.

Examples:
  samantha meeting sweep
  samantha meeting sweep --json
  samantha meeting sweep --dry-run`,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		// Read-only config entry: routing reads meeting.route.*, and
		// config.Load would apply the persona overlay and write to disk.
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.LoadRaw()
			if err != nil {
				return err
			}
			router := meeting.NewDefaultRouter(meeting.FromConfig(cfg))
			return runMeetingSweep(cmd, router, config.MeetingsDirFrom(cfg), opts)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&opts.JSON, "json", false, "Emit the sweep report as one JSON object on stdout")
	f.BoolVar(&opts.DryRun, "dry-run", false, "Report what would be retried without filing anything")
	return cmd
}

func runMeetingSweep(cmd *cobra.Command, router *meeting.Router, meetingsDir string, opts meetingSweepOptions) error {
	report, err := sweepReport(cmd, router, meetingsDir, opts)
	if err != nil {
		return err
	}
	if opts.JSON {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(report); err != nil {
			return fmt.Errorf("meeting sweep: write json: %w", err)
		}
		return nil
	}
	writeSweepText(cmd.OutOrStdout(), report)
	return nil
}

// sweepReport runs (or, for a dry run, only reports) the retry pass.
func sweepReport(cmd *cobra.Command, router *meeting.Router, meetingsDir string, opts meetingSweepOptions) (meetingSweepReport, error) {
	report := meetingSweepReport{DryRun: opts.DryRun, Results: []meetingSweepResult{}}
	// Read the index first: it fails loudly on a meetings dir that exists but
	// cannot be read, which SweepPendingRoutes deliberately treats as "nothing
	// pending". A sweep that could not even look is not a clean sweep.
	entries, _, err := meeting.Index(cmd.Context(), meetingsDir, meeting.IndexOptions{Limit: meeting.MaxIndexLimit})
	if err != nil {
		return meetingSweepReport{}, err
	}
	if opts.DryRun {
		for _, entry := range pendingOnly(entries) {
			report.Results = append(report.Results, meetingSweepResult{
				Bundle: entry.Bundle, ID: entry.ID, Outcome: outcomePending,
				DestinationID: entry.Route.DestinationID,
			})
		}
		report.Attempted = len(report.Results)
		return report, nil
	}
	for _, result := range meeting.SweepPendingRoutes(cmd.Context(), router, meetingsDir) {
		row := meetingSweepResult{
			Bundle:        result.Bundle,
			ID:            filepath.Base(result.Bundle),
			DestinationID: result.DestID,
			Outcome:       result.Receipt.Outcome,
			Detail:        result.Receipt.Detail,
		}
		if result.Err != nil {
			row.Error = result.Err.Error()
			if row.Outcome == "" {
				row.Outcome = meeting.OutcomeFailed
			}
		}
		if row.Outcome == meeting.OutcomeRouted {
			report.Routed++
		} else {
			report.Failed++
		}
		report.Results = append(report.Results, row)
	}
	report.Attempted = len(report.Results)
	return report, nil
}

func writeSweepText(w io.Writer, report meetingSweepReport) {
	for _, result := range report.Results {
		line := fmt.Sprintf("%s  → %s (%s)", result.ID, result.DestinationID, result.Outcome)
		if result.Detail != "" {
			line += ": " + result.Detail
		}
		if result.Error != "" {
			line += ": " + result.Error
		}
		fmt.Fprintln(w, line)
	}
	if report.DryRun {
		fmt.Fprintf(w, "%d meeting(s) would be retried\n", report.Attempted)
		return
	}
	fmt.Fprintf(w, "%d attempted, %d routed, %d failed\n", report.Attempted, report.Routed, report.Failed)
}
