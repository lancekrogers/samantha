package endpoint

import (
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/config"
)

func livePolicy() Policy {
	return Policy{
		MinSpeech:    200 * time.Millisecond,
		MinSilence:   500 * time.Millisecond,
		MaxUtterance: 30 * time.Second,
		StartTimeout: 10 * time.Second,
	}
}

func fixturePolicy() Policy {
	p := livePolicy()
	p.FinalizeOnEOF = true
	p.StartTimeout = 0
	return p
}

func TestPolicyDecide(t *testing.T) {
	tests := []struct {
		name   string
		policy Policy
		obs    Observation
		want   DecisionKind
	}{
		{
			name:   "live waiting for speech",
			policy: livePolicy(),
			obs:    Observation{HasSpeech: false, Elapsed: 2 * time.Second},
			want:   Continue,
		},
		{
			name:   "live start timeout with no speech",
			policy: livePolicy(),
			obs:    Observation{HasSpeech: false, Elapsed: 10 * time.Second},
			want:   Timeout,
		},
		{
			name:   "live speech in progress",
			policy: livePolicy(),
			obs:    Observation{HasSpeech: true, SpeechSeen: 800 * time.Millisecond, TrailingSilence: 100 * time.Millisecond, Elapsed: 1 * time.Second},
			want:   Continue,
		},
		{
			name:   "live finalize on trailing silence",
			policy: livePolicy(),
			obs:    Observation{HasSpeech: true, SpeechSeen: 800 * time.Millisecond, TrailingSilence: 500 * time.Millisecond, Elapsed: 2 * time.Second},
			want:   Finalize,
		},
		{
			name:   "live too-short speech rejected on silence",
			policy: livePolicy(),
			obs:    Observation{HasSpeech: true, SpeechSeen: 100 * time.Millisecond, TrailingSilence: 600 * time.Millisecond, Elapsed: 1 * time.Second},
			want:   TooShort,
		},
		{
			name:   "live max utterance finalizes long speech",
			policy: livePolicy(),
			obs:    Observation{HasSpeech: true, SpeechSeen: 25 * time.Second, TrailingSilence: 0, Elapsed: 39 * time.Second, SpeechElapsed: 30 * time.Second},
			want:   Finalize,
		},
		{
			name:   "max utterance anchored at speech onset, not listen start",
			policy: livePolicy(),
			obs:    Observation{HasSpeech: true, SpeechSeen: 20 * time.Second, TrailingSilence: 0, Elapsed: 31 * time.Second, SpeechElapsed: 22 * time.Second},
			want:   Continue, // 9s of pre-speech silence must not eat the 30s budget
		},
		{
			name:   "max utterance with too little speech rejects",
			policy: livePolicy(),
			obs:    Observation{HasSpeech: true, SpeechSeen: 100 * time.Millisecond, Elapsed: 31 * time.Second, SpeechElapsed: 30 * time.Second},
			want:   TooShort,
		},
		{
			name:   "provider endpoint finalizes when allowed",
			policy: Policy{MinSpeech: 200 * time.Millisecond, MinSilence: 500 * time.Millisecond, AllowProviderEnd: true},
			obs:    Observation{HasSpeech: true, SpeechSeen: 1 * time.Second, ProviderEnd: true},
			want:   Finalize,
		},
		{
			name:   "provider endpoint ignored when not allowed",
			policy: livePolicy(), // AllowProviderEnd false
			obs:    Observation{HasSpeech: true, SpeechSeen: 1 * time.Second, ProviderEnd: true, TrailingSilence: 100 * time.Millisecond, Elapsed: 1 * time.Second},
			want:   Continue,
		},
		{
			name:   "fixture EOF finalizes buffered speech",
			policy: fixturePolicy(),
			obs:    Observation{HasSpeech: true, SpeechSeen: 1 * time.Second, SourceFinal: true},
			want:   Finalize,
		},
		{
			name:   "fixture EOF rejects too-short speech",
			policy: fixturePolicy(),
			obs:    Observation{HasSpeech: true, SpeechSeen: 50 * time.Millisecond, SourceFinal: true},
			want:   TooShort,
		},
		{
			name:   "fixture EOF without speech is exhausted",
			policy: fixturePolicy(),
			obs:    Observation{HasSpeech: false, SourceFinal: true},
			want:   SourceExhausted,
		},
		{
			name:   "EOF without FinalizeOnEOF is exhausted",
			policy: livePolicy(), // FinalizeOnEOF false
			obs:    Observation{HasSpeech: true, SpeechSeen: 1 * time.Second, SourceFinal: true},
			want:   SourceExhausted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.policy.Decide(tt.obs)
			if got.Kind != tt.want {
				t.Fatalf("Decide() = %s (%q), want %s", got.Kind, got.Reason, tt.want)
			}
			if got.Reason == "" {
				t.Error("Decide().Reason is empty, want an explanation")
			}
		})
	}
}

func TestFromConfigLiveVsFinite(t *testing.T) {
	cfg := &config.Config{
		VADSilenceDuration: 0.5,
		ListenTimeout:      10,
		PhraseTimeLimit:    30,
	}

	live := FromConfig(cfg, false)
	if live.FinalizeOnEOF {
		t.Error("live policy FinalizeOnEOF = true, want false")
	}
	if live.StartTimeout != 10*time.Second {
		t.Errorf("live StartTimeout = %v, want 10s", live.StartTimeout)
	}
	if live.MinSilence != 500*time.Millisecond {
		t.Errorf("live MinSilence = %v, want 500ms", live.MinSilence)
	}
	if live.MaxUtterance != 30*time.Second {
		t.Errorf("live MaxUtterance = %v, want 30s", live.MaxUtterance)
	}

	finite := FromConfig(cfg, true)
	if !finite.FinalizeOnEOF {
		t.Error("finite policy FinalizeOnEOF = false, want true")
	}
	if finite.StartTimeout != 0 {
		t.Errorf("finite StartTimeout = %v, want 0 (EOF bounds finite sources)", finite.StartTimeout)
	}
}

func TestFromConfigFallsBackOnZeroValues(t *testing.T) {
	cfg := &config.Config{} // all zero
	p := FromConfig(cfg, false)
	if p.MinSilence != 500*time.Millisecond {
		t.Errorf("MinSilence fallback = %v, want 500ms", p.MinSilence)
	}
	if p.StartTimeout != 10*time.Second {
		t.Errorf("StartTimeout fallback = %v, want 10s", p.StartTimeout)
	}
	if p.MaxUtterance != 30*time.Second {
		t.Errorf("MaxUtterance fallback = %v, want 30s", p.MaxUtterance)
	}
}

func TestDecisionKindString(t *testing.T) {
	cases := map[DecisionKind]string{
		Continue:         "continue",
		Finalize:         "finalize",
		Timeout:          "timeout",
		TooShort:         "too_short",
		SourceExhausted:  "source_exhausted",
		DecisionKind(99): "unknown",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Errorf("DecisionKind(%d).String() = %q, want %q", k, got, want)
		}
	}
}
