package stt

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestToTyped(t *testing.T) {
	tests := []struct {
		name string
		in   Event
		want TypedEvent
	}{
		{
			"phase",
			PhaseEvent{Phase: "listening", Elapsed: int64(250 * time.Millisecond)},
			TypedEvent{Kind: KindPhase, Phase: "listening", Elapsed: 250 * time.Millisecond},
		},
		{
			"partial",
			PartialTranscript{Text: "open the"},
			TypedEvent{Kind: KindPartialTranscript, Text: "open the"},
		},
		{
			"final",
			FinalTranscript{Text: "open the door"},
			TypedEvent{Kind: KindFinalTranscript, Text: "open the door", Final: true},
		},
		{
			"timeout",
			Timeout{},
			TypedEvent{Kind: KindTimeout},
		},
		{
			"failure",
			Failure{Err: errors.New("mic disconnected")},
			TypedEvent{Kind: KindFailure, ErrText: "mic disconnected"},
		},
		{
			"failure nil err",
			Failure{},
			TypedEvent{Kind: KindFailure},
		},
		{
			"input level",
			InputLevel{Level: 0.42},
			TypedEvent{Kind: KindInputLevel, Level: 0.42},
		},
		{
			"nil event",
			nil,
			TypedEvent{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ToTyped(tt.in); got != tt.want {
				t.Fatalf("ToTyped(%#v) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestTypedEventJSONRoundTrip(t *testing.T) {
	want := TypedEvent{
		Kind:       KindFinalTranscript,
		Text:       "open the door",
		Phase:      "transcribing",
		Final:      true,
		ErrText:    "context deadline exceeded",
		Confidence: 0.87,
		Elapsed:    1500 * time.Millisecond,
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal(%+v): %v", want, err)
	}
	var got TypedEvent
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal(%s): %v", data, err)
	}
	if got != want {
		t.Fatalf("round-trip = %+v, want %+v", got, want)
	}
}
