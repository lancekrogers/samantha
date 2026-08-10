package speaker

import (
	"context"
	"sort"
	"strings"

	"github.com/lancekrogers/samantha/internal/audio"
)

// maxResolveSpansPerCluster bounds per-cluster embedding cost when mapping
// diarized clusters onto enrolled names at finalize.
const maxResolveSpansPerCluster = 3

// resolveEnrolledClusters renames anonymous diarized clusters (speaker-N) to
// enrolled names when a strict majority of a cluster's longest spans
// identify as one enrolled profile. Additive: clusters without a majority
// keep their anonymous label, and spans that already carry a non-anonymous
// label are never touched. Per-span model failures are best-effort skips;
// context cancellation is propagated so Finalize never reports success on a
// canceled pass. Identify may auto-register unmatched meeting voices into
// the engine's in-memory manager; that state is scoped to this engine
// instance and discarded with it.
func (a *Analyzer) resolveEnrolledClusters(ctx context.Context, eng Engine, res EnrolledResolver, samples []float32, tl Timeline) (Timeline, error) {
	clusters := map[string][]int{}
	for i, obs := range tl.Observations {
		if strings.HasPrefix(strings.ToLower(obs.Label), LabelSpeakerPrefix) {
			clusters[obs.Label] = append(clusters[obs.Label], i)
		}
	}
	if len(clusters) == 0 {
		return tl, nil
	}

	a.engineMu.Lock()
	defer a.engineMu.Unlock()
	a.stateMu.Lock()
	if a.closed {
		a.stateMu.Unlock()
		return tl, nil
	}
	a.stateMu.Unlock()

	for _, idxs := range clusters {
		if err := ctx.Err(); err != nil {
			return tl, err
		}
		votes := map[string]int{}
		sampled := 0
		for _, i := range longestSpans(tl.Observations, idxs, maxResolveSpansPerCluster) {
			span := spanSamples(samples, tl.Observations[i])
			if len(span) < minLiveEmbedSamples {
				continue
			}
			emb, err := eng.Embed(ctx, span)
			if err != nil {
				if cerr := ctx.Err(); cerr != nil {
					return tl, cerr
				}
				continue
			}
			name, _, err := eng.Identify(ctx, emb)
			if err != nil {
				if cerr := ctx.Err(); cerr != nil {
					return tl, cerr
				}
				continue
			}
			sampled++
			if res.EnrollRevFor(name) != "" {
				votes[name]++
			}
		}
		for name, n := range votes {
			if n*2 > sampled {
				rev := res.EnrollRevFor(name)
				for _, i := range idxs {
					tl.Observations[i].Label = name
					tl.Observations[i].EnrollRev = rev
					tl.Observations[i].State = StateRevised
				}
				break
			}
		}
	}
	return tl, nil
}

// longestSpans returns up to max observation indices sorted by duration,
// longest first — the spans most likely to embed cleanly.
func longestSpans(obs []Observation, idxs []int, max int) []int {
	sorted := append([]int(nil), idxs...)
	sort.Slice(sorted, func(a, b int) bool {
		return obs[sorted[a]].DurationMS() > obs[sorted[b]].DurationMS()
	})
	if len(sorted) > max {
		sorted = sorted[:max]
	}
	return sorted
}

func spanSamples(samples []float32, obs Observation) []float32 {
	start := int(obs.StartMS) * audio.SampleRate / 1000
	end := int(obs.EndMS) * audio.SampleRate / 1000
	if start < 0 {
		start = 0
	}
	if end > len(samples) {
		end = len(samples)
	}
	if start >= end {
		return nil
	}
	return samples[start:end]
}
