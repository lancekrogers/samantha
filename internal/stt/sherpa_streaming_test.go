package stt

import "testing"

func TestNormalizeTranscriptSentenceCasesUppercaseResults(t *testing.T) {
	got := normalizeTranscript("HELLO WORLD. I AM SAMANTHA")
	want := "Hello world. I am samantha"
	if got != want {
		t.Fatalf("normalizeTranscript() = %q, want %q", got, want)
	}
}

func TestNormalizeTranscriptLeavesMixedCaseUntouched(t *testing.T) {
	got := normalizeTranscript("git status in projects/samantha")
	want := "git status in projects/samantha"
	if got != want {
		t.Fatalf("normalizeTranscript() = %q, want %q", got, want)
	}
}
