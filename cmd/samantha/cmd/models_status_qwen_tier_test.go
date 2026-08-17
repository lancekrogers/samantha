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

// --- M6: the tier rows are reported for any TTS-covering request ---

// modelsStatusFixture decodes a captured `models status --json --all` payload
// and proves the fixture is byte-for-byte what []config.AssetStatus encodes —
// a field added to the type without re-capturing fails here rather than in the
// Mac app.
func modelsStatusFixture(t *testing.T, name string) []config.AssetStatus {
	t.Helper()
	raw := readFixture(t, name)

	var statuses []config.AssetStatus
	if err := json.Unmarshal([]byte(raw), &statuses); err != nil {
		t.Fatalf("fixture %s is not a []config.AssetStatus: %v", name, err)
	}
	encoded, err := json.MarshalIndent(statuses, "", "  ")
	if err != nil {
		t.Fatalf("re-encoding %s: %v", name, err)
	}
	if string(encoded)+"\n" != raw {
		t.Errorf("fixture %s does not round-trip — re-run testdata/capture-models-fixtures.sh\n--- want ---\n%s\n--- got ---\n%s",
			name, raw, string(encoded)+"\n")
	}
	return statuses
}

func statusByID(statuses []config.AssetStatus) map[string]config.AssetStatus {
	byID := make(map[string]config.AssetStatus, len(statuses))
	for _, s := range statuses {
		byID[s.ID] = s
	}
	return byID
}

// The acceptance run's own configuration: tts_provider kokoro, nothing
// installed. It reported five rows and no tier rows at all, so the Mac app had
// no way to show tier install state (defect M6). The tier rows must be there —
// missing, but there — because the app's tier picker runs before the user
// switches provider.
func TestModelsStatusKokoroFixtureStillCarriesTierRows(t *testing.T) {
	byID := statusByID(modelsStatusFixture(t, "models-status-kokoro-no-tiers.json"))

	if _, ok := byID["tts.qwen3.native"]; ok {
		t.Error("coarse tts.qwen3.native row present for a kokoro config; it describes an asset this config does not need")
	}
	for _, tier := range []string{"0.6b", "1.7b"} {
		row, ok := byID[qwenTierIDPrefix+tier]
		if !ok {
			t.Fatalf("no %s%s row — this is defect M6", qwenTierIDPrefix, tier)
		}
		if row.Installed {
			t.Errorf("%s installed = true, want false (nothing is installed in this fixture)", row.ID)
		}
		if len(row.Missing) == 0 {
			t.Errorf("%s carries no missing path", row.ID)
		}
		if row.Kind != config.AssetKindTTS || row.Provider != managedqwen.ProviderName || row.Mode != "customvoice" {
			t.Errorf("%s = %+v, want kind tts / provider %s / mode customvoice", row.ID, row, managedqwen.ProviderName)
		}
	}
	if got := byID[qwenTierIDPrefix+"1.7b"].Detail; got == "" {
		t.Error("1.7b row lost its fail-closed detail")
	}

	// The fixture on its own only proves what the binary emitted the day it was
	// captured. Reverting the fix and keeping the file would leave this test
	// green, so the same configuration is run through the command here and the
	// row ids compared. Found by adversarial review.
	cfg := &config.Config{
		TTSProvider: "kokoro", ModelsDir: t.TempDir(),
		VADEnabled: true, STTProvider: "sherpa",
		Speaker: config.SpeakerConfig{Enabled: true, Meeting: config.SpeakerMeetingConfig{Enabled: true}},
	}
	var live []config.AssetStatus
	if err := json.Unmarshal([]byte(runStatusScoped(t, cfg, scopeFlags{all: true}, cfg.ModelsDir, true)), &live); err != nil {
		t.Fatalf("live --json output: %v", err)
	}
	liveByID := statusByID(live)
	for id := range byID {
		if _, ok := liveByID[id]; !ok {
			t.Errorf("row %q is in the fixture but not in today's output — re-run testdata/capture-models-fixtures.sh", id)
		}
	}
	for id := range liveByID {
		if _, ok := byID[id]; !ok {
			t.Errorf("row %q is in today's output but not in the fixture — re-run testdata/capture-models-fixtures.sh", id)
		}
	}
}

// The other end: a real native package carrying both tiers. installed comes
// from InspectNative's TiersReady, and the coarse row is back because this
// config does select managed qwen.
func TestModelsStatusQwenFixtureReportsInstalledTiers(t *testing.T) {
	byID := statusByID(modelsStatusFixture(t, "models-status-qwen-both-tiers.json"))

	coarse, ok := byID["tts.qwen3.native"]
	if !ok || !coarse.Installed {
		t.Fatalf("coarse row = %+v, want it present and installed", coarse)
	}
	for _, tier := range []string{"0.6b", "1.7b"} {
		row, ok := byID[qwenTierIDPrefix+tier]
		if !ok {
			t.Fatalf("no %s%s row", qwenTierIDPrefix, tier)
		}
		if !row.Installed {
			t.Errorf("%s installed = false, want true (the package holds this tier)", row.ID)
		}
		if len(row.Missing) != 0 || row.Detail != "" {
			t.Errorf("%s = %+v, want no missing paths and no detail once installed", row.ID, row)
		}
	}
}

