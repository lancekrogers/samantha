//go:build integration
// +build integration

// Package ownerverify measures enrollment match quality (FAR/FRR) on the
// real multi-voice meeting fixture. Separate from tests/speakerflow so it
// builds under -tags integration without the meeting package (whose
// !integration files break that suite's compile).
package ownerverify

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/speaker"
	"github.com/lancekrogers/samantha/pkg/voiceagent/audio"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

// TestOwnerVerifyFalseAcceptRate measures enrollment match quality on the
// real 2-speaker meeting fixture — the FAR/FRR record WI-648119 requires
// before speaker.live.mode=owner_verify is recommended. It enrolls the
// dominant speaker from its two longest diarized spans, then runs
// engine.Verify (read-only; no auto-registration side effects) over every
// remaining span ≥1s of both speakers.
const (
	fixtureName  = "product-marketing-meeting-90s.wav"
	fixtureRate  = 16000
	wantSpeakers = 2
)

// fixturePath resolves the shared meeting fixture (same rules as
// tests/speakerflow and scripts/fetch-meeting-fixture.sh); skips when absent.
func fixturePath(t *testing.T) string {
	t.Helper()
	var candidates []string
	if p := os.Getenv("SAMANTHA_MEETING_FIXTURE"); p != "" {
		candidates = append(candidates, p)
	}
	cacheDir := os.Getenv("SAMANTHA_FIXTURE_CACHE")
	if cacheDir == "" {
		if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
			cacheDir = filepath.Join(xdg, config.AppSlug, "fixtures", "meetings")
		} else if home, err := os.UserHomeDir(); err == nil {
			cacheDir = filepath.Join(home, ".cache", config.AppSlug, "fixtures", "meetings")
		}
	}
	if cacheDir != "" {
		candidates = append(candidates, filepath.Join(cacheDir, fixtureName))
	}
	for _, candidate := range candidates {
		if st, err := os.Stat(candidate); err == nil && st.Size() > 100_000 {
			return candidate
		}
	}
	t.Skipf("meeting fixture missing — run: just fetch-meeting-fixture (looked in %v)", candidates)
	return ""
}

