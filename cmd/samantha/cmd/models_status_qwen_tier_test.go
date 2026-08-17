package cmd

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	managedqwen "github.com/lancekrogers/samantha/pkg/voiceagent/qwen"
)

// --- qwenTierStatusRows / qwenTierFailClosedDetail (MDL-A3) ---

func TestQwenTierStatusRowsPackageAbsentReportsBothTiersMissing(t *testing.T) {
	native := managedqwen.NativeStatus{
		Root:     "/models/qwen3-tts",
		ModelDir: "/models/qwen3-tts/models",
		// TiersReady/TiersMissing both nil: InspectNative's early-return shape
		// for "package not installed at all".
	}

	rows := qwenTierStatusRows(native, "")
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (one per known tier)", len(rows))
	}

	byID := map[string]config.AssetStatus{}
	for _, r := range rows {
		byID[r.ID] = r
	}

	for _, tc := range []struct {
		id       string
		wantName string
	}{
		{"tts.qwen3.tier.0.6b", "Qwen3-TTS model tier 0.6b"},
		{"tts.qwen3.tier.1.7b", "Qwen3-TTS model tier 1.7b"},
	} {
		row, ok := byID[tc.id]
		if !ok {
			t.Fatalf("missing row %q, got rows %+v", tc.id, rows)
		}
		if row.Name != tc.wantName {
			t.Errorf("row %q name = %q, want %q", tc.id, row.Name, tc.wantName)
		}
		if row.Provider != managedqwen.ProviderName {
			t.Errorf("row %q provider = %q, want %q", tc.id, row.Provider, managedqwen.ProviderName)
		}
		if row.Mode != "customvoice" {
			t.Errorf("row %q mode = %q, want customvoice", tc.id, row.Mode)
		}
		if row.Kind != config.AssetKindTTS {
			t.Errorf("row %q kind = %q, want tts", tc.id, row.Kind)
		}
		if row.Installed {
			t.Errorf("row %q installed = true, want false (package absent)", tc.id)
		}
		if len(row.Missing) != 1 || row.Missing[0] != native.Root {
			t.Errorf("row %q missing = %v, want [%s] (package root)", tc.id, row.Missing, native.Root)
		}
	}

	// Only the 1.7b row carries the fail-closed detail (0.6b is the pinned
	// release's own tier, so a plain "missing" already explains it).
	if byID["tts.qwen3.tier.0.6b"].Detail != "" {
		t.Errorf("0.6b detail = %q, want empty", byID["tts.qwen3.tier.0.6b"].Detail)
	}
	wantDetail := "not in the pinned release — set qwen_tts_native_url to a multi-tier tarball"
	if got := byID["tts.qwen3.tier.1.7b"].Detail; got != wantDetail {
		t.Errorf("1.7b detail = %q, want %q", got, wantDetail)
	}
}

func TestQwenTierStatusRowsReadyTierHasNoMissingOrDetail(t *testing.T) {
	native := managedqwen.NativeStatus{
		Root:         "/models/qwen3-tts",
		ModelDir:     "/models/qwen3-tts/models",
		TiersReady:   []string{"0.6b"},
		TiersMissing: []string{"1.7b"},
	}

	rows := qwenTierStatusRows(native, "")
	byID := map[string]config.AssetStatus{}
	for _, r := range rows {
		byID[r.ID] = r
	}

	ready := byID["tts.qwen3.tier.0.6b"]
	if !ready.Installed {
		t.Error("0.6b installed = false, want true (in TiersReady)")
	}
	if len(ready.Missing) != 0 || ready.Detail != "" {
		t.Errorf("installed row must not carry Missing/Detail, got %+v", ready)
	}

	missing := byID["tts.qwen3.tier.1.7b"]
	if missing.Installed {
		t.Error("1.7b installed = true, want false (in TiersMissing)")
	}
	// The package itself is present (TiersReady is non-empty), so the
	// missing path is the tier's real flat-layout filename — qwen/native.go's
	// tierReady checks <ModelDir>/qwen3-tts-<tier>-f16.gguf, never a
	// per-tier subdirectory (no release layout has ever used one; an
	// earlier draft of this row fabricated ModelDir/<tier>/, caught by
	// adversarial review).
	wantPath := filepath.Join(native.ModelDir, "qwen3-tts-1.7b-f16.gguf")
	if len(missing.Missing) != 1 || missing.Missing[0] != wantPath {
		t.Errorf("1.7b missing = %v, want [%s]", missing.Missing, wantPath)
	}
}

func TestQwenTierFailClosedDetailOnlyFor1_7bAndPinnedRelease(t *testing.T) {
	if got := qwenTierFailClosedDetail("", "0.6b"); got != "" {
		t.Errorf("0.6b detail = %q, want empty (never fails closed)", got)
	}
	if got := qwenTierFailClosedDetail("https://example.test/custom-multi-tier.tar.gz", "1.7b"); got != "" {
		t.Errorf("custom URL 1.7b detail = %q, want empty (not the pinned release)", got)
	}
	if got := qwenTierFailClosedDetail("", "1.7b"); got == "" {
		t.Error("pinned-release 1.7b detail = \"\", want the fail-closed explanation")
	}
}

// --- models status --json integration (array shape unchanged) ---

func TestModelsStatusJSONIncludesQwenTierRows(t *testing.T) {
	cfg := &config.Config{TTSProvider: "qwen3-tts", ModelsDir: t.TempDir(), QwenTTSModelTier: "0.6b"}
	out := runStatusScoped(t, cfg, scopeFlags{tts: true}, cfg.ModelsDir, true)

	var statuses []config.AssetStatus
	if err := json.Unmarshal([]byte(out), &statuses); err != nil {
		t.Fatalf("--json output is not a valid []config.AssetStatus: %v\n%s", err, out)
	}

	var sawCoarse, sawTier06, sawTier17 bool
	for _, s := range statuses {
		switch s.ID {
		case "tts.qwen3.native":
			sawCoarse = true
		case "tts.qwen3.tier.0.6b":
			sawTier06 = true
		case "tts.qwen3.tier.1.7b":
			sawTier17 = true
			if s.Detail == "" {
				t.Error("1.7b tier row on a fresh install should carry the fail-closed detail")
			}
		}
	}
	if !sawCoarse {
		t.Error("existing coarse tts.qwen3.native row was dropped, want it kept (additive only)")
	}
	if !sawTier06 || !sawTier17 {
		t.Errorf("missing per-tier rows, got statuses %+v", statuses)
	}
}
