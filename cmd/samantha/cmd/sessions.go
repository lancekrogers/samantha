package cmd

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/netapi"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	"github.com/lancekrogers/samantha/pkg/voiceagent/session"
)

// sessionSchemaV1 identifies the `sessions show --json` transcript schema.
const sessionSchemaV1 = "samantha.session.v1"

func newSessionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "Browse, inspect, and delete saved conversations",
		Long: `Browse, inspect, and delete saved conversations.

The top-level "resume"/"continue" commands are unchanged; these verbs are
read/delete only — they never load a session into a running agent.`,
	}
	cmd.AddCommand(newSessionsListCmd())
	cmd.AddCommand(newSessionsShowCmd())
	cmd.AddCommand(newSessionsRmCmd())
	return cmd
}

// --- list ---

func newSessionsListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List saved conversations, most recently updated first",
		Args:  cobra.NoArgs,
		RunE:  runSessionsList,
	}
	cmd.Flags().Bool("json", false, "Output machine-readable JSON")
	return cmd
}

// sessionSummaries returns the same rows GET /v1/sessions returns
// (netapi.SessionSummary), so the CLI and the wire route can be diffed:
// session.List() is the one source both listSessionSummaries (serve.go) and
// this command read from.
func sessionSummaries() []netapi.SessionSummary {
	sessions := session.List()
	out := make([]netapi.SessionSummary, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, netapi.SessionSummary{
			ID: s.ID, Summary: s.Summary, Turns: len(s.Turns), UpdatedAt: s.UpdatedAt,
		})
	}
	return out
}

func runSessionsList(cmd *cobra.Command, _ []string) error {
	cmd.SilenceUsage = true
	asJSON, _ := cmd.Flags().GetBool("json")
	summaries := sessionSummaries()

	if asJSON {
		return encodeJSON(cmd, summaries)
	}

	out := cmd.OutOrStdout()
	if len(summaries) == 0 {
		fmt.Fprintln(out, dimStyle.Render("  No saved sessions."))
		return nil
	}
	fmt.Fprintf(out, "\n  %s\n\n", titleStyle.Render("Saved sessions"))
	for _, s := range summaries {
		fmt.Fprintf(out, "  %s  %s\n", keyStyle.Render(s.ID), dimStyle.Render(fmt.Sprintf("%d turn(s) · updated %s", s.Turns, s.UpdatedAt.Format(time.RFC3339))))
		if s.Summary != "" {
			fmt.Fprintf(out, "    %s\n", s.Summary)
		}
	}
	fmt.Fprintf(out, "\n  %d session(s).\n\n", len(summaries))
	return nil
}

// --- show ---

func newSessionsShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show a saved conversation's transcript",
		Args:  cobra.ExactArgs(1),
		RunE:  runSessionsShow,
	}
	cmd.Flags().Bool("json", false, "Output machine-readable JSON")
	return cmd
}

// sessionTurnJSON is one turn of `sessions show --json`. At is always present
// as a key (string|null) so a client can render a stable column: on-disk
// turns carry no timestamp today, so it is null for every existing session
// until per-turn stamping lands.
type sessionTurnJSON struct {
	Role    string  `json:"role"`
	Text    string  `json:"text"`
	At      *string `json:"at"`
	Speaker string  `json:"speaker,omitempty"`
}

// sessionTranscriptJSON is the body of `sessions show --json` (schema
// samantha.session.v1).
type sessionTranscriptJSON struct {
	Schema    string            `json:"schema"`
	ID        string            `json:"id"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	Provider  string            `json:"provider"`
	Model     string            `json:"model"`
	Summary   string            `json:"summary"`
	Turns     []sessionTurnJSON `json:"turns"`
}

func sessionTranscriptFrom(sess *session.Session) sessionTranscriptJSON {
	turns := make([]sessionTurnJSON, 0, len(sess.Turns))
	for _, t := range sess.Turns {
		turns = append(turns, sessionTurnJSON{Role: t.Role, Text: t.Content, At: turnAtJSON(t.At), Speaker: t.Speaker})
	}
	return sessionTranscriptJSON{
		Schema: sessionSchemaV1, ID: sess.ID, CreatedAt: sess.CreatedAt, UpdatedAt: sess.UpdatedAt,
		Provider: sess.Provider, Model: sess.Model, Summary: sess.Summary, Turns: turns,
	}
}

// turnAtJSON converts a brain.Turn's At into the show --json "at" value: nil
// (JSON null) for the zero time — every turn that predates SES-A5's per-turn
// stamping, or came from a provider that does not stamp it yet — otherwise
// its RFC3339 UTC string. Explicit rather than relying on At's own
// `omitempty` tag, which encoding/json never honours for a zero struct.
func turnAtJSON(at time.Time) *string {
	if at.IsZero() {
		return nil
	}
	s := at.UTC().Format(time.RFC3339)
	return &s
}

func runSessionsShow(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	asJSON, _ := cmd.Flags().GetBool("json")
	id := args[0]

	sess, loadErr := session.Load(id)
	if loadErr != nil {
		msg := fmt.Sprintf("no session %q (looked in %s)", id, config.SessionsDir())
		if asJSON {
			return emitJSONError(cmd, codedError(codeNotFound, "%s", msg))
		}
		return errors.New(msg)
	}

	if asJSON {
		return encodeJSON(cmd, sessionTranscriptFrom(sess))
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "\n  %s %s\n", titleStyle.Render("Session"), keyStyle.Render(sess.ID))
	fmt.Fprintf(out, "  %s\n\n", dimStyle.Render(fmt.Sprintf("%s/%s · created %s · updated %s", sess.Provider, sess.Model, sess.CreatedAt.Format(time.RFC3339), sess.UpdatedAt.Format(time.RFC3339))))
	for _, t := range sess.Turns {
		label := t.Role
		if t.Speaker != "" {
			label = fmt.Sprintf("%s [%s]", t.Role, t.Speaker)
		}
		fmt.Fprintf(out, "  %s: %s\n", keyStyle.Render(label), t.Content)
	}
	fmt.Fprintln(out)
	return nil
}

// --- rm ---

func newSessionsRmCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rm [id]",
		Short: "Delete one saved conversation, or every one older than --older-than",
		Long: `Delete one saved conversation, or every one older than --older-than.

Deleting the session the agent is currently using just makes it reappear on
the next turn — the CLI cannot see another process's live session id, so it
does not try to guess. The DELETE /v1/sessions/{id} route is the path that
can and does refuse.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runSessionsRm,
	}
	cmd.Flags().String("older-than", "", "Delete every session whose updated_at is older than this (e.g. 30d, 12h); requires --yes")
	cmd.Flags().Bool("yes", false, "Confirm the --older-than batch delete")
	cmd.Flags().Bool("json", false, "Output machine-readable JSON")
	return cmd
}

