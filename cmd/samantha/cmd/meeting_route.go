package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/meeting"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

// cliAutoRouteDest returns the destination to deliver automatically at
// capture end: --route wins, else mode=auto's configured default. Empty means
// routing is interactive (ask), disabled, or suppressed by --no-route.
func cliAutoRouteDest(routeCfg meeting.Config, opts meetingOptions) string {
	if opts.NoRoute {
		return ""
	}
	if opts.RouteTo != "" {
		return opts.RouteTo
	}
	if routeCfg.Mode == meeting.ModeAuto && routeCfg.Default != "" {
		return routeCfg.Default
	}
	return ""
}

// routeAfterRecordTo resolves destinations and delivers to destID — the
// auto-delivery path that runs at capture end, before review (WI-162bbb R2).
func routeAfterRecordTo(cmd *cobra.Command, cfg *config.Config, summary meetinglog.Summary, destID string, jsonOut bool) error {
	routeCfg := meeting.FromConfig(cfg)
	router := meeting.NewDefaultRouter(routeCfg)
	ctx, cancel := context.WithTimeout(context.Background(), meeting.DiscoverTimeout)
	defer cancel()
	expanded, _, _ := router.ExpandForRouting(ctx)
	router.Cfg = expanded
	return routeAndPrint(cmd, router, summary, expanded.Body, destID, jsonOut)
}

// maybeRouteAfterRecord applies post-meeting routing for the CLI record path.
// Auto-delivery (--route / mode=auto) is handled earlier by routeAfterRecordTo;
// this covers the interactive ask flow and mode=off.
//
// Human status lines never go to stdout when opts.JSON is set — machine-readable
// mode must keep stdout as pure JSON (summary / utterance stream only).
func maybeRouteAfterRecord(cmd *cobra.Command, cfg *config.Config, summary meetinglog.Summary, opts meetingOptions) error {
	if opts.NoRoute {
		return nil
	}
	routeCfg := meeting.FromConfig(cfg)
	router := meeting.NewDefaultRouter(routeCfg)
	ctx, cancel := context.WithTimeout(context.Background(), meeting.DiscoverTimeout)
	defer cancel()
	expanded, dests, discoverErr := router.ExpandForRouting(ctx)
	router.Cfg = expanded

	if opts.RouteTo != "" {
		return routeAndPrint(cmd, router, summary, expanded.Body, opts.RouteTo, opts.JSON)
	}

	switch routeCfg.Mode {
	case meeting.ModeOff:
		return nil
	case meeting.ModeAuto:
		if routeCfg.Default == "" {
			fmt.Fprintln(cmd.ErrOrStderr(), "meeting route: mode=auto but no default destination configured")
			return nil
		}
		return routeAndPrint(cmd, router, summary, expanded.Body, routeCfg.Default, opts.JSON)
	default: // ask
		if opts.JSON || opts.NoTUI || !isatty.IsTerminal(os.Stdout.Fd()) || !isatty.IsTerminal(os.Stdin.Fd()) {
			// Non-interactive: skip silently (use --route or meeting route later).
			return nil
		}
		if len(dests) == 0 {
			msg := "No routing destinations available (install camp or edit meeting.route.destinations). Notes kept local."
			if discoverErr != nil {
				msg = fmt.Sprintf("No routing destinations available (camp list: %v). Notes kept local.", discoverErr)
			}
			fmt.Fprintln(cmd.OutOrStdout(), msg)
			return nil
		}
		id, skipped, err := promptRouteDestination(cmd, dests, routeCfg.Default)
		if err != nil {
			return err
		}
		if skipped {
			fmt.Fprintln(cmd.OutOrStdout(), meeting.BannerLine(meeting.Receipt{Outcome: meeting.OutcomeSkipped}))
			return nil
		}
		return routeAndPrint(cmd, router, summary, expanded.Body, id, false)
	}
}

// routeAndPrint renders and routes a meeting, then prints a human status line.
// When jsonOut is true the banner goes to stderr so stdout stays machine-readable.
// router.Cfg should already include discovered destinations (see ExpandForRouting).
//
// The receipt is dropped here on purpose: `meeting record --json` owns stdout
// for its utterance stream and final summary, and a routing object in that
// stream would break it. `meeting route --json` calls routeAndReport instead.
func routeAndPrint(cmd *cobra.Command, router *meeting.Router, summary meetinglog.Summary, body, destID string, jsonOut bool) error {
	_, err := routeAndReport(cmd, router, summary, body, destID, jsonOut)
	return err
}

