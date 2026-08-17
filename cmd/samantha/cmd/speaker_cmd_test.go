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
