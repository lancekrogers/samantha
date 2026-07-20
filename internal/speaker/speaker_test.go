package speaker

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestApplyThreshold(t *testing.T) {
	tests := []struct {
		label, want string
		conf, th    float32
	}{
		{"lance", "lance", 0.9, 0.6},
		{"Lance", "lance", 0.9, 0.6},
		{"lance", LabelUnknown, 0.4, 0.6},
		{"", LabelUnknown, 0.99, 0.6},
		{LabelUnknown, LabelUnknown, 0.99, 0.6},
	}
	for _, tt := range tests {
		if got := ApplyThreshold(tt.label, tt.conf, tt.th); got != tt.want {
			t.Fatalf("ApplyThreshold(%q, %v, %v) = %q want %q", tt.label, tt.conf, tt.th, got, tt.want)
		}
	}
}

func TestMapDiarizationID(t *testing.T) {
	if got := MapDiarizationID(0); got != "speaker-1" {
		t.Fatalf("got %q", got)
	}
	if got := MapDiarizationID(2); got != "speaker-3" {
		t.Fatalf("got %q", got)
	}
	if got := MapDiarizationID(-1); got != LabelUnknown {
		t.Fatalf("got %q", got)
	}
}

func TestConfigNormalizeAndFlags(t *testing.T) {
	c := Config{}.Normalize()
	if c.Threshold != 0.6 || c.Live.WindowMS != 1500 || c.Live.Mode != LiveModeIndicator {
		t.Fatalf("defaults: %+v", c)
	}
	if c.EffectiveEnabled() || c.LiveActive() || c.MeetingActive() {
		t.Fatal("zero config must keep features off")
	}
	c.Enabled = true
	c.Live.Enabled = true
	c.Meeting.Enabled = true
	if !c.LiveActive() || !c.MeetingActive() {
		t.Fatal("expected active flags")
	}
}

func TestNewAnalyzerRequiresEngineWhenEnabled(t *testing.T) {
	_, err := NewAnalyzer(Config{Enabled: true}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	a, err := NewAnalyzer(Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = a.Close()
}

func TestAnalyzerFinalizeDisabledEmpty(t *testing.T) {
	a, err := NewAnalyzer(Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	tl, err := a.Finalize(context.Background(), []float32{0.1, 0.2})
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Observations) != 0 {
		t.Fatalf("disabled finalize should be empty: %+v", tl)
	}
}

func TestAnalyzerFinalizeWithFake(t *testing.T) {
	eng := &FakeEngine{}
	a, err := NewAnalyzer(Config{
		Enabled: true,
		Meeting: MeetingConfig{Enabled: true},
	}, eng)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	// 1 second of silence at 16 kHz.
	samples := make([]float32, 16000)
	tl, err := a.Finalize(context.Background(), samples)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Observations) != 2 {
		t.Fatalf("want 2 default diarization spans, got %d", len(tl.Observations))
	}
	if tl.Observations[0].Label != "speaker-1" || tl.Observations[1].Label != "speaker-2" {
		t.Fatalf("labels: %+v", tl.Observations)
	}
	if tl.FinalizedAt.IsZero() {
		t.Fatal("FinalizedAt unset")
	}
}

func TestAnalyzerFinalizeContextCancel(t *testing.T) {
	eng := &FakeEngine{}
	a, err := NewAnalyzer(Config{Enabled: true, Meeting: MeetingConfig{Enabled: true}}, eng)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = a.Finalize(ctx, []float32{1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestIdentifySegmentThreshold(t *testing.T) {
	eng := &FakeEngine{
		NextEmbed:  []float32{0.5, 0, 0, 0},
		Identities: map[string]string{"0.5000": "lance"},
	}
	a, err := NewAnalyzer(Config{
		Enabled:   true,
		Threshold: 0.6,
		Live:      LiveConfig{Enabled: true, Threshold: 0.6},
	}, eng)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	obs, err := a.IdentifySegment(context.Background(), Segment{
		ID: "1", Start: 0, End: time.Second, Samples: []float32{0.5}, Source: SourceFixture,
	})
	if err != nil {
		t.Fatal(err)
	}
	if obs.Label != "lance" {
		t.Fatalf("label = %q", obs.Label)
	}

	// Low confidence identity → unknown
	eng.Identities = map[string]string{}
	obs, err = a.IdentifySegment(context.Background(), Segment{
		Samples: []float32{0.1}, Source: SourceFixture,
	})
	if err != nil {
		t.Fatal(err)
	}
	if obs.Label != LabelUnknown {
		t.Fatalf("want unknown, got %q", obs.Label)
	}
}

func TestFakeDiarizeFailure(t *testing.T) {
	eng := &FakeEngine{FailDiarize: errors.New("boom")}
	a, err := NewAnalyzer(Config{Enabled: true, Meeting: MeetingConfig{Enabled: true}}, eng)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	_, err = a.Finalize(context.Background(), []float32{1, 2, 3})
	if err == nil {
		t.Fatal("expected diarize error")
	}
}