// routeResult is one delivery attempt: the receipt, plus the failure that is
// reported rather than raised.
type routeResult struct {
	Receipt meeting.Receipt
	Err     error
}

// routeAndReport is routeAndPrint plus the receipt, for callers that publish a
// structured result. The returned error is fatal (nothing could be rendered);
// a route that was attempted and failed comes back in routeResult.Err, because
// the bundle is still on disk and its durable route_failed event keeps it
// retryable — losing the notes over a failed export would be the real bug.
func routeAndReport(cmd *cobra.Command, router *meeting.Router, summary meetinglog.Summary, body, destID string, jsonOut bool) (routeResult, error) {
	note, err := meeting.Render(summary, body)
	if err != nil {
		return routeResult{}, fmt.Errorf("render meeting note: %w", err)
	}
	// If the id is still unknown, try a late expand (covers direct callers).
	if _, ok := router.Cfg.DestinationByID(destID); !ok {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		expanded, _, _ := router.ExpandForRouting(ctx)
		cancel()
		router.Cfg = expanded
	}
	receipt, routeErr := router.RouteByID(context.Background(), note, destID)
	status := meeting.BannerLine(receipt)
	if jsonOut {
		fmt.Fprintln(cmd.ErrOrStderr(), status)
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), status)
	}
	if routeErr != nil {
		// Lossless: original files remain; surface the error but don't fail the record command hard.
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v\n", routeErr)
	}
	return routeResult{Receipt: receipt, Err: routeErr}, nil
}

func promptRouteDestination(cmd *cobra.Command, dests []meeting.Destination, defaultID string) (id string, skipped bool, err error) {
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "Route meeting notes?")
	// Preselect default if present.
	defaultIdx := -1
	for i, d := range dests {
		mark := ""
		if d.ID == defaultID {
			mark = " (default)"
			defaultIdx = i
		}
		label := meeting.DestinationLabel(d)
		fmt.Fprintf(out, "  %d) %s%s\n", i+1, label, mark)
	}
	fmt.Fprintf(out, "  0) keep local only\n")
	fmt.Fprint(out, "Choice: ")

	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return "", false, err
		}
		return "", true, nil
	}
	line := strings.TrimSpace(sc.Text())
	if line == "" && defaultIdx >= 0 {
		return dests[defaultIdx].ID, false, nil
	}
	if line == "" || line == "0" {
		return "", true, nil
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 0 || n > len(dests) {
		return "", true, nil
	}
	if n == 0 {
		return "", true, nil
	}
	return dests[n-1].ID, false, nil
}

// meetingRouteReceipt is the `meeting route --json` contract (G36): where the
// notes went, in what shape, and — when the export failed — why. It is the
// Mac app's only path to file and apple-notes destinations, because the wire's
// route files into campaigns only.
type meetingRouteReceipt struct {
	Bundle        string    `json:"bundle"`
	ID            string    `json:"id"`
	DestinationID string    `json:"destination_id"`
	Type          string    `json:"type"`
	Outcome       string    `json:"outcome"`
	Detail        string    `json:"detail"`
	At            time.Time `json:"at"`
	Body          string    `json:"body"`
	Error         string    `json:"error"`
}

// writeRouteReceipt publishes the receipt on stdout. The human banner already
// went to stderr, so stdout is exactly one JSON object.
func writeRouteReceipt(w io.Writer, bundle, body string, result routeResult) error {
	receipt := meetingRouteReceipt{
		Bundle:        bundle,
		ID:            filepath.Base(bundle),
		DestinationID: result.Receipt.DestinationID,
		Type:          result.Receipt.Type,
		Outcome:       result.Receipt.Outcome,
		Detail:        result.Receipt.Detail,
		At:            result.Receipt.At,
		Body:          body,
	}
	if result.Err != nil {
		receipt.Error = result.Err.Error()
		if receipt.Outcome == "" {
			receipt.Outcome = meeting.OutcomeFailed
		}
	}
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(receipt); err != nil {
		return fmt.Errorf("meeting route: write json: %w", err)
	}
	return nil
}

// meetingRouteFlags carries one `meeting route` invocation.
type meetingRouteFlags struct {
	To    string
	Body  string
	NoTUI bool
	JSON  bool
}

