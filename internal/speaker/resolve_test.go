package speaker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/pkg/voiceagent/audio"
)

func liveTestConfig() Config {
	return Config{Enabled: true, Live: LiveConfig{Enabled: true}}.Normalize()
}

func meetingTestConfig() Config {
	return Config{Enabled: true, Meeting: MeetingConfig{Enabled: true}}.Normalize()
}

func TestIdentifySegmentStampsEnrollRev(t *testing.T) {
	eng := &FakeEngine{
		Identities: map[string]string{"0.5000": "lance"},
		Enrolled:   map[string]string{"lance": "titanet"},
	}
	a, err := NewAnalyzer(liveTestConfig(), eng)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()

	enrolled := make([]float32, 100)
	enrolled[0] = 0.5
	obs, err := a.IdentifySegment(context.Background(), Segment{ID: "s1", End: time.Second, Samples: enrolled})
	if err != nil {
		t.Fatal(err)
	}
	if obs.Label != "lance" || obs.EnrollRev != "titanet" {
		t.Fatalf("enrolled segment: label=%q rev=%q", obs.Label, obs.EnrollRev)
	}

	stranger := make([]float32, 100)
	stranger[0] = 0.9
	obs, err = a.IdentifySegment(context.Background(), Segment{ID: "s2", End: time.Second, Samples: stranger})
	if err != nil {
		t.Fatal(err)
	}
	if obs.EnrollRev != "" {
		t.Fatalf("unenrolled segment must not carry EnrollRev, got %q (label %q)", obs.EnrollRev, obs.Label)
	}
}

// resolveFixture builds a diarized timeline + PCM where FakeEngine's
// first-sample keying makes: speaker-1 spans vote "lance" unanimously,
// speaker-2's span hits an unenrolled identity, and speaker-3 splits 1/2.
func resolveFixture() (*FakeEngine, []float32) {
	ms := func(v int64) int { return int(v) * audio.SampleRate / 1000 }
	samples := make([]float32, ms(13000))
	samples[ms(0)] = 0.5     // speaker-1 span A → lance
	samples[ms(2500)] = 0.5  // speaker-1 span B → lance
	samples[ms(5000)] = 0.9  // speaker-2 span → stranger (not enrolled)
	samples[ms(9000)] = 0.5  // speaker-3 span A → lance
	samples[ms(11000)] = 0.7 // speaker-3 span B → miss (unknown)

	eng := &FakeEngine{
		Identities: map[string]string{"0.5000": "lance", "0.9000": "stranger"},
		Enrolled:   map[string]string{"lance": "titanet"},
		Diarization: Timeline{Observations: []Observation{
			{SegmentID: "a", StartMS: 0, EndMS: 2000, Label: "speaker-1", State: StateStable, Source: SourceRecording},
			{SegmentID: "b", StartMS: 2500, EndMS: 4500, Label: "speaker-1", State: StateStable, Source: SourceRecording},
			{SegmentID: "c", StartMS: 5000, EndMS: 8000, Label: "speaker-2", State: StateStable, Source: SourceRecording},
			{SegmentID: "d", StartMS: 9000, EndMS: 10500, Label: "speaker-3", State: StateStable, Source: SourceRecording},
			{SegmentID: "e", StartMS: 11000, EndMS: 12500, Label: "speaker-3", State: StateStable, Source: SourceRecording},
		}},
	}
	return eng, samples
}

func TestFinalizeResolvesEnrolledClusters(t *testing.T) {
	eng, samples := resolveFixture()
	a, err := NewAnalyzer(meetingTestConfig(), eng)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()

	tl, err := a.Finalize(context.Background(), samples)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Observation{}
	for _, obs := range tl.Observations {
		byID[obs.SegmentID] = obs
	}

	for _, id := range []string{"a", "b"} {
		obs := byID[id]
		if obs.Label != "lance" || obs.EnrollRev != "titanet" || obs.State != StateRevised {
			t.Fatalf("span %s: label=%q rev=%q state=%q, want enrolled rename", id, obs.Label, obs.EnrollRev, obs.State)
		}
	}
	if obs := byID["c"]; obs.Label != "speaker-2" || obs.EnrollRev != "" {
		t.Fatalf("unenrolled-identity cluster must keep its label: %+v", obs)
	}
	for _, id := range []string{"d", "e"} {
		if obs := byID[id]; obs.Label != "speaker-3" || obs.State == StateRevised {
			t.Fatalf("split-vote cluster must stay anonymous: %+v", obs)
		}
	}
}

// cancelAfterDiarizeEngine cancels the Finalize context as Diarize returns,
// so the enrolled-cluster resolution pass starts with ctx already canceled.
type cancelAfterDiarizeEngine struct {
	*FakeEngine
	cancel context.CancelFunc
}

func (e *cancelAfterDiarizeEngine) Diarize(ctx context.Context, samples []float32, n int) (Timeline, error) {
	tl, err := e.FakeEngine.Diarize(ctx, samples, n)
	e.cancel()
	return tl, err
}

func TestFinalizePropagatesCancellationFromResolve(t *testing.T) {
	inner, samples := resolveFixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := NewAnalyzer(meetingTestConfig(), &cancelAfterDiarizeEngine{FakeEngine: inner, cancel: cancel})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()

	if _, err := a.Finalize(ctx, samples); !errors.Is(err, context.Canceled) {
		t.Fatalf("Finalize after mid-flight cancel = %v, want context.Canceled", err)
	}
}

func TestFinalizeWithoutEnrollmentUnchanged(t *testing.T) {
	eng, samples := resolveFixture()
	eng.Enrolled = nil // no profiles seeded — the default install
	a, err := NewAnalyzer(meetingTestConfig(), eng)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()

	tl, err := a.Finalize(context.Background(), samples)
	if err != nil {
		t.Fatal(err)
	}
	for _, obs := range tl.Observations {
		if obs.EnrollRev != "" || obs.State == StateRevised {
			t.Fatalf("default install must be untouched by resolution: %+v", obs)
		}
	}
}

func TestSeedFromStoreSkipsStaleRevisions(t *testing.T) {
	store, err := OpenEnrollment(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := store.Add(ctx, "lance", "fake-embedding-rev", [][]float32{{1, 2}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(ctx, "old-friend", "previous-model", [][]float32{{3, 4}}); err != nil {
		t.Fatal(err)
	}

	eng := &FakeEngine{}
	seeded, skipped, err := SeedFromStore(eng, store)
	if err != nil {
		t.Fatal(err)
	}
	if seeded != 1 {
		t.Fatalf("seeded = %d, want 1", seeded)
	}
	if len(skipped) != 1 || skipped[0] != "old-friend" {
		t.Fatalf("skipped = %v, want [old-friend]", skipped)
	}
	if rev := eng.EnrollRevFor("lance"); rev != "fake-embedding-rev" {
		t.Fatalf("lance rev = %q", rev)
	}
	if rev := eng.EnrollRevFor("old-friend"); rev != "" {
		t.Fatalf("stale profile must not seed, rev = %q", rev)
	}
}

func TestLoadEnrollmentAbsentCreatesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "speakers")
	store, err := LoadEnrollment(dir)
	if err != nil {
		t.Fatal(err)
	}
	if store != nil {
		t.Fatal("absent store must load as nil")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("LoadEnrollment must not create directories, stat: %v", err)
	}
}
