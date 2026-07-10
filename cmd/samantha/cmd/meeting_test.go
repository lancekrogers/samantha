package cmd

import (
	"errors"
	"testing"
	"time"

	"github.com/charmbracelet/huh"
)

func TestResolveMeetingDescription(t *testing.T) {
	prompted := func(s string, err error) func() (string, error) {
		return func() (string, error) { return s, err }
	}
	neverPrompt := func() (string, error) {
		t.Fatal("prompt must not run")
		return "", nil
	}

	tests := []struct {
		name       string
		flag       string
		noTUI, tty bool
		prompt     func() (string, error)
		want       string
		cancelled  bool
		wantErr    bool
	}{
		{"explicit flag wins, no prompt", "Standup", false, true, neverPrompt, "Standup", false, false},
		{"no TTY falls back without prompting", "", false, false, neverPrompt, "meeting", false, false},
		{"--no-tui skips prompt even on a TTY", "", true, true, neverPrompt, "meeting", false, false},
		{"interactive prompt result used", "", false, true, prompted("Planning sync", nil), "Planning sync", false, false},
		{"empty prompt answer falls back", "", false, true, prompted("  ", nil), "meeting", false, false},
		{"form abort is a clean cancel", "", false, true, prompted("", huh.ErrUserAborted), "", true, false},
		{"form error propagates", "", false, true, prompted("", errors.New("boom")), "", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, cancelled, err := resolveMeetingDescription(tt.flag, tt.noTUI, tt.tty, tt.prompt)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if got != tt.want || cancelled != tt.cancelled {
				t.Fatalf("got (%q, %v), want (%q, %v)", got, cancelled, tt.want, tt.cancelled)
			}
		})
	}
}

func TestMeetingFilename(t *testing.T) {
	now := time.Date(2026, 7, 9, 9, 30, 0, 0, time.Local)
	tests := []struct {
		description string
		want        string
	}{
		{"Weekly planning sync", "weekly-planning-sync-20260709-093000.log"},
		{"", "meeting-20260709-093000.log"},
		{"!!!", "meeting-20260709-093000.log"},
		{"Q3 / OKR review!", "q3-okr-review-20260709-093000.log"},
	}
	for _, tt := range tests {
		if got := meetingFilename(tt.description, now); got != tt.want {
			t.Fatalf("meetingFilename(%q) = %q, want %q", tt.description, got, tt.want)
		}
	}
	long := meetingFilename("this description is very long and keeps going well past the sixty character slug cap for sure", now)
	if len(long) > 60+len("-20260709-093000.log") {
		t.Fatalf("slug not capped: %q", long)
	}
}

func TestStopPhraseSetMatchesExactNormalizedOnly(t *testing.T) {
	set := stopPhraseSet([]string{"That's a wrap"})
	for phrase, want := range map[string]bool{
		"Stop recording.":              true, // default, case+punctuation normalized
		"stop recording":               true,
		"end meeting!":                 true,
		"that's a wrap":                true,  // user-added
		"please stop recording":        false, // never substring
		"we should stop recording now": false,
		"keep going":                   false,
	} {
		if got := set[normalizeStopPhrase(phrase)]; got != want {
			t.Fatalf("match(%q) = %v, want %v", phrase, got, want)
		}
	}
}
