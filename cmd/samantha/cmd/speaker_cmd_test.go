//go:build !integration

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/speaker"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

func speakerTestConfig(dir string) func() (*config.Config, error) {
	return func() (*config.Config, error) {
		return &config.Config{Speaker: config.SpeakerConfig{EnrollmentDir: dir}}, nil
	}
}

func TestSpeakerListJSONEmptyStorePrintsEmptyArray(t *testing.T) {
	dir := t.TempDir()
	cmd := newSpeakerCmd(speakerTestConfig(dir))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"list", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("speaker list --json: %v", err)
	}
	got := strings.TrimSpace(out.String())
	if got != "[]" {
		t.Fatalf("empty store output = %q, want %q", got, "[]")
	}
}

func TestSpeakerListJSONCarriesStaleness(t *testing.T) {
	dir := t.TempDir()
	store, err := speaker.OpenEnrollment(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Fresh profile at the config's default window (1500ms) — matches.
	if _, err := store.Add(context.Background(), "Lance", speaker.ExpectedLiveRev(speaker.Config{}.Normalize()), [][]float32{{1, 2}}); err != nil {
		t.Fatal(err)
	}
	// Stale profile: recorded under a different revision string entirely.
	if _, err := store.Add(context.Background(), "Old", "nemo-titanet-small-live-w3000", [][]float32{{3, 4}}); err != nil {
		t.Fatal(err)
	}

	cmd := newSpeakerCmd(speakerTestConfig(dir))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"list", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("speaker list --json: %v", err)
	}

	var rows []speakerListRow
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("decode rows: %v\noutput: %s", err, out.String())
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want 2", rows)
	}
	byName := map[string]speakerListRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	fresh, ok := byName["Lance"]
	if !ok {
		t.Fatalf("missing Lance row: %+v", rows)
	}
	if fresh.Stale {
		t.Fatalf("fresh profile marked stale: %+v", fresh)
	}
	if fresh.WindowMS != 1500 {
		t.Fatalf("fresh.WindowMS = %d, want 1500", fresh.WindowMS)
	}
	if fresh.ExpectedModelRevision == "" {
		t.Fatal("expected_model_revision must not be empty")
	}
	stale, ok := byName["Old"]
	if !ok {
		t.Fatalf("missing Old row: %+v", rows)
	}
	if !stale.Stale {
		t.Fatalf("mismatched-revision profile not marked stale: %+v", stale)
	}
	if stale.ExpectedModelRevision != fresh.ExpectedModelRevision {
		t.Fatalf("expected_model_revision must be the same for every row: %q vs %q", stale.ExpectedModelRevision, fresh.ExpectedModelRevision)
	}
}

func TestSpeakerRemoveJSONPrintsConfirmation(t *testing.T) {
	dir := t.TempDir()
	store, err := speaker.OpenEnrollment(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(context.Background(), "Lance", "rev", [][]float32{{1, 2}}); err != nil {
		t.Fatal(err)
	}

	cmd := newSpeakerCmd(speakerTestConfig(dir))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"remove", "Lance", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("speaker remove --json: %v", err)
	}

	var got struct {
		Removed string `json:"removed"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode confirmation: %v\noutput: %s", err, out.String())
	}
	if got.Removed != "Lance" {
		t.Fatalf("removed = %q, want %q", got.Removed, "Lance")
	}

	reopened, err := speaker.OpenEnrollment(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.List()) != 0 {
		t.Fatalf("profile still enrolled after remove: %+v", reopened.List())
	}
}

func TestSpeakerRemoveJSONUnknownNameErrorsWithoutJSONOnStdout(t *testing.T) {
	dir := t.TempDir()
	cmd := newSpeakerCmd(speakerTestConfig(dir))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"remove", "ghost", "--json"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("remove of an unknown speaker must fail")
	}
	if !strings.Contains(err.Error(), `no speaker named "ghost" is enrolled`) {
		t.Fatalf("error = %q, want the standard not-enrolled message", err)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout must stay empty on error, got %q", out.String())
	}
}

func TestSpeakerRenameJSONPrintsProfile(t *testing.T) {
	dir := t.TempDir()
	store, err := speaker.OpenEnrollment(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(context.Background(), "Lance", "rev", [][]float32{{1, 2}}); err != nil {
		t.Fatal(err)
	}

	cmd := newSpeakerCmd(speakerTestConfig(dir))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"rename", "Lance", "Lance R", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("speaker rename --json: %v", err)
	}

	var profile speaker.Profile
	if err := json.Unmarshal(out.Bytes(), &profile); err != nil {
		t.Fatalf("decode profile: %v\noutput: %s", err, out.String())
	}
	if profile.Name != "Lance R" {
		t.Fatalf("profile.Name = %q, want %q", profile.Name, "Lance R")
	}
}

func TestSpeakerRenameHumanOutput(t *testing.T) {
	dir := t.TempDir()
	store, err := speaker.OpenEnrollment(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(context.Background(), "Lance", "rev", [][]float32{{1, 2}}); err != nil {
		t.Fatal(err)
	}

	cmd := newSpeakerCmd(speakerTestConfig(dir))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"rename", "Lance", "Lance R"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("speaker rename: %v", err)
	}
	want := `Renamed "Lance" → "Lance R" (embedding moved)`
	if !strings.Contains(out.String(), want) {
		t.Fatalf("output = %q, want it to contain %q", out.String(), want)
	}
}

func TestSpeakerRenameUnknownNameErrors(t *testing.T) {
	dir := t.TempDir()
	cmd := newSpeakerCmd(speakerTestConfig(dir))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"rename", "ghost", "someone"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("rename of an unknown speaker must fail")
	}
	if !strings.Contains(err.Error(), `no speaker named "ghost" is enrolled`) {
		t.Fatalf("error = %q, want the standard not-enrolled message", err)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout must stay empty on error, got %q", out.String())
	}
}

func TestSpeakerRenameRequiresTwoArgs(t *testing.T) {
	dir := t.TempDir()
	cmd := newSpeakerCmd(speakerTestConfig(dir))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"rename", "onlyone"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("rename with one arg must fail")
	}
}
