package speaker

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/config"
)

func TestApplyThreshold(t *testing.T) {
	tests := []struct {
		label, want string
		conf, th    float32
	}{
		{"lance", "lance", 0.9, 0.6},
		{"Lance", "Lance", 0.9, 0.6}, // preserve casing
		{"lance", LabelUnknown, 0.4, 0.6},
		{"lance", LabelUnknown, 0.6, 0.6}, // conf < threshold only; equal passes? conf < th rejects
		{"", LabelUnknown, 0.99, 0.6},
		{LabelUnknown, LabelUnknown, 0.99, 0.6},
	}
	// conf == threshold should pass (conf < threshold rejects)
	tests[3] = struct {
		label, want string
		conf, th    float32
	}{"lance", "lance", 0.6, 0.6}

	for _, tt := range tests {
		if got := ApplyThreshold(tt.label, tt.conf, tt.th); got != tt.want {
			t.Fatalf("ApplyThreshold(%q, %v, %v) = %q want %q", tt.label, tt.conf, tt.th, got, tt.want)
		}
	}
	// just below
	if got := ApplyThreshold("lance", 0.599, 0.6); got != LabelUnknown {
		t.Fatalf("just below: %q", got)
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
	if c.AnyActive() || c.LiveActive() || c.MeetingActive() {
		t.Fatal("zero config must keep features off")
	}
	// invalid mode clamps
	c = Config{Live: LiveConfig{Mode: "owner-verify"}}.Normalize()
	if c.Live.Mode != LiveModeIndicator {
		t.Fatalf("invalid mode clamp: %q", c.Live.Mode)
	}
	// live threshold inherits root
	c = Config{Threshold: 0.75, Live: LiveConfig{Threshold: 0}}.Normalize()
	if c.Live.Threshold != 0.75 {
		t.Fatalf("inherit live threshold: %v", c.Live.Threshold)
	}
	c.Enabled = true
	c.Live.Enabled = true
	c.Meeting.Enabled = true
	if !c.LiveActive() || !c.MeetingActive() || !c.AnyActive() {
		t.Fatal("expected active flags")
	}
}

func TestNewAnalyzerRequiresEngineWhenActive(t *testing.T) {
	_, err := NewAnalyzer(Config{Enabled: true, Live: LiveConfig{Enabled: true}}, nil)
	if err == nil {
		t.Fatal("expected error when live active without engine")
	}
	// master on but no sub-path: engine optional
	a, err := NewAnalyzer(Config{Enabled: true}, nil)
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
	// Event should carry timeline payload
	select {
	case ev := <-a.Events():
		if ev.Kind != EventTimelineFinalized || ev.Timeline == nil || len(ev.Timeline.Observations) != 2 {
			t.Fatalf("finalize event: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("expected finalize event")
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

func TestIdentifySegmentThresholdAndEmpty(t *testing.T) {
	eng := &FakeEngine{
		NextEmbed:  []float32{0.5, 0, 0, 0},
		Identities: map[string]string{"0.5000": "Lance"},
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
		ID: "1", Start: 0, End: time.Second, Samples: []float32{0.5}, Source: SourceLocalMic,
	})
	if err != nil {
		t.Fatal(err)
	}
	if obs.Label != "Lance" { // preserve casing
		t.Fatalf("label = %q", obs.Label)
	}
	if obs.StartMS != 0 || obs.EndMS != 1000 {
		t.Fatalf("ms fields: %+v", obs)
	}

	// Empty samples → rejected
	obs, err = a.IdentifySegment(context.Background(), Segment{
		Samples: nil, Source: SourceLocalMic,
	})
	if err != nil {
		t.Fatal(err)
	}
	if obs.State != StateRejected || obs.Label != LabelUnknown {
		t.Fatalf("empty samples: %+v", obs)
	}

	// Low confidence → unknown after threshold
	eng.NextEmbed = []float32{0.1, 0, 0, 0}
	eng.Identities = map[string]string{}
	obs, err = a.IdentifySegment(context.Background(), Segment{
		Samples: []float32{0.1}, Source: SourceLocalMic,
	})
	if err != nil {
		t.Fatal(err)
	}
	if obs.Label != LabelUnknown || obs.State != StateStable {
		t.Fatalf("low conf: %+v", obs)
	}
}

func TestIdentifySegmentContextCancel(t *testing.T) {
	eng := &FakeEngine{NextEmbed: []float32{0.5}}
	a, err := NewAnalyzer(Config{Enabled: true, Live: LiveConfig{Enabled: true}}, eng)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = a.IdentifySegment(ctx, Segment{Samples: []float32{0.5}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestIdentifyThresholdLiveVsMeeting(t *testing.T) {
	// Meeting-only: uses root Threshold 0.9; conf 0.95 passes, live.threshold ignored.
	eng := &FakeEngine{
		NextEmbed:  []float32{0.5},
		Identities: map[string]string{"0.5000": "x"},
	}
	// Fake always returns 0.95 for hits
	a, err := NewAnalyzer(Config{
		Enabled:   true,
		Threshold: 0.9,
		Live:      LiveConfig{Enabled: false, Threshold: 0.99}, // must not apply
		Meeting:   MeetingConfig{Enabled: true},
	}, eng)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	obs, err := a.IdentifySegment(context.Background(), Segment{
		Samples: []float32{0.5}, Source: SourceRecording,
	})
	if err != nil {
		t.Fatal(err)
	}
	if obs.Label != "x" {
		t.Fatalf("meeting path label=%q", obs.Label)
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

func TestCloseRejectsIdentify(t *testing.T) {
	eng := &FakeEngine{NextEmbed: []float32{0.5}}
	a, err := NewAnalyzer(Config{Enabled: true, Live: LiveConfig{Enabled: true}}, eng)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	obs, err := a.IdentifySegment(context.Background(), Segment{Samples: []float32{0.5}})
	if err == nil {
		t.Fatal("expected closed error")
	}
	if obs.State != StateRejected {
		t.Fatalf("state=%v", obs.State)
	}
}

func TestObservationJSONUsesMilliseconds(t *testing.T) {
	obs := Observation{StartMS: 1500, EndMS: 2000, Label: "speaker-1", State: StateStable}
	b, err := json.Marshal(obs)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["start_ms"] != float64(1500) || m["end_ms"] != float64(2000) {
		t.Fatalf("json=%s", b)
	}
	// must not look like nanoseconds
	if m["start"] != nil {
		t.Fatalf("unexpected start key: %s", b)
	}
}

func TestFromAppConfigDefaults(t *testing.T) {
	// Fresh viper defaults via empty Config zero value mapped after setDefaults
	// is not loaded; construct app config as Load would leave speaker zero
	// then FromAppConfig + Normalize.
	app := &config.Config{}
	// Simulate defaults from setDefaults without full Load
	app.Speaker.Threshold = 0.6
	app.Speaker.Live.WindowMS = 1500
	app.Speaker.Live.Mode = "indicator"
	sc := FromAppConfig(app)
	if sc.Enabled || sc.Live.Enabled || sc.Meeting.Enabled {
		t.Fatalf("must stay off: %+v", sc)
	}
	if sc.Threshold != 0.6 || sc.Live.WindowMS != 1500 {
		t.Fatalf("%+v", sc)
	}
	// inherit live threshold
	app.Speaker.Threshold = 0.8
	app.Speaker.Live.Threshold = 0
	sc = FromAppConfig(app)
	if sc.Live.Threshold != 0.8 {
		t.Fatalf("inherit: %v", sc.Live.Threshold)
	}
}

func TestConcurrentIdentifyFinalizeClose(t *testing.T) {
	eng := &FakeEngine{
		NextEmbed:  []float32{0.5},
		Identities: map[string]string{"0.5000": "a"},
	}
	a, err := NewAnalyzer(Config{
		Enabled: true,
		Live:    LiveConfig{Enabled: true},
		Meeting: MeetingConfig{Enabled: true},
	}, eng)
	if err != nil {
		t.Fatal(err)
	}
	samples := make([]float32, 8000)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = a.IdentifySegment(context.Background(), Segment{Samples: []float32{0.5}, Source: SourceLocalMic})
		}()
		go func() {
			defer wg.Done()
			_, _ = a.Finalize(context.Background(), samples)
		}()
	}
	wg.Wait()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	// after close, engine closed
	if !eng.Closed {
		t.Fatal("engine not closed")
	}
}
