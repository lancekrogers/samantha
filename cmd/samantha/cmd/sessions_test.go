package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/netapi/wire"
	"github.com/lancekrogers/samantha/pkg/voiceagent/brain"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	"github.com/lancekrogers/samantha/pkg/voiceagent/session"
)

func runSessionsCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newSessionsCmd()
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(args)
	cmd.SetContext(context.Background())
	for _, sub := range cmd.Commands() {
		sub.SetOut(&out)
		sub.SetErr(&errBuf)
	}
	err := cmd.Execute()
	return out.String(), err
}

// writeSessionFile writes s directly to dir, bypassing Store.Save (which
// always stamps UpdatedAt = now) so tests can control it, e.g. for
// --older-than fixtures.
func writeSessionFile(t *testing.T, dir string, s session.Session) {
	t.Helper()
	data, err := json.MarshalIndent(&s, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, s.ID+".json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func sessionsEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	config.SetConfigDirForTest(t, dir)
	sessionsDir := config.SessionsDir()
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return sessionsDir
}

// --- list ---

func TestSessionsListJSONOrderedMostRecentFirst(t *testing.T) {
	dir := sessionsEnv(t)
	now := time.Now().UTC().Truncate(time.Second)
	writeSessionFile(t, dir, session.Session{
		ID: "20260101-000000-aaaa", Provider: "ollama", Model: "qwen3:8b",
		Summary: "older", UpdatedAt: now.Add(-time.Hour),
		Turns: []brain.Turn{{Role: "user", Content: "hi"}},
	})
	writeSessionFile(t, dir, session.Session{
		ID: "20260101-010000-bbbb", Provider: "claude", Model: "sonnet",
		Summary: "newer", UpdatedAt: now,
		Turns: []brain.Turn{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hey"}},
	})

	out, err := runSessionsCmd(t, "list", "--json")
	if err != nil {
		t.Fatalf("sessions list --json error = %v (out %s)", err, out)
	}
	var got []wire.SessionSummary
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if len(got) != 2 {
		t.Fatalf("summaries = %+v, want 2", got)
	}
	if got[0].ID != "20260101-010000-bbbb" || got[1].ID != "20260101-000000-aaaa" {
		t.Fatalf("order = [%s, %s], want newest first", got[0].ID, got[1].ID)
	}
	if got[0].Turns != 2 || got[0].Summary != "newer" {
		t.Fatalf("summary[0] = %+v, want turns=2 summary=newer", got[0])
	}
}

func TestSessionsListJSONEmptyIsEmptyArray(t *testing.T) {
	sessionsEnv(t)

	out, err := runSessionsCmd(t, "list", "--json")
	if err != nil {
		t.Fatalf("sessions list --json error = %v (out %s)", err, out)
	}
	var got []wire.SessionSummary
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if len(got) != 0 {
		t.Fatalf("summaries = %+v, want none", got)
	}
	if bytes.TrimSpace([]byte(out))[0] != '[' {
		t.Fatalf("output = %q, want a JSON array even when empty", out)
	}
}

func TestSessionsListHumanShowsSummaryAndCount(t *testing.T) {
	dir := sessionsEnv(t)
	writeSessionFile(t, dir, session.Session{
		ID: "20260101-000000-aaaa", Provider: "ollama", Model: "qwen3:8b",
		Summary: "what's on my calendar", UpdatedAt: time.Now(),
		Turns: []brain.Turn{{Role: "user", Content: "hi"}},
	})

	out, err := runSessionsCmd(t, "list")
	if err != nil {
		t.Fatalf("sessions list error = %v (out %s)", err, out)
	}
	for _, want := range []string{"20260101-000000-aaaa", "what's on my calendar", "1 session(s)"} {
		if !contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// --- show ---

func TestSessionsShowJSONSchema(t *testing.T) {
	dir := sessionsEnv(t)
	created := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	updated := time.Now().UTC().Truncate(time.Second)
	writeSessionFile(t, dir, session.Session{
		ID: "20260816-231455-a3f9", Provider: "ollama", Model: "qwen3:8b",
		CreatedAt: created, UpdatedAt: updated, Summary: "what's on my calendar",
		Turns: []brain.Turn{
			{Role: "user", Content: "what's on my calendar", Speaker: "Lance"},
			{Role: "assistant", Content: "You have two things…"},
		},
	})

	out, err := runSessionsCmd(t, "show", "20260816-231455-a3f9", "--json")
	if err != nil {
		t.Fatalf("sessions show --json error = %v (out %s)", err, out)
	}
	var got sessionTranscriptJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if got.Schema != "samantha.session.v1" {
		t.Errorf("schema = %q, want samantha.session.v1", got.Schema)
	}
	if got.ID != "20260816-231455-a3f9" || got.Provider != "ollama" || got.Model != "qwen3:8b" {
		t.Errorf("transcript = %+v, want it to match the stored session", got)
	}
	if !got.CreatedAt.Equal(created) || !got.UpdatedAt.Equal(updated) {
		t.Errorf("timestamps = %v/%v, want %v/%v", got.CreatedAt, got.UpdatedAt, created, updated)
	}
	if len(got.Turns) != 2 {
		t.Fatalf("turns = %+v, want 2", got.Turns)
	}
	if got.Turns[0].Role != "user" || got.Turns[0].Text != "what's on my calendar" || got.Turns[0].Speaker != "Lance" {
		t.Errorf("turn[0] = %+v, want the labeled user turn", got.Turns[0])
	}
	if got.Turns[0].At != nil {
		t.Errorf("turn[0].at = %v, want null (no per-turn timestamp yet)", got.Turns[0].At)
	}
	if got.Turns[1].Role != "assistant" || got.Turns[1].Speaker != "" {
		t.Errorf("turn[1] = %+v, want assistant with no speaker", got.Turns[1])
	}
}

func TestSessionsShowJSONAtKeyAlwaysPresent(t *testing.T) {
	dir := sessionsEnv(t)
	writeSessionFile(t, dir, session.Session{
		ID: "20260101-000000-aaaa", UpdatedAt: time.Now(),
		Turns: []brain.Turn{{Role: "user", Content: "hi"}},
	})

	out, err := runSessionsCmd(t, "show", "20260101-000000-aaaa", "--json")
	if err != nil {
		t.Fatalf("sessions show --json error = %v (out %s)", err, out)
	}
	var raw struct {
		Turns []map[string]any `json:"turns"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if len(raw.Turns) != 1 {
		t.Fatalf("turns = %+v, want 1", raw.Turns)
	}
	at, present := raw.Turns[0]["at"]
	if !present {
		t.Fatal("turn is missing the \"at\" key entirely, want it present as null")
	}
	if at != nil {
		t.Errorf("at = %v, want null", at)
	}
	if _, present := raw.Turns[0]["speaker"]; present {
		t.Error("unlabeled turn has a speaker key, want it omitted")
	}
}

// A turn stamped by SES-A5 (brain.Turn.At) must show up as a real RFC3339
// string, not null — null is only for turns that predate stamping.
func TestSessionsShowJSONEmitsRealTimestampForStampedTurns(t *testing.T) {
	dir := sessionsEnv(t)
	stamped := time.Date(2026, 8, 16, 23, 14, 55, 0, time.UTC)
	writeSessionFile(t, dir, session.Session{
		ID: "20260101-000000-aaaa", UpdatedAt: time.Now(),
		Turns: []brain.Turn{
			{Role: "user", Content: "hi", At: stamped},
			{Role: "assistant", Content: "hello"}, // predates stamping: At is zero
		},
	})

	out, err := runSessionsCmd(t, "show", "20260101-000000-aaaa", "--json")
	if err != nil {
		t.Fatalf("sessions show --json error = %v (out %s)", err, out)
	}
	var got sessionTranscriptJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if len(got.Turns) != 2 {
		t.Fatalf("turns = %+v, want 2", got.Turns)
	}
	if got.Turns[0].At == nil || *got.Turns[0].At != "2026-08-16T23:14:55Z" {
		t.Errorf("turn[0].at = %v, want the stamped RFC3339 string", got.Turns[0].At)
	}
	if got.Turns[1].At != nil {
		t.Errorf("turn[1].at = %v, want null (unstamped turn)", got.Turns[1].At)
	}
}

func TestSessionsShowUnknownIDJSON(t *testing.T) {
	sessionsEnv(t)

	out, err := runSessionsCmd(t, "show", "does-not-exist", "--json")
	if err == nil {
		t.Fatalf("sessions show --json error = nil, want not_found (out %s)", out)
	}
	code, msg, _ := decodeErrorJSON(t, out)
	if code != codeNotFound {
		t.Fatalf("code = %q, want %q", code, codeNotFound)
	}
	if !contains(msg, "does-not-exist") || !contains(msg, config.SessionsDir()) {
		t.Fatalf("error = %q, want it to name the id and the sessions dir", msg)
	}
}

func TestSessionsShowUnknownIDHuman(t *testing.T) {
	sessionsEnv(t)

	out, err := runSessionsCmd(t, "show", "does-not-exist")
	if err == nil {
		t.Fatal("sessions show error = nil, want a failure")
	}
	if !contains(err.Error(), "does-not-exist") {
		t.Errorf("error = %q, want it to name the id", err.Error())
	}
	if out != "" {
		t.Errorf("output = %q, want nothing printed on failure", out)
	}
}

func TestSessionsShowHumanRendersSpeakerAndRoles(t *testing.T) {
	dir := sessionsEnv(t)
	writeSessionFile(t, dir, session.Session{
		ID: "20260101-000000-aaaa", Provider: "ollama", Model: "qwen3:8b", UpdatedAt: time.Now(),
		Turns: []brain.Turn{
			{Role: "user", Content: "what's on my calendar", Speaker: "Lance"},
			{Role: "assistant", Content: "nothing today"},
		},
	})

	out, err := runSessionsCmd(t, "show", "20260101-000000-aaaa")
	if err != nil {
		t.Fatalf("sessions show error = %v (out %s)", err, out)
	}
	// "assistant" and the trailing ":" straddle a lipgloss style boundary
	// (an ANSI reset sits between them), so they are checked separately.
	for _, want := range []string{"user [Lance]", "what's on my calendar", "assistant", "nothing today"} {
		if !contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// --- rm ---

func TestSessionsRmSingleIDJSON(t *testing.T) {
	dir := sessionsEnv(t)
	writeSessionFile(t, dir, session.Session{ID: "20260101-000000-aaaa", UpdatedAt: time.Now()})

	out, err := runSessionsCmd(t, "rm", "20260101-000000-aaaa", "--json")
	if err != nil {
		t.Fatalf("sessions rm --json error = %v (out %s)", err, out)
	}
	var got sessionsRmResultJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if got.Count != 1 || len(got.Deleted) != 1 || got.Deleted[0] != "20260101-000000-aaaa" {
		t.Fatalf("result = %+v, want deleted=[the id] count=1", got)
	}
	if got.WouldDelete != nil {
		t.Errorf("would_delete = %v, want omitted on a real delete", got.WouldDelete)
	}
	if _, err := os.Stat(filepath.Join(dir, "20260101-000000-aaaa.json")); !os.IsNotExist(err) {
		t.Fatalf("session file still exists: err=%v", err)
	}
}

func TestSessionsRmSingleIDHuman(t *testing.T) {
	dir := sessionsEnv(t)
	writeSessionFile(t, dir, session.Session{ID: "20260101-000000-aaaa", UpdatedAt: time.Now()})

	out, err := runSessionsCmd(t, "rm", "20260101-000000-aaaa")
	if err != nil {
		t.Fatalf("sessions rm error = %v (out %s)", err, out)
	}
	for _, want := range []string{"Deleted 1 session(s)", "rewrite it on the next turn"} {
		if !contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestSessionsRmUnknownIDReportsSentinelMessage(t *testing.T) {
	sessionsEnv(t)

	out, err := runSessionsCmd(t, "rm", "20260101-000000-dead", "--json")
	if err == nil {
		t.Fatalf("sessions rm --json error = nil, want not_found (out %s)", out)
	}
	code, msg, _ := decodeErrorJSON(t, out)
	if code != codeNotFound {
		t.Fatalf("code = %q, want %q", code, codeNotFound)
	}
	if msg != session.ErrSessionNotFound.Error() {
		t.Fatalf("error = %q, want ErrSessionNotFound's exact message %q", msg, session.ErrSessionNotFound.Error())
	}
}

func TestSessionsRmNoArgsErrors(t *testing.T) {
	sessionsEnv(t)

	out, err := runSessionsCmd(t, "rm", "--json")
	if err == nil {
		t.Fatalf("sessions rm --json error = nil, want a usage error (out %s)", out)
	}
	if code, _, _ := decodeErrorJSON(t, out); code != codeInvalidID {
		t.Fatalf("code = %q, want %q", code, codeInvalidID)
	}
}

func TestSessionsRmIDAndOlderThanConflictErrors(t *testing.T) {
	sessionsEnv(t)

	out, err := runSessionsCmd(t, "rm", "20260101-000000-aaaa", "--older-than", "1d", "--json")
	if err == nil {
		t.Fatalf("sessions rm --json error = nil, want a conflict error (out %s)", out)
	}
	if code, _, _ := decodeErrorJSON(t, out); code != codeInvalidID {
		t.Fatalf("code = %q, want %q", code, codeInvalidID)
	}
}

// A negative --older-than must be rejected before it ever lists a
// candidate, let alone deletes one with --yes.
func TestSessionsRmOlderThanNegativeDurationRejected(t *testing.T) {
	dir := sessionsEnv(t)
	writeSessionFile(t, dir, session.Session{ID: "20260101-000000-aaaa", UpdatedAt: time.Now()})

	out, err := runSessionsCmd(t, "rm", "--older-than", "-30d", "--yes", "--json")
	if err == nil {
		t.Fatalf("sessions rm --older-than -30d --yes --json error = nil, want a rejection (out %s)", out)
	}
	if code, _, _ := decodeErrorJSON(t, out); code != codeInvalidID {
		t.Fatalf("code = %q, want %q", code, codeInvalidID)
	}
	if _, err := os.Stat(filepath.Join(dir, "20260101-000000-aaaa.json")); err != nil {
		t.Fatalf("session file was affected by a rejected --older-than: %v", err)
	}
}

func TestSessionsRmOlderThanDryRunDeletesNothing(t *testing.T) {
	dir := sessionsEnv(t)
	now := time.Now()
	writeSessionFile(t, dir, session.Session{ID: "20260101-000000-aaaa", UpdatedAt: now.Add(-40 * 24 * time.Hour)})
	writeSessionFile(t, dir, session.Session{ID: "20260201-000000-bbbb", UpdatedAt: now})

	out, err := runSessionsCmd(t, "rm", "--older-than", "30d", "--json")
	if err != nil {
		t.Fatalf("sessions rm --older-than --json error = %v (out %s)", err, out)
	}
	var got sessionsRmResultJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if got.Count != 1 || len(got.WouldDelete) != 1 || got.WouldDelete[0] != "20260101-000000-aaaa" {
		t.Fatalf("result = %+v, want would_delete=[the old one] count=1", got)
	}
	if got.Deleted != nil {
		t.Errorf("deleted = %v, want omitted on a dry run", got.Deleted)
	}
	for _, id := range []string{"20260101-000000-aaaa", "20260201-000000-bbbb"} {
		if _, err := os.Stat(filepath.Join(dir, id+".json")); err != nil {
			t.Fatalf("%s missing after a dry run: %v", id, err)
		}
	}
}

func TestSessionsRmOlderThanYesDeletesOnlyOldOnes(t *testing.T) {
	dir := sessionsEnv(t)
	now := time.Now()
	writeSessionFile(t, dir, session.Session{ID: "20260101-000000-aaaa", UpdatedAt: now.Add(-40 * 24 * time.Hour)})
	writeSessionFile(t, dir, session.Session{ID: "20260201-000000-bbbb", UpdatedAt: now})

	out, err := runSessionsCmd(t, "rm", "--older-than", "30d", "--yes", "--json")
	if err != nil {
		t.Fatalf("sessions rm --older-than --yes --json error = %v (out %s)", err, out)
	}
	var got sessionsRmResultJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if got.Count != 1 || len(got.Deleted) != 1 || got.Deleted[0] != "20260101-000000-aaaa" {
		t.Fatalf("result = %+v, want deleted=[the old one] count=1", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "20260101-000000-aaaa.json")); !os.IsNotExist(err) {
		t.Error("old session file still exists")
	}
	if _, err := os.Stat(filepath.Join(dir, "20260201-000000-bbbb.json")); err != nil {
		t.Errorf("new session file was removed: %v", err)
	}
}

func TestSessionsRmOlderThanNoMatchesIsEmptySuccess(t *testing.T) {
	dir := sessionsEnv(t)
	writeSessionFile(t, dir, session.Session{ID: "20260201-000000-bbbb", UpdatedAt: time.Now()})

	out, err := runSessionsCmd(t, "rm", "--older-than", "30d", "--yes", "--json")
	if err != nil {
		t.Fatalf("sessions rm --older-than --yes --json error = %v (out %s)", err, out)
	}
	var got sessionsRmResultJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if got.Count != 0 || len(got.Deleted) != 0 {
		t.Fatalf("result = %+v, want an empty (not nil-omitted) deleted list", got)
	}
}

// --- parseOlderThan ---

func TestParseOlderThan(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"30d", 30 * 24 * time.Hour, false},
		{"1d", 24 * time.Hour, false},
		{"0.5d", 12 * time.Hour, false},
		{"12h", 12 * time.Hour, false},
		{"45m", 45 * time.Minute, false},
		{"", 0, true},
		{"nope", 0, true},
		{"d", 0, true},
		// A non-positive duration must be rejected outright: it would put
		// the cutoff at or after now, matching almost every session on a
		// command that deletes with --yes and no per-id confirmation.
		{"-30d", 0, true},
		{"-12h", 0, true},
		{"0d", 0, true},
		{"0h", 0, true},
	}
	for _, tc := range cases {
		got, err := parseOlderThan(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseOlderThan(%q) error = nil, want an error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseOlderThan(%q) error = %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseOlderThan(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// --- command tree ---

func TestSessionsCommandTreeAndFlags(t *testing.T) {
	cmd := newSessionsCmd()
	var list, show, rm bool
	for _, sub := range cmd.Commands() {
		switch sub.Name() {
		case "list":
			list = true
			if sub.Flags().Lookup("json") == nil {
				t.Error("list missing --json")
			}
		case "show":
			show = true
			if sub.Flags().Lookup("json") == nil {
				t.Error("show missing --json")
			}
		case "rm":
			rm = true
			for _, f := range []string{"json", "older-than", "yes"} {
				if sub.Flags().Lookup(f) == nil {
					t.Errorf("rm missing --%s", f)
				}
			}
		}
	}
	if !list || !show || !rm {
		t.Fatalf("sessions command tree missing a verb: list=%v show=%v rm=%v", list, show, rm)
	}
}