// The gate is the request's TTS scope, not the configured provider.
func TestModelsStatusTierRowsFollowTheTTSScope(t *testing.T) {
	tests := []struct {
		name       string
		provider   string
		scope      scopeFlags
		wantTiers  bool
		wantCoarse bool
	}{
		{name: "stt only asks for nothing qwen", provider: "kokoro", scope: scopeFlags{stt: true}},
		{name: "vad only asks for nothing qwen", provider: "qwen3-tts", scope: scopeFlags{vad: true}},
		{name: "kokoro with --all still reports tiers", provider: "kokoro", scope: scopeFlags{all: true}, wantTiers: true},
		{name: "kokoro with --tts still reports tiers", provider: "kokoro", scope: scopeFlags{tts: true}, wantTiers: true},
		// NeedTTS is ScopedAssetRequest's own answer (scope.TTS && ManagedTTS):
		// with no TTS provider there is no TTS request at all, so there is
		// nothing for the tier rows to ride on either.
		{name: "no TTS provider means no TTS request", provider: "", scope: scopeFlags{all: true}},
		{name: "qwen reports tiers and the coarse row", provider: "qwen3-tts", scope: scopeFlags{tts: true}, wantTiers: true, wantCoarse: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{TTSProvider: tt.provider, ModelsDir: t.TempDir()}
			out := runStatusScoped(t, cfg, tt.scope, cfg.ModelsDir, true)

			var statuses []config.AssetStatus
			if err := json.Unmarshal([]byte(out), &statuses); err != nil {
				t.Fatalf("--json output is not a []config.AssetStatus: %v\n%s", err, out)
			}
			byID := statusByID(statuses)
			_, saw06 := byID[qwenTierIDPrefix+"0.6b"]
			_, saw17 := byID[qwenTierIDPrefix+"1.7b"]
			if saw06 != tt.wantTiers || saw17 != tt.wantTiers {
				t.Errorf("tier rows present = %v/%v, want %v", saw06, saw17, tt.wantTiers)
			}
			if _, sawCoarse := byID["tts.qwen3.native"]; sawCoarse != tt.wantCoarse {
				t.Errorf("coarse row present = %v, want %v", sawCoarse, tt.wantCoarse)
			}
		})
	}
}

// A bare `models ensure` installs the tier the config points at, so it would do
// nothing for the other tier — and nothing at all for a kokoro user. The human
// line has to name the flag that does work, and it must not read as a chore for
// a tier this configuration would never load.
func TestMissingHintNamesTheTierFlag(t *testing.T) {
	tests := []struct {
		name         string
		row          config.AssetStatus
		requiredTier string
		want         string
	}{
		{
			name: "a detail always wins",
			row:  config.AssetStatus{ID: qwenTierIDPrefix + "1.7b", Detail: "not in the pinned release"},
			want: "not in the pinned release",
		},
		{
			name:         "the tier this config would load is missing",
			row:          config.AssetStatus{ID: qwenTierIDPrefix + "0.6b"},
			requiredTier: "0.6b",
			want:         "missing — run 'samantha models ensure --tts --tier 0.6b'",
		},
		{
			name:         "a tier this config would not load is offered, not demanded",
			row:          config.AssetStatus{ID: qwenTierIDPrefix + "1.7b"},
			requiredTier: "0.6b",
			want:         "not installed — available with 'samantha models ensure --tts --tier 1.7b'",
		},
		{
			name: "no qwen tier is required at all",
			row:  config.AssetStatus{ID: qwenTierIDPrefix + "0.6b"},
			want: "not installed — available with 'samantha models ensure --tts --tier 0.6b'",
		},
		{
			name: "any other row keeps the plain hint",
			row:  config.AssetStatus{ID: "vad.silero.v1"},
			want: "missing — run 'samantha models ensure'",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := missingHint(tt.row, tt.requiredTier); got != tt.want {
				t.Errorf("missingHint(%s) = %q, want %q", tt.row.ID, got, tt.want)
			}
		})
	}
}

// The count a qwen user reads has to be reachable. Only the tier the agent
// would load counts as missing; the other one cannot be installed from the
// pinned release at all, so counting it meant "N missing" could never fall to
// zero. Found by adversarial review.
func TestModelsStatusCountsOnlyTheTierTheConfigWouldLoad(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		want     string
	}{
		{
			name:     "a qwen user is asked for one tier, and offered the other",
			provider: "qwen3-tts",
			want:     "7 asset(s), 6 missing (plus 1 optional Qwen3-TTS tier(s) not installed).",
		},
		{
			name:     "a kokoro user is asked for neither",
			provider: "kokoro",
			want:     "7 asset(s), 5 missing (plus 2 optional Qwen3-TTS tier(s) not installed).",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				TTSProvider: tt.provider, ModelsDir: t.TempDir(), QwenTTSModelTier: "0.6b",
				VADEnabled: true, STTProvider: "sherpa",
				Speaker: config.SpeakerConfig{Enabled: true, Meeting: config.SpeakerMeetingConfig{Enabled: true}},
			}
			out := runStatusScoped(t, cfg, scopeFlags{all: true}, cfg.ModelsDir, false)
			if !contains(out, tt.want) {
				t.Errorf("summary line missing %q:\n%s", tt.want, out)
			}
		})
	}
}