// sessionsRmResultJSON is the body of `sessions rm --json`: Deleted on
// success, WouldDelete for an --older-than dry preview (no --yes) — never
// both on the same response.
type sessionsRmResultJSON struct {
	Deleted     []string `json:"deleted,omitempty"`
	WouldDelete []string `json:"would_delete,omitempty"`
	Count       int      `json:"count"`
}

// parseOlderThan parses a duration like "30d" or "12h" for --older-than.
// time.ParseDuration has no day unit, so "d" is handled here (as an exact
// 24h multiple) before falling through to it for h/m/s and the rest.
func parseOlderThan(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("--older-than: empty duration")
	}
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.ParseFloat(days, 64)
		if err != nil {
			return 0, fmt.Errorf("--older-than %q: %w", s, err)
		}
		return time.Duration(n * float64(24*time.Hour)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("--older-than %q: %w", s, err)
	}
	return d, nil
}

func runSessionsRm(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	asJSON, _ := cmd.Flags().GetBool("json")
	yes, _ := cmd.Flags().GetBool("yes")
	olderThan, _ := cmd.Flags().GetString("older-than")
	var id string
	if len(args) == 1 {
		id = args[0]
	}

	switch {
	case id == "" && olderThan == "":
		return sessionsRmFail(cmd, asJSON, codeInvalidID, "sessions rm: give a session id or --older-than")
	case id != "" && olderThan != "":
		return sessionsRmFail(cmd, asJSON, codeInvalidID, "sessions rm: --older-than cannot be combined with a session id")
	}

	store := session.DefaultStore()

	if id != "" {
		if err := store.Delete(id); err != nil {
			code := codeInvalidID
			if errors.Is(err, session.ErrSessionNotFound) {
				code = codeNotFound
			}
			return sessionsRmFail(cmd, asJSON, code, "%s", err.Error())
		}
		return sessionsRmDone(cmd, asJSON, []string{id})
	}

	dur, err := parseOlderThan(olderThan)
	if err != nil {
		return sessionsRmFail(cmd, asJSON, codeInvalidID, "%s", err.Error())
	}
	cutoff := time.Now().Add(-dur)
	var candidates []string
	for _, s := range session.List() {
		if s.UpdatedAt.Before(cutoff) {
			candidates = append(candidates, s.ID)
		}
	}

	if !yes {
		if asJSON {
			return encodeJSON(cmd, sessionsRmResultJSON{WouldDelete: nonNilStrings(candidates), Count: len(candidates)})
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  Would delete %d session(s) older than %s. Re-run with --yes to delete.\n", len(candidates), olderThan)
		return nil
	}

	var deleted []string
	for _, cid := range candidates {
		err := store.Delete(cid)
		switch {
		case err == nil:
			deleted = append(deleted, cid)
		case errors.Is(err, session.ErrSessionNotFound):
			// Deleted concurrently (another client, or the agent rewriting
			// its live session mid-batch): not this run's doing, so it does
			// not count, but it is not a batch failure either.
		default:
			// An unexpected per-file error (permissions, etc.) must not
			// abort the batch — report what this run actually removed.
		}
	}
	return sessionsRmDone(cmd, asJSON, deleted)
}

func sessionsRmDone(cmd *cobra.Command, asJSON bool, deleted []string) error {
	deleted = nonNilStrings(deleted)
	if asJSON {
		return encodeJSON(cmd, sessionsRmResultJSON{Deleted: deleted, Count: len(deleted)})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  Deleted %d session(s). If the agent is currently using one of them it will rewrite it on the next turn.\n", len(deleted))
	return nil
}

func sessionsRmFail(cmd *cobra.Command, asJSON bool, code, format string, args ...any) error {
	if asJSON {
		return emitJSONError(cmd, codedError(code, format, args...))
	}
	return fmt.Errorf(format, args...)
}

// nonNilStrings normalises nil to an empty (non-nil) slice so --json always
// encodes [] rather than null for an empty result.
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func init() {
	rootCmd.AddCommand(newSessionsCmd())
}
