package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/meeting"
	"github.com/lancekrogers/samantha/internal/meetinglog"
)

// maybeRouteAfterRecord applies post-meeting routing for the CLI record path.
// --no-route wins; --route <id> forces a destination; otherwise mode from config.
func maybeRouteAfterRecord(cmd *cobra.Command, cfg *config.Config, summary meetinglog.Summary, opts meetingOptions) error {
	if opts.NoRoute {
		return nil
	}
	routeCfg := meeting.FromConfig(cfg)
	router := meeting.NewDefaultRouter(routeCfg)

	if opts.RouteTo != "" {
		return routeAndPrint(cmd, router, summary, routeCfg.Body, opts.RouteTo)
	}

	switch routeCfg.Mode {
	case meeting.ModeOff:
		return nil
	case meeting.ModeAuto:
		if routeCfg.Default == "" {
			fmt.Fprintln(cmd.ErrOrStderr(), "meeting route: mode=auto but no default destination configured")
			return nil
		}
		return routeAndPrint(cmd, router, summary, routeCfg.Body, routeCfg.Default)
	default: // ask
		if opts.JSON || opts.NoTUI || !isatty.IsTerminal(os.Stdout.Fd()) || !isatty.IsTerminal(os.Stdin.Fd()) {
			// Non-interactive: skip silently (use --route or meeting route later).
			return nil
		}
		dests := router.AvailableDestinations()
		if len(dests) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No routing destinations configured (edit meeting.route.destinations in config). Notes kept local.")
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
		return routeAndPrint(cmd, router, summary, routeCfg.Body, id)
	}
}

func routeAndPrint(cmd *cobra.Command, router *meeting.Router, summary meetinglog.Summary, body, destID string) error {
	note, err := meeting.Render(summary, body)
	if err != nil {
		return fmt.Errorf("render meeting note: %w", err)
	}
	receipt, err := router.RouteByID(context.Background(), note, destID)
	fmt.Fprintln(cmd.OutOrStdout(), meeting.BannerLine(receipt))
	if err != nil {
		// Lossless: original files remain; surface the error but don't fail the record command hard.
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v\n", err)
		return nil
	}
	return nil
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
		fmt.Fprintf(out, "  %d) %s [%s]%s\n", i+1, d.ID, d.Type, mark)
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
		Long: `Render a finished meeting (.log/.jsonl) and send it to a configured
destination. With no file argument, uses the most recent meeting under the
meetings directory.

Examples:
  samantha meeting route
  samantha meeting route --to docs
  samantha meeting route ~/path/to/standup-20260720.jsonl --to mytools
  samantha meeting route --to docs --body full`,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			routeCfg := meeting.FromConfig(cfg)
			if body != "" {
				routeCfg.Body = body
			}
			meetingsDir := config.MeetingsDirFrom(cfg)
			fileArg := ""
			if len(args) == 1 {
				fileArg = args[0]
			}
			jsonl, err := meeting.ResolveMeetingFile(meetingsDir, fileArg)
			if err != nil {
				return err
			}
			summary, err := meeting.LoadSummaryFromJSONL(jsonl)
			if err != nil {
				return err
			}
			router := meeting.NewDefaultRouter(routeCfg)

			destID := strings.TrimSpace(to)
			if destID == "" {
				if noTUI || jsonOut || !isatty.IsTerminal(os.Stdout.Fd()) {
					if routeCfg.Default == "" {
						return fmt.Errorf("meeting route: pass --to <destination-id> (or set meeting.route.default)")
					}
					destID = routeCfg.Default
				} else {
					dests := router.AvailableDestinations()
					if len(dests) == 0 {
						return fmt.Errorf("meeting route: no destinations configured")
					}
					var skipped bool
					destID, skipped, err = promptRouteDestination(cmd, dests, routeCfg.Default)
					if err != nil {
						return err
					}
					if skipped {
						fmt.Fprintln(cmd.OutOrStdout(), meeting.BannerLine(meeting.Receipt{Outcome: meeting.OutcomeSkipped}))
						return nil
					}
				}
			}
			return routeAndPrint(cmd, router, summary, routeCfg.Body, destID)
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "Destination id from meeting.route.destinations")
	cmd.Flags().StringVar(&body, "body", "", "Override body scope: notes | full")
	cmd.Flags().BoolVar(&noTUI, "no-tui", false, "Non-interactive (requires --to or meeting.route.default)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Non-interactive (requires --to or meeting.route.default)")
	return cmd
}
