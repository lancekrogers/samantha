//go:build !integration

package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/meeting"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
	appTUI "github.com/lancekrogers/samantha/internal/tui"
)

func TestCLIAutoRouteDest(t *testing.T) {
	autoCfg := meeting.Config{Mode: meeting.ModeAuto, Default: "camp:obey"}
	cases := []struct {
		name string
		cfg  meeting.Config
		opts meetingOptions
		want string
	}{
		{"route flag wins", autoCfg, meetingOptions{RouteTo: "docs"}, "docs"},
		{"no-route suppresses", autoCfg, meetingOptions{RouteTo: "docs", NoRoute: true}, ""},
		{"auto default", autoCfg, meetingOptions{}, "camp:obey"},
		{"ask mode is interactive", meeting.Config{Mode: meeting.ModeAsk, Default: "camp:obey"}, meetingOptions{}, ""},
		{"auto without default", meeting.Config{Mode: meeting.ModeAuto}, meetingOptions{}, ""},
	}
	for _, tc := range cases {
		if got := cliAutoRouteDest(tc.cfg, tc.opts); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestAwaitDiarizeReportsAndSkipsProgressWhenDone(t *testing.T) {
	cmd := &cobra.Command{}
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)

	// Outcome already available: no progress chatter.
	ready := make(chan appTUI.MeetingAnalysisOutcome, 1)
	ready <- appTUI.MeetingAnalysisOutcome{Result: meeting.AnalysisResult{Status: meeting.AnalysisComplete, SpeakerCount: 2}}
	close(ready)
	result, ran := awaitDiarize(cmd, ready, func() {})
	if !ran || result.SpeakerCount != 2 {
		t.Fatalf("ran=%v result=%+v", ran, result)
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected progress output: %q", stderr.String())
	}

	// Outcome pending: progress line, then the delivered result.
	pending := make(chan appTUI.MeetingAnalysisOutcome, 1)
	go func() {
		time.Sleep(20 * time.Millisecond)
		pending <- appTUI.MeetingAnalysisOutcome{Result: meeting.AnalysisResult{Status: meeting.AnalysisError, Error: "cancelled"}}
		close(pending)
	}()
	cancelled := false
	result, ran = awaitDiarize(cmd, pending, func() { cancelled = true })
	if !ran || result.Error != "cancelled" || !cancelled {
		t.Fatalf("ran=%v cancelled=%v result=%+v", ran, cancelled, result)
	}
	if !strings.Contains(stderr.String(), "Diarizing speakers") {
		t.Fatalf("missing progress line: %q", stderr.String())
	}

	// No analysis session at all.
	if _, ran := awaitDiarize(cmd, nil, nil); ran {
		t.Fatal("nil channel must report not-ran")
	}
}

func TestFoldSpeakerOutcome(t *testing.T) {
	summary := meetinglog.Summary{Description: "d"}
	foldSpeakerOutcome(&summary, meeting.AnalysisResult{
		Status: meeting.AnalysisComplete, SpeakerCount: 3,
		Artifact: "/tmp/a.json", AudioFile: "/tmp/a.wav",
	})
	if summary.SpeakerStatus != string(meeting.AnalysisComplete) || summary.SpeakerCount != 3 ||
		summary.SpeakerAnalysisFile != "/tmp/a.json" || summary.AudioFile != "/tmp/a.wav" {
		t.Fatalf("summary = %+v", summary)
	}
}
