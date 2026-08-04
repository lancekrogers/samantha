package speaker

import "testing"

func TestMergeSparseLabelsTwoBigManyTiny(t *testing.T) {
	// Two long talkers + many short false IDs (auto over-split pattern).
	tl := Timeline{Observations: []Observation{
		{StartMS: 0, EndMS: 60_000, Label: "speaker-2", State: StateStable},
		{StartMS: 60_000, EndMS: 120_000, Label: "speaker-12", State: StateStable},
		{StartMS: 5_000, EndMS: 5_400, Label: "speaker-1", State: StateStable},
		{StartMS: 70_000, EndMS: 70_300, Label: "speaker-27", State: StateStable},
		{StartMS: 80_000, EndMS: 81_500, Label: "speaker-8", State: StateStable}, // 1.5s still sparse if floor 2s and small fraction
		{StartMS: 100_000, EndMS: 160_000, Label: "speaker-12", State: StateStable},
	}}
	// Total mass ~121s; 1.5s is ~1.2% — raise MinFraction so speaker-8 merges, or use default 1% + 2s floor (1.5s merges).
	got := MergeSparseLabels(tl, MergeOpts{})
	labels := SortedSpeakerLabels(got)
	if len(labels) > 3 {
		t.Fatalf("labels = %v (count %d), want ≤3 after merge", labels, len(labels))
	}
	// Tiny speaker-1 and speaker-27 must be gone.
	for _, o := range got.Observations {
		if o.Label == "speaker-1" || o.Label == "speaker-27" {
			t.Fatalf("sparse label survived: %+v", o)
		}
	}
	// Dominant long labels should remain present.
	seen := map[string]bool{}
	for _, o := range got.Observations {
		seen[o.Label] = true
	}
	if !seen["speaker-2"] || !seen["speaker-12"] {
		t.Fatalf("keepers lost: %v", SortedSpeakerLabels(got))
	}
}

func TestMergeSparseLabelsNoopWhenAlreadyFew(t *testing.T) {
	tl := Timeline{Observations: []Observation{
		{StartMS: 0, EndMS: 30_000, Label: "speaker-1", State: StateStable},
		{StartMS: 30_000, EndMS: 60_000, Label: "speaker-2", State: StateStable},
	}}
	got := MergeSparseLabels(tl, MergeOpts{})
	if DistinctSpeakerLabels(got) != 2 {
		t.Fatalf("labels = %v", SortedSpeakerLabels(got))
	}
}

func TestMergeSparseLabelsAutoAbsorbsTinyPartner(t *testing.T) {
	// 400ms is under 2s floor and under 1% of ~60s total → auto merge absorbs.
	tl := Timeline{Observations: []Observation{
		{StartMS: 0, EndMS: 60_000, Label: "speaker-1", State: StateStable},
		{StartMS: 60_000, EndMS: 60_400, Label: "speaker-2", State: StateStable},
	}}
	merged := MergeSparseLabels(tl, MergeOpts{})
	if DistinctSpeakerLabels(merged) != 1 {
		t.Fatalf("auto merge should absorb 400ms partner: %v", SortedSpeakerLabels(merged))
	}
	// Fixed-N product path does not call merge (Analyzer NumSpeakers > 0), so
	// the original timeline still has both labels.
	if DistinctSpeakerLabels(tl) != 2 {
		t.Fatalf("unmerged timeline must keep both labels: %v", SortedSpeakerLabels(tl))
	}
}

func TestMergeSparseLabelsIgnoresNonAnonymous(t *testing.T) {
	tl := Timeline{Observations: []Observation{
		{StartMS: 0, EndMS: 50_000, Label: "speaker-1", State: StateStable},
		{StartMS: 50_000, EndMS: 50_200, Label: "Lance", State: StateStable},
	}}
	got := MergeSparseLabels(tl, MergeOpts{})
	var hasLance bool
	for _, o := range got.Observations {
		if o.Label == "Lance" {
			hasLance = true
		}
	}
	if !hasLance {
		t.Fatal("enrolled/custom labels must not be rewritten")
	}
}