func runMeetingRoute(cmd *cobra.Command, fileArg string, flags meetingRouteFlags) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	routeCfg := meeting.FromConfig(cfg)
	if flags.Body != "" {
		routeCfg.Body = flags.Body
	}
	jsonl, err := meeting.ResolveMeetingFile(config.MeetingsDirFrom(cfg), fileArg)
	if err != nil {
		return err
	}
	summary, err := meeting.LoadSummaryFromJSONL(jsonl)
	if err != nil {
		return err
	}
	router := meeting.NewDefaultRouter(routeCfg)
	ctx, cancel := context.WithTimeout(context.Background(), meeting.DiscoverTimeout)
	expanded, dests, discoverErr := router.ExpandForRouting(ctx)
	cancel()
	router.Cfg = expanded

	destID, skipped, err := resolveRouteDestination(cmd, routeCfg, dests, discoverErr, flags)
	if err != nil || skipped {
		return err
	}
	result, err := routeAndReport(cmd, router, summary, expanded.Body, destID, flags.JSON)
	if err != nil || !flags.JSON {
		return err
	}
	return writeRouteReceipt(cmd.OutOrStdout(), summary.Bundle, expanded.Body, result)
}

// resolveRouteDestination picks where the notes go: --to wins, then the
// configured default in any non-interactive mode, then a prompt. skipped is
// the user choosing to keep the meeting local, which is a clean exit.
func resolveRouteDestination(cmd *cobra.Command, routeCfg meeting.Config, dests []meeting.Destination,
	discoverErr error, flags meetingRouteFlags) (destID string, skipped bool, err error) {
	if destID = strings.TrimSpace(flags.To); destID != "" {
		return destID, false, nil
	}
	if flags.NoTUI || flags.JSON || !isatty.IsTerminal(os.Stdout.Fd()) {
		if routeCfg.Default == "" {
			return "", false, fmt.Errorf("meeting route: pass --to <destination-id> (or set meeting.route.default)")
		}
		return routeCfg.Default, false, nil
	}
	if len(dests) == 0 {
		if discoverErr != nil {
			return "", false, fmt.Errorf("meeting route: no destinations available (camp list: %w)", discoverErr)
		}
		return "", false, fmt.Errorf("meeting route: no destinations configured")
	}
	destID, skipped, err = promptRouteDestination(cmd, dests, routeCfg.Default)
	if err != nil || !skipped {
		return destID, skipped, err
	}
	fmt.Fprintln(cmd.OutOrStdout(), meeting.BannerLine(meeting.Receipt{Outcome: meeting.OutcomeSkipped}))
	return "", true, nil
}

func newMeetingRouteCmd() *cobra.Command {
	var (
		to      string
		body    string
		noTUI   bool
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "route [file]",
		Short: "Route an existing meeting's notes to a destination",
		Long: `Render a finished .meeting bundle and send it to a configured
destination. With no file argument, uses the most recent meeting under the
meetings directory.

Campaign destinations are discovered via camp list --json when camp is on PATH,
in addition to meeting.route.destinations in config.

--json is non-interactive (it requires --to or meeting.route.default) and
writes one receipt object to stdout:

  {"bundle":"…","id":"x.meeting","destination_id":"camp:blockhead",
   "type":"campaign","outcome":"routed","detail":"notes/meetings/x.md",
   "at":"…","body":"full","error":""}

The human status line stays on stderr so stdout is only the receipt.

Exit codes: 0 when the route was attempted — including a failed export, whose
reason is in the receipt's "error" and whose bundle stays on disk and
retryable by samantha meeting sweep. Non-zero only when nothing could be
attempted: no such meeting, no destination given, or notes that could not be
rendered.

Examples:
  samantha meeting route
  samantha meeting route --to docs
  samantha meeting route ~/path/to/standup-20260720.meeting --to mytools
  samantha meeting route --to docs --body full
  samantha meeting route --to notes-folder --json`,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			fileArg := ""
			if len(args) == 1 {
				fileArg = args[0]
			}
			return runMeetingRoute(cmd, fileArg, meetingRouteFlags{
				To: to, Body: body, NoTUI: noTUI, JSON: jsonOut,
			})
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "Destination id from meeting.route.destinations or camp:<name>")
	cmd.Flags().StringVar(&body, "body", "", "Override body scope: notes | full")
	cmd.Flags().BoolVar(&noTUI, "no-tui", false, "Non-interactive (requires --to or meeting.route.default)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Non-interactive; keep human status on stderr (requires --to or meeting.route.default)")
	return cmd
}
