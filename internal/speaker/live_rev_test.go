package speaker

import (
	"context"
	"testing"
	"time"
)

func TestLiveAdapterStatsCarryEnrollRev(t *testing.T) {
	eng := &FakeEngine{
		Identities: map[string]string{"0.5000": "lance"},
		Enrolled:   map[string]string{"lance": "titanet"},
	}
	analyzer, err := NewAnalyzer(liveTestConfig(), eng)
	if err != nil {
		t.Fatal(err)
	}
	adapter := NewLiveAdapter(context.Background(), analyzer, 4)
	defer func() { _ = adapter.Close() }()

	samples := make([]float32, 100)
	samples[0] = 0.5
	if err := adapter.Submit(context.Background(), Segment{ID: "s1", End: time.Second, Samples: samples}); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(2 * time.Second)
	for {
		st := adapter.Stats()
		if st.Processed >= 1 {
			if st.LastLabel != "lance" || st.LastEnrollRev != "titanet" {
				t.Fatalf("stats = label %q rev %q, want enrolled pair", st.LastLabel, st.LastEnrollRev)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("adapter never processed, stats %+v", st)
		case <-time.After(5 * time.Millisecond):
		}
	}

	if err := adapter.Reset(); err != nil {
		t.Fatal(err)
	}
	if st := adapter.Stats(); st.LastLabel != "" || st.LastEnrollRev != "" {
		t.Fatalf("reset must clear label and rev, stats %+v", st)
	}
}
