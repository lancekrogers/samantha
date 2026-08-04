package speaker

import (
	"sort"
	"strings"
)

// MergeOpts controls post-diarization cleanup of over-split clusters.
// Zero values use product defaults tuned for long single-channel meetings.
type MergeOpts struct {
	// MinDurationMS is the floor for a label's total speech mass. Labels
	// below this are merged into a larger neighbor (default 2000 ms).
	MinDurationMS int64
	// MinFraction of total speech mass; labels below both floors are sparse.
	// Default 0.01 (1%).
	MinFraction float64
}

func (o MergeOpts) withDefaults() MergeOpts {
	if o.MinDurationMS <= 0 {
		o.MinDurationMS = 2000
	}
	if o.MinFraction <= 0 {
		o.MinFraction = 0.01
	}
	return o
}

// MergeSparseLabels reassigns short/low-mass speaker-N labels onto a dominant
// neighbor so auto clustering does not invent a crowd of stable IDs. Enrolled
// or non-speaker-* labels are left alone. Unknown is never used as a merge
// target. The returned timeline preserves observation order and segment bounds.
func MergeSparseLabels(tl Timeline, opts MergeOpts) Timeline {
	opts = opts.withDefaults()
	out := tl.Clone()
	if len(out.Observations) == 0 {
		return out
	}

	// Total mass per label (speaker-* only).
	mass := map[string]int64{}
	var total int64
	for _, o := range out.Observations {
		if !isAnonymousSpeaker(o.Label) {
			continue
		}
		d := o.DurationMS()
		mass[o.Label] += d
		total += d
	}
	if total <= 0 || len(mass) <= 1 {
		return out
	}

	// Labels that survive as merge targets: enough mass by duration or fraction.
	keepers := map[string]bool{}
	for label, m := range mass {
		frac := float64(m) / float64(total)
		if m >= opts.MinDurationMS || frac >= opts.MinFraction {
			keepers[label] = true
		}
	}
	// Always keep the single heaviest label so we never wipe all targets.
	if len(keepers) == 0 {
		var best string
		var bestM int64
		for label, m := range mass {
			if m > bestM {
				best, bestM = label, m
			}
		}
		if best != "" {
			keepers[best] = true
		}
	}

	// Midpoints of keeper segments for nearest-neighbor assignment.
	type span struct {
		label string
		mid   int64
	}
	var keeperSpans []span
	for _, o := range out.Observations {
		if !keepers[o.Label] {
			continue
		}
		keeperSpans = append(keeperSpans, span{
			label: o.Label,
			mid:   (o.StartMS + o.EndMS) / 2,
		})
	}
	if len(keeperSpans) == 0 {
		return out
	}

	// Heaviest keeper as global fallback.
	var topLabel string
	var topMass int64
	for label := range keepers {
		if mass[label] > topMass {
			topLabel, topMass = label, mass[label]
		}
	}

	for i := range out.Observations {
		lab := out.Observations[i].Label
		if !isAnonymousSpeaker(lab) || keepers[lab] {
			continue
		}
		mid := (out.Observations[i].StartMS + out.Observations[i].EndMS) / 2
		best := topLabel
		var bestDist int64 = -1
		for _, k := range keeperSpans {
			d := mid - k.mid
			if d < 0 {
				d = -d
			}
			if bestDist < 0 || d < bestDist {
				bestDist, best = d, k.label
			}
		}
		out.Observations[i].Label = best
	}
	return out
}

func isAnonymousSpeaker(label string) bool {
	return strings.HasPrefix(label, LabelSpeakerPrefix) && label != LabelUnknown
}

// DistinctSpeakerLabels counts unique anonymous speaker-N labels (and any other
// non-empty non-unknown labels) on a timeline.
func DistinctSpeakerLabels(tl Timeline) int {
	seen := map[string]struct{}{}
	for _, o := range tl.Observations {
		lab := strings.TrimSpace(o.Label)
		if lab == "" || lab == LabelUnknown {
			continue
		}
		seen[lab] = struct{}{}
	}
	return len(seen)
}

// SortedSpeakerLabels returns distinct labels sorted for stable tests.
func SortedSpeakerLabels(tl Timeline) []string {
	seen := map[string]struct{}{}
	for _, o := range tl.Observations {
		lab := strings.TrimSpace(o.Label)
		if lab == "" || lab == LabelUnknown {
			continue
		}
		seen[lab] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for lab := range seen {
		out = append(out, lab)
	}
	sort.Strings(out)
	return out
}
