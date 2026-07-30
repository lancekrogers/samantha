package speaker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// stubCapture is a CaptureSource that never emits, so LiveFeed starts and drains
// without hardware or timing dependence.
type stubCapture struct {
	mu          sync.Mutex
	subscribed  int
	unsubscribe int
	ch          chan []float32
}

func newStubCapture() *stubCapture {
	return &stubCapture{ch: make(chan []float32)}
}

func (s *stubCapture) Subscribe(int) (int, <-chan []float32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscribed++
	return s.subscribed, s.ch
}

func (s *stubCapture) Unsubscribe(int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unsubscribe++
}

func (s *stubCapture) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subscribed, s.unsubscribe
}

func liveOnConfig() Config {
	cfg := Config{Enabled: true}
	cfg.Live.Enabled = true
	return cfg.Normalize()
}

// waitForStatus polls Stats until status matches, because the engine build is
// deliberately asynchronous.
func waitForStatus(t *testing.T, l *LazyLive, want LiveStatus) LiveStats {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last LiveStats
	for time.Now().Before(deadline) {
		last = l.Stats()
		if last.Status == want {
			return last
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("status = %q, want %q (last stats %+v)", last.Status, want, last)
	return last
}

// waitForBuild blocks until the capture feed is subscribed, which is the
// externally visible effect of a completed build. Status cannot serve here:
// SetEnabled reports Running synchronously and the installed adapter reports
// Running too, so there is no status edge to wait on.
func waitForBuild(t *testing.T, capture *stubCapture) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if subs, _ := capture.counts(); subs > 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("engine build did not subscribe to capture")
}

// TestLazyLiveBuildsNothingUntilEnabled is the whole point of the type: an
// install with speakers off must not load a model or touch the mic.
func TestLazyLiveBuildsNothingUntilEnabled(t *testing.T) {
	capture := newStubCapture()
	built := 0
	l := NewLazyLive(context.Background(), liveOnConfig(), capture, func() (Engine, error) {
		built++
		return &FakeEngine{}, nil
	}, "")
	defer func() { _ = l.Close() }()

	stats := l.Stats()
	if stats.Status != LiveDisabled {
		t.Errorf("initial status = %q, want %q", stats.Status, LiveDisabled)
	}
	if built != 0 {
		t.Errorf("engine built %d times before enable, want 0", built)
	}
	if subs, _ := capture.counts(); subs != 0 {
		t.Errorf("capture subscribed %d times before enable, want 0", subs)
	}
}

// TestLazyLiveEnableBuildsOnDemand covers the bug this type exists to fix:
// /speakers on used to be a no-op when config started live analysis off.
func TestLazyLiveEnableBuildsOnDemand(t *testing.T) {
	capture := newStubCapture()
	l := NewLazyLive(context.Background(), liveOnConfig(), capture, func() (Engine, error) {
		return &FakeEngine{}, nil
	}, "")
	defer func() { _ = l.Close() }()

	l.SetEnabled(true)
	waitForBuild(t, capture)

	if subs, _ := capture.counts(); subs != 1 {
		t.Errorf("capture subscribed %d times, want 1", subs)
	}
	if status := l.Stats().Status; status != LiveRunning && status != LiveHealthy {
		t.Errorf("status after on-demand build = %q, want running or healthy", status)
	}
}

// TestLazyLiveBuildFailureReportsReason keeps a failed model load explainable
// instead of a silent nothing-happens.
func TestLazyLiveBuildFailureReportsReason(t *testing.T) {
	l := NewLazyLive(context.Background(), liveOnConfig(), newStubCapture(), func() (Engine, error) {
		return nil, errors.New("titanet model missing")
	}, "")
	defer func() { _ = l.Close() }()

	l.SetEnabled(true)
	stats := waitForStatus(t, l, LiveUnavailable)
	if stats.LastError == "" {
		t.Fatal("failed build should report LastError")
	}
	if got := stats.LastError; got != "titanet model missing" {
		t.Errorf("LastError = %q, want the builder error", got)
	}
}

// TestLazyLiveNilBuilderStaysUnavailable is the --text / no-capture runtime:
// enabling must not panic or pretend to work.
func TestLazyLiveNilBuilderStaysUnavailable(t *testing.T) {
	l := NewLazyLive(context.Background(), liveOnConfig(), nil, nil, "no microphone")
	defer func() { _ = l.Close() }()

	l.SetEnabled(true)
	stats := l.Stats()
	if stats.Status != LiveUnavailable {
		t.Errorf("status = %q, want %q", stats.Status, LiveUnavailable)
	}
	if stats.LastError != "no microphone" {
		t.Errorf("LastError = %q, want the construction detail", stats.LastError)
	}
}

// TestLazyLiveDisableThenReEnableKeepsEngine proves a toggle round-trip reuses
// the loaded engine rather than paying the model load twice.
func TestLazyLiveDisableThenReEnableKeepsEngine(t *testing.T) {
	var mu sync.Mutex
	built := 0
	capture := newStubCapture()
	l := NewLazyLive(context.Background(), liveOnConfig(), capture, func() (Engine, error) {
		mu.Lock()
		built++
		mu.Unlock()
		return &FakeEngine{}, nil
	}, "")
	defer func() { _ = l.Close() }()

	l.SetEnabled(true)
	waitForBuild(t, capture)
	l.SetEnabled(false)
	if status := l.Stats().Status; status != LiveDisabled {
		t.Fatalf("status after disable = %q, want %q", status, LiveDisabled)
	}
	l.SetEnabled(true)
	if status := l.Stats().Status; status != LiveRunning && status != LiveHealthy {
		t.Fatalf("status after re-enable = %q, want running or healthy", status)
	}

	mu.Lock()
	defer mu.Unlock()
	if built != 1 {
		t.Errorf("engine built %d times across a toggle round-trip, want 1", built)
	}
}

// TestLazyLiveDisableDuringBuildIsHonored covers the mid-load change of mind:
// a model load takes long enough that /speakers on immediately followed by
// /speakers off must not come up analyzing.
func TestLazyLiveDisableDuringBuildIsHonored(t *testing.T) {
	release := make(chan struct{})
	capture := newStubCapture()
	l := NewLazyLive(context.Background(), liveOnConfig(), capture, func() (Engine, error) {
		<-release
		return &FakeEngine{}, nil
	}, "")
	defer func() { _ = l.Close() }()

	l.SetEnabled(true)
	l.SetEnabled(false)
	close(release)
	waitForBuild(t, capture)

	if status := waitForStatus(t, l, LiveDisabled).Status; status != LiveDisabled {
		t.Errorf("status = %q, want %q", status, LiveDisabled)
	}
}

// TestLazyLiveCloseDuringBuildReleasesChain guards the race where Close lands
// while a model is still loading: the finished chain must be torn down, not
// leaked into a closed controller.
func TestLazyLiveCloseDuringBuildReleasesChain(t *testing.T) {
	release := make(chan struct{})
	engine := &FakeEngine{}
	capture := newStubCapture()
	l := NewLazyLive(context.Background(), liveOnConfig(), capture, func() (Engine, error) {
		<-release
		return engine, nil
	}, "")

	l.SetEnabled(true)
	if err := l.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	close(release)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if engine.IsClosed() {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !engine.IsClosed() {
		t.Error("engine built after Close was not released")
	}
	if _, unsubs := capture.counts(); unsubs == 0 {
		t.Error("capture feed built after Close was not drained")
	}
	if status := l.Stats().Status; status != LiveClosed {
		t.Errorf("status = %q, want %q", status, LiveClosed)
	}
}

// TestLazyLiveOnDemandFromDisabledConfigProducesLabels is the regression that
// matters most: turning speakers on from a config that had live analysis off
// must actually label. The analyzer gates IdentifySegment on LiveActive, so a
// controller that passed the on-disk config straight through would load a model
// and then reject every window — a silent no-op wearing a healthy status.
func TestLazyLiveOnDemandFromDisabledConfigProducesLabels(t *testing.T) {
	offConfig := Config{}.Normalize() // speakers off, exactly as shipped
	l := NewLazyLive(context.Background(), offConfig, newStubCapture(), func() (Engine, error) {
		return &FakeEngine{}, nil
	}, "")
	defer func() { _ = l.Close() }()

	analyzer, err := NewAnalyzer(l.cfg, &FakeEngine{})
	if err != nil {
		t.Fatalf("NewAnalyzer with controller config error = %v", err)
	}
	defer func() { _ = analyzer.Close() }()

	obs, err := analyzer.IdentifySegment(context.Background(), Segment{
		ID:      "seg-1",
		Samples: make([]float32, minLiveEmbedSamples),
		Source:  SourceLocalMic,
	})
	if err != nil {
		t.Fatalf("IdentifySegment error = %v", err)
	}
	if obs.State == StateRejected {
		t.Fatalf("segment rejected despite on-demand enable: %+v", obs)
	}
}

// TestLazyLiveResetWithoutEngineIsNoError lets callers reset a session before
// speakers were ever switched on.
func TestLazyLiveResetWithoutEngineIsNoError(t *testing.T) {
	l := NewLazyLive(context.Background(), liveOnConfig(), newStubCapture(), func() (Engine, error) {
		return &FakeEngine{}, nil
	}, "")
	defer func() { _ = l.Close() }()

	if err := l.Reset(); err != nil {
		t.Errorf("Reset() before build error = %v, want nil", err)
	}
}