func TestOwnerVerifyFalseAcceptRate(t *testing.T) {
	path := fixturePath(t)
	samples, rate, err := audio.ReadWAVFloat32(path)
	if err != nil {
		t.Fatalf("ReadWAVFloat32: %v", err)
	}
	if rate != fixtureRate {
		t.Fatalf("sample rate = %d, want %d", rate, fixtureRate)
	}

	appCfg := &config.Config{ModelsDir: config.ModelsDir()}
	appCfg.Speaker.Enabled = true
	appCfg.Speaker.Meeting.Enabled = true
	appCfg.Speaker.Meeting.NumSpeakers = wantSpeakers
	if err := config.EnsureRuntimeAssets(t.Context(), appCfg, config.AssetRequest{NeedSpeaker: true}, nil); err != nil {
		t.Fatalf("EnsureRuntimeAssets: %v", err)
	}
	cfg := speaker.FromAppConfig(appCfg)
	engine, err := speaker.NewSherpaEngine(cfg, config.ModelsDirFrom(appCfg))
	if err != nil {
		t.Fatalf("NewSherpaEngine: %v", err)
	}
	defer func() { _ = engine.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	tl, err := engine.Diarize(ctx, samples, wantSpeakers)
	if err != nil {
		t.Fatalf("Diarize: %v", err)
	}

	spanPCM := func(obs speaker.Observation) []float32 {
		start := int(obs.StartMS) * rate / 1000
		end := int(obs.EndMS) * rate / 1000
		if start < 0 || end > len(samples) || start >= end {
			return nil
		}
		return samples[start:end]
	}

	byLabel := map[string][]speaker.Observation{}
	total := map[string]int64{}
	for _, obs := range tl.Observations {
		if len(spanPCM(obs)) < rate/2 { // ≥0.5s — the live embed minimum
			continue
		}
		byLabel[obs.Label] = append(byLabel[obs.Label], obs)
		total[obs.Label] += obs.DurationMS()
	}
	if len(byLabel) < 2 {
		t.Fatalf("diarization found %d usable clusters, want 2", len(byLabel))
	}
	labels := make([]string, 0, len(byLabel))
	for l := range byLabel {
		labels = append(labels, l)
	}
	sort.Slice(labels, func(i, j int) bool { return total[labels[i]] > total[labels[j]] })
	ownerLabel, otherLabel := labels[0], labels[1]

	// 1.5s — the live path's own analysis window.
	const windowSamples = 3 * fixtureRate / 2

	ownerSpans := byLabel[ownerLabel]
	sort.Slice(ownerSpans, func(i, j int) bool { return ownerSpans[i].DurationMS() > ownerSpans[j].DurationMS() })
	if len(ownerSpans) < 4 {
		t.Fatalf("owner cluster has %d spans ≥0.5s, want ≥4 (2 enroll + ≥2 eval)", len(ownerSpans))
	}

	// Enroll from 1.5s windows of the two longest spans — matching the live
	// verification window. Whole-span embeds of 20-45s turns measured FRR
	// 0.80 against 1.5s verify windows (duration mismatch); windowed
	// enrollment is the shape the product path must use.
	const maxEnrollWindows = 8
	var enrollment [][]float32
	for _, obs := range ownerSpans[:2] {
		pcm := spanPCM(obs)
		for start := 0; start+windowSamples <= len(pcm) && len(enrollment) < maxEnrollWindows; start += windowSamples {
			emb, err := engine.Embed(ctx, pcm[start:start+windowSamples])
			if err != nil {
				t.Fatalf("Embed enroll window: %v", err)
			}
			enrollment = append(enrollment, emb)
		}
	}
	if len(enrollment) < 2 {
		t.Fatalf("only %d enroll windows", len(enrollment))
	}
	if err := engine.SeedEnrolled("owner", engine.EmbeddingRev(), enrollment); err != nil {
		t.Fatalf("SeedEnrolled: %v", err)
	}

	windows := func(spans []speaker.Observation) [][]float32 {
		var out [][]float32
		for _, obs := range spans {
			pcm := spanPCM(obs)
			for start := 0; start+windowSamples <= len(pcm); start += windowSamples {
				out = append(out, pcm[start:start+windowSamples])
			}
			if len(pcm) >= rate/2 && len(pcm) < windowSamples {
				out = append(out, pcm) // short turn: one sub-window sample
			}
		}
		return out
	}
	verifyWindows := func(wins [][]float32) (accepted, evaluated int) {
		for _, win := range wins {
			emb, err := engine.Embed(ctx, win)
			if err != nil {
				continue
			}
			ok, err := engine.Verify(ctx, "owner", emb, 0) // 0 = engine's configured threshold
			if err != nil {
				continue
			}
			evaluated++
			if ok {
				accepted++
			}
		}
		return accepted, evaluated
	}

	trueAccepts, ownerEvals := verifyWindows(windows(ownerSpans[2:]))
	falseAccepts, otherEvals := verifyWindows(windows(byLabel[otherLabel]))
	if ownerEvals < 2 || otherEvals < 3 {
		t.Fatalf("too few eval windows (owner %d, other %d) for a meaningful rate", ownerEvals, otherEvals)
	}

	far := float64(falseAccepts) / float64(otherEvals)
	frr := 1 - float64(trueAccepts)/float64(ownerEvals)
	t.Logf("owner-verify eval on %s:", fixtureName)
	t.Logf("  enrolled owner=%s from 2 spans (%.1fs, %.1fs)", ownerLabel,
		float64(ownerSpans[0].DurationMS())/1000, float64(ownerSpans[1].DurationMS())/1000)
	t.Logf("  FAR (impostor %s windows accepted as owner): %d/%d = %.2f", otherLabel, falseAccepts, otherEvals, far)
	t.Logf("  FRR (owner windows rejected):            %d/%d = %.2f", ownerEvals-trueAccepts, ownerEvals, frr)
	t.Logf("  threshold: engine default (speaker.threshold, normalized)")

	// Sanity bound only — the point of this test is the recorded numbers.
	// A FAR at coin-flip level would mean the metric (or model) is broken.
	if far > 0.5 {
		t.Fatalf("FAR %.2f > 0.50 — enrollment matching is not usable", far)
	}
}
