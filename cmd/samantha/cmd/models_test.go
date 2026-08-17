package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/persona"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

// fullCfg needs every managed asset kind: sherpa STT, Kokoro TTS, and VAD.
func fullCfg() *config.Config {
	return &config.Config{STTProvider: "sherpa", WhisperModel: "base.en", TTSProvider: "kokoro", VADEnabled: true}
}

func runStatus(t *testing.T, cfg *config.Config, modelsDir string, asJSON bool) string {
	t.Helper()
	return runStatusScoped(t, cfg, scopeFlags{}, modelsDir, asJSON)
}

func runStatusScoped(t *testing.T, cfg *config.Config, scope scopeFlags, modelsDir string, asJSON bool) string {
	t.Helper()
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := runModelsStatus(cmd, cfg, modelsDir, scope.request(cfg), asJSON); err != nil {
		t.Fatalf("runModelsStatus() error = %v", err)
	}
	return buf.String()
}

func TestModelsStatusScopedUnsupportedModelErrors(t *testing.T) {
	cfg := &config.Config{STTProvider: "whispercpp", WhisperCPPModel: "bogus"}
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})

	err := runModelsStatus(cmd, cfg, t.TempDir(), scopeFlags{stt: true}.request(cfg), false)
	if err == nil || !contains(err.Error(), "bogus") {
		t.Fatalf("scoped status error = %v, want it to name the unsupported model", err)
	}
}

func TestModelsEnsureWrapsEnsureError(t *testing.T) {
	cfg := fullCfg()
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	fail := func(ctx context.Context, cfg *config.Config, req config.AssetRequest, onProgress func(string, float64)) error {
		return errors.New("boom")
	}

	err := runModelsEnsure(cmd, cfg, scopeFlags{tts: true}.request(cfg), fail)
	if err == nil || !contains(err.Error(), "models ensure: boom") {
		t.Fatalf("ensure error = %v, want it wrapped with 'models ensure:'", err)
	}
}

func TestModelsScopeRequests(t *testing.T) {
	full := fullCfg()
	bare := &config.Config{STTProvider: "none", TTSProvider: "none", VADEnabled: false}
	cases := []struct {
		name  string
		scope scopeFlags
		cfg   *config.Config
		want  config.AssetRequest
	}{
		{"no flags keeps default", scopeFlags{}, full, config.DefaultAssetRequest(full)},
		{"all keeps default", scopeFlags{all: true}, full, config.DefaultAssetRequest(full)},
		{"tts only", scopeFlags{tts: true}, full, config.AssetRequest{NeedTTS: true}},
		{"stt only", scopeFlags{stt: true}, full, config.AssetRequest{NeedSTT: true}},
		{"vad only", scopeFlags{vad: true}, full, config.AssetRequest{NeedVAD: true}},
		{"tts and vad union", scopeFlags{tts: true, vad: true}, full, config.AssetRequest{NeedTTS: true, NeedVAD: true}},
		{"scope never adds assets the config does not need", scopeFlags{tts: true, stt: true, vad: true}, bare, config.AssetRequest{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.scope.request(tc.cfg); got != tc.want {
				t.Errorf("request() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestModelsScopeRequestsEnabledSpeakerAssets(t *testing.T) {
	cfg := &config.Config{}
	cfg.Speaker.Enabled = true
	cfg.Speaker.Meeting.Enabled = true
	if got := (scopeFlags{speaker: true}).request(cfg); got != (config.AssetRequest{NeedSpeaker: true}) {
		t.Fatalf("speaker scope = %+v", got)
	}
}

func TestModelsStatusScoped(t *testing.T) {
	cases := []struct {
		name    string
		scope   scopeFlags
		want    []string
		exclude []string
	}{
		// A TTS-covering scope also lists the two Qwen3-TTS tier rows (M6): a
		// front end reads tier install state from `models status`. This config
		// runs kokoro, so those two are offers, not gaps — they are listed and
		// counted separately, and the missing count stays what it was.
		{
			name:    "tts excludes stt and vad",
			scope:   scopeFlags{tts: true},
			want:    []string{"kokoro-tts", "3 asset(s), 1 missing (plus 2 optional Qwen3-TTS tier(s) not installed)"},
			exclude: []string{"silero_vad.onnx", "whisper-base.en"},
		},
		{
			name:    "tts and vad union",
			scope:   scopeFlags{tts: true, vad: true},
			want:    []string{"kokoro-tts", "silero_vad.onnx", "4 asset(s), 2 missing (plus 2 optional Qwen3-TTS tier(s) not installed)"},
			exclude: []string{"whisper-base.en"},
		},
		{
			name:  "no flags includes everything",
			scope: scopeFlags{},
			want:  []string{"kokoro-tts", "silero_vad.onnx", "whisper-base.en", "5 asset(s), 3 missing (plus 2 optional Qwen3-TTS tier(s) not installed)"},
		},
		{
			name:    "stt only leaves the tier rows out entirely",
			scope:   scopeFlags{stt: true},
			want:    []string{"whisper-base.en", "1 asset(s), 1 missing"},
			exclude: []string{"Qwen3-TTS model tier", "kokoro-tts"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := runStatusScoped(t, fullCfg(), tc.scope, t.TempDir(), false)
			for _, want := range tc.want {
				if !contains(out, want) {
					t.Errorf("status output missing %q:\n%s", want, out)
				}
			}
			for _, excl := range tc.exclude {
				if contains(out, excl) {
					t.Errorf("status output should not include %q:\n%s", excl, out)
				}
			}
		})
	}
}

func TestModelsEnsureScoped(t *testing.T) {
	full := fullCfg()
	cases := []struct {
		name      string
		scope     scopeFlags
		wantReq   config.AssetRequest
		wantKinds map[config.AssetKind]int
	}{
		{
			name:      "tts only ensures only tts assets",
			scope:     scopeFlags{tts: true},
			wantReq:   config.AssetRequest{NeedTTS: true},
			wantKinds: map[config.AssetKind]int{config.AssetKindTTS: 1},
		},
		{
			name:    "stt and vad union",
			scope:   scopeFlags{stt: true, vad: true},
			wantReq: config.AssetRequest{NeedSTT: true, NeedVAD: true},
			wantKinds: map[config.AssetKind]int{
				config.AssetKindSTT: 1,
				config.AssetKindVAD: 1,
			},
		},
		{
			name:    "no flags ensures the default request",
			scope:   scopeFlags{},
			wantReq: config.DefaultAssetRequest(full),
			wantKinds: map[config.AssetKind]int{
				config.AssetKindSTT: 1,
				config.AssetKindTTS: 1,
				config.AssetKindVAD: 1,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			cmd.SetOut(&bytes.Buffer{})
			var gotReq config.AssetRequest
			fake := func(ctx context.Context, cfg *config.Config, req config.AssetRequest, onProgress func(string, float64)) error {
				gotReq = req
				return nil
			}

			if err := runModelsEnsure(cmd, full, tc.scope.request(full), fake); err != nil {
				t.Fatalf("runModelsEnsure() error = %v", err)
			}
			if gotReq != tc.wantReq {
				t.Fatalf("ensure request = %+v, want %+v", gotReq, tc.wantReq)
			}

			manifest, err := config.ManifestFor(full, gotReq)
			if err != nil {
				t.Fatalf("ManifestFor() error = %v", err)
			}
			kinds := map[config.AssetKind]int{}
			for _, a := range manifest.Assets {
				kinds[a.Kind]++
			}
			if len(kinds) != len(tc.wantKinds) {
				t.Fatalf("ensured kinds = %v, want %v", kinds, tc.wantKinds)
			}
			for kind, n := range tc.wantKinds {
				if kinds[kind] != n {
					t.Errorf("ensured kinds[%s] = %d, want %d", kind, kinds[kind], n)
				}
			}
		})
	}
}

func TestModelsCommandsRegisterScopeFlags(t *testing.T) {
	for _, cmd := range []*cobra.Command{modelsStatusCmd, modelsEnsureCmd} {
		for _, name := range []string{"tts", "stt", "vad", "all"} {
			if cmd.Flags().Lookup(name) == nil {
				t.Errorf("%s command missing --%s flag", cmd.Use, name)
			}
		}
	}
}

func TestModelsStatusListsMissingAssets(t *testing.T) {
	dir := t.TempDir()

	out := runStatus(t, fullCfg(), dir, false)
	for _, want := range []string{"silero_vad.onnx", "kokoro-tts", "whisper-base.en", "missing", "5 asset(s), 3 missing (plus 2 optional Qwen3-TTS tier(s) not installed)"} {
		if !contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}
}

func TestModelsStatusJSONIsMachineReadable(t *testing.T) {
	cfg := &config.Config{STTProvider: "whispercpp", WhisperCPPModel: "base.en"}
	dir := t.TempDir()

	out := runStatus(t, cfg, dir, true)
	var statuses []config.AssetStatus
	if err := json.Unmarshal([]byte(out), &statuses); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\n%s", err, out)
	}
	if len(statuses) != 1 || statuses[0].Provider != "whispercpp" || statuses[0].Installed {
		t.Fatalf("json statuses = %+v, want 1 missing whispercpp asset", statuses)
	}
}

func TestModelsStatusReportsManagedQwenPresetVoices(t *testing.T) {
	cfg := &config.Config{TTSProvider: "qwen3-tts", ModelsDir: t.TempDir(), QwenTTSModelTier: "0.6b"}
	out := runStatusScoped(t, cfg, scopeFlags{tts: true}, cfg.ModelsDir, false)
	for _, want := range []string{"Qwen3-TTS native package", "qwen3-tts/customvoice", "missing"} {
		if !contains(out, want) {
			t.Errorf("native Qwen status missing %q:\n%s", want, out)
		}
	}
}

func TestModelsStatusReportsInstalled(t *testing.T) {
	cfg := &config.Config{STTProvider: "sherpa", WhisperModel: "base.en", TTSProvider: "none", VADEnabled: false}
	dir := t.TempDir()
	manifest, err := config.ManifestFor(cfg, config.DefaultAssetRequest(cfg))
	if err != nil {
		t.Fatalf("ManifestFor() error = %v", err)
	}
	asset := manifest.Assets[0]
	hashes := map[string]string{}
	for _, rel := range asset.CheckFiles {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte("x"))
		hashes[rel] = fmt.Sprintf("%x", sum)
	}
	marker := fmt.Sprintf(`{
  "id": %q,
  "url": %q,
  "sha256": %q,
  "check_hashes": {
    %q: %q,
    %q: %q
  }
}
`, asset.ID, asset.Archive.URL, asset.Archive.SHA256, asset.CheckFiles[0], hashes[asset.CheckFiles[0]], asset.CheckFiles[1], hashes[asset.CheckFiles[1]])
	if err := os.WriteFile(filepath.Join(dir, ".samantha-asset-"+asset.ID+".json"), []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}

	out := runStatus(t, cfg, dir, false)
	// The asset line should say "installed"; the missing-state suffix must be
	// absent, and the summary should report zero missing.
	if !contains(out, "installed") || contains(out, "run 'samantha models ensure'") {
		t.Errorf("status should report installed and no missing-state line:\n%s", out)
	}
	if !contains(out, "1 asset(s), 0 missing") {
		t.Errorf("status summary should report 0 missing:\n%s", out)
	}
}

func TestModelsEnsureReportsAllPresentWhenNothingNeeded(t *testing.T) {
	// A config that requires no managed assets (no sherpa/whispercpp STT, no
	// Kokoro TTS, VAD off) must report all-present and download nothing.
	cfg := &config.Config{STTProvider: "none", TTSProvider: "none", VADEnabled: false}

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := runModelsEnsure(cmd, cfg, scopeFlags{}.request(cfg), config.EnsureRuntimeAssets); err != nil {
		t.Fatalf("runModelsEnsure() error = %v", err)
	}
	if !contains(buf.String(), "already present") {
		t.Errorf("ensure with nothing to do should report all present:\n%s", buf.String())
	}
}

// stubPersonas swaps the persona lister for the duration of t so no test ever
// reads the real install root, and so a test can pin the exact persona set the
// required-asset computation sees.
func stubPersonas(t *testing.T, profiles []*persona.Profile, err error) {
	t.Helper()
	dir := t.TempDir()
	for _, p := range profiles {
		if p == nil {
			continue
		}
		if mkErr := os.MkdirAll(filepath.Join(dir, p.ID), 0o755); mkErr != nil {
			t.Fatal(mkErr)
		}
	}
	stubPersonaDir(t, dir)
	original := personaProfilesFn
	personaProfilesFn = func() ([]*persona.Profile, error) { return profiles, err }
	t.Cleanup(func() { personaProfilesFn = original })
}

// stubPersonaDir points the personas-directory lookup at dir for the duration
// of t.
func stubPersonaDir(t *testing.T, dir string) {
	t.Helper()
	original := personaDirFn
	personaDirFn = func() string { return dir }
	t.Cleanup(func() { personaDirFn = original })
}

// qwenPersonaProfile is a profile that speaks through the native Qwen3-TTS
// package at tier, whatever the app-level provider is.
func qwenPersonaProfile(id, tier string) *persona.Profile {
	return &persona.Profile{
		Schema:      persona.Schema,
		ID:          id,
		DisplayName: id,
		TTS:         persona.TTS{Provider: "qwen3-tts", Tier: tier},
		Prompts:     persona.PromptRefs{Persona: id},
	}
}

// runClean runs a clean the way a human at a terminal does: no plan file, both
// streams are a TTY, and the confirmation is answered "y".
func runClean(t *testing.T, cfg *config.Config, modelsDir string, unused, dryRun, yes, asJSON bool) (string, error) {
	t.Helper()
	stubTTY(t, true)
	return runCleanOpts(t, cfg, modelsDir, cleanOptions{Unused: unused, DryRun: dryRun, Yes: yes, JSON: asJSON}, "y\n")
}

// runCleanOpts runs one clean with explicit flags and stdin contents.
func runCleanOpts(t *testing.T, cfg *config.Config, modelsDir string, opts cleanOptions, stdin string) (string, error) {
	t.Helper()
	if personaProfilesFn == nil {
		t.Fatal("persona lister is not stubbed")
	}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetIn(strings.NewReader(stdin))
	err := runModelsClean(cmd, cfg, modelsDir, opts)
	return buf.String(), err
}

// stubTTY forces both interactive checks for the duration of t.
func stubTTY(t *testing.T, tty bool) {
	t.Helper()
	originalOut, originalIn := stdoutIsTerminalFn, stdinIsTerminalFn
	stdoutIsTerminalFn = func() bool { return tty }
	stdinIsTerminalFn = func() bool { return tty }
	t.Cleanup(func() {
		stdoutIsTerminalFn, stdinIsTerminalFn = originalOut, originalIn
	})
}

// cleanFixtureDir lays down one required asset, one leftover, and returns the
// models dir plus the leftover's path.
func cleanFixtureDir(t *testing.T) (dir, stale string) {
	t.Helper()
	dir = t.TempDir()
	stale = filepath.Join(dir, "stale.bin")
	for _, path := range []string{filepath.Join(dir, "silero_vad.onnx"), stale} {
		if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir, stale
}

// currentPlanID is the plan id clean would compute for dir right now.
func currentPlanID(t *testing.T, cfg *config.Config, dir string) string {
	t.Helper()
	out, err := runClean(t, cfg, dir, true, true, false, true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	var plan config.CleanPlan
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("dry run json: %v", err)
	}
	return plan.PlanID
}

func TestModelsCleanFlagValidation(t *testing.T) {
	stubPersonas(t, nil, nil)
	cases := []struct {
		name    string
		unused  bool
		dryRun  bool
		yes     bool
		wantErr string
	}{
		{"bare clean requires --unused", false, false, false, "--unused is required"},
		{"bare clean with --dry-run still requires --unused", false, true, false, "--unused is required"},
		{"clean requires an explicit mode", true, false, false, "choose exactly one"},
		{"clean rejects two explicit modes", true, true, true, "choose exactly one"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runClean(t, fullCfg(), t.TempDir(), tc.unused, tc.dryRun, tc.yes, false)
			if err == nil || !contains(err.Error(), tc.wantErr) {
				t.Fatalf("clean error = %v, want it to mention %q", err, tc.wantErr)
			}
			if out != "" {
				t.Errorf("rejected clean should print nothing, got:\n%s", out)
			}
		})
	}
}

func TestModelsCleanDryRunReportsOnlyExtras(t *testing.T) {
	stubPersonas(t, nil, nil)
	// VAD-only config: silero_vad.onnx is required, everything else is extra.
	cfg := &config.Config{STTProvider: "none", TTSProvider: "none", VADEnabled: true}
	dir := t.TempDir()
	for _, name := range []string{"silero_vad.onnx", "stale.bin"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out, err := runClean(t, cfg, dir, true, true, false, false)
	if err != nil {
		t.Fatalf("runModelsClean() error = %v", err)
	}
	for _, want := range []string{"stale.bin", "1 candidate(s)", "Nothing was deleted"} {
		if !contains(out, want) {
			t.Errorf("clean output missing %q:\n%s", want, out)
		}
	}
	if contains(candidateSection(out), "silero_vad.onnx") {
		t.Errorf("clean must not offer the required asset for deletion:\n%s", out)
	}
	if !contains(keptSection(out), "silero_vad.onnx") {
		t.Errorf("clean must show the required asset as kept, with a reason:\n%s", out)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "stale.bin")); err != nil || len(data) != 4 {
		t.Errorf("dry run must not touch candidates: %v", err)
	}
}

func TestModelsCleanDryRunReportsNoCandidates(t *testing.T) {
	stubPersonas(t, nil, nil)
	cfg := &config.Config{STTProvider: "none", TTSProvider: "none", VADEnabled: false}

	out, err := runClean(t, cfg, t.TempDir(), true, true, false, false)
	if err != nil {
		t.Fatalf("runModelsClean() error = %v", err)
	}
	if !contains(out, "No removable assets") {
		t.Errorf("empty clean should report no removable assets:\n%s", out)
	}
}

func TestModelsCleanJSONIsMachineReadable(t *testing.T) {
	stubPersonas(t, nil, nil)
	cfg := &config.Config{STTProvider: "none", TTSProvider: "none", VADEnabled: true}
	dir := t.TempDir()
	for _, name := range []string{"stale.bin", "silero_vad.onnx"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out, err := runClean(t, cfg, dir, true, true, false, true)
	if err != nil {
		t.Fatalf("runModelsClean() error = %v", err)
	}
	var plan config.CleanPlan
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\n%s", err, out)
	}
	if plan.SchemaVersion != config.CleanPlanSchemaVersion {
		t.Errorf("schema_version = %d, want %d", plan.SchemaVersion, config.CleanPlanSchemaVersion)
	}
	if plan.ModelsDir != dir {
		t.Errorf("models_dir = %q, want %q", plan.ModelsDir, dir)
	}
	if len(plan.Candidates) != 1 || plan.Candidates[0].Path != filepath.Join(dir, "stale.bin") || plan.Candidates[0].Size != 4 {
		t.Fatalf("json candidates = %+v, want one 4-byte stale.bin", plan.Candidates)
	}
	if plan.Candidates[0].Rel != "stale.bin" || plan.Candidates[0].Category != config.CleanCategoryAsset || plan.Candidates[0].Kind != config.CleanKindFile {
		t.Errorf("candidate = %+v, want rel/category/kind filled in", plan.Candidates[0])
	}
	if plan.TotalBytes != 4 {
		t.Errorf("total_bytes = %d, want 4", plan.TotalBytes)
	}
	if plan.PlanID != config.CleanPlanID(plan.Candidates) {
		t.Errorf("plan_id = %q, want the sha256 of the sorted candidate paths", plan.PlanID)
	}
	if len(plan.Protected) == 0 {
		t.Error("dry run must report what it keeps, not just what it would delete")
	}
}

func TestModelsCleanDryRunJSONClassifiesJunk(t *testing.T) {
	stubPersonas(t, nil, nil)
	cfg := &config.Config{STTProvider: "none", TTSProvider: "none", VADEnabled: false}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".extract-1234"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".archive-99.tar.bz2.part"), []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stale.bin"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runClean(t, cfg, dir, true, true, false, true)
	if err != nil {
		t.Fatalf("runModelsClean() error = %v", err)
	}
	var plan config.CleanPlan
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\n%s", err, out)
	}
	want := map[string]config.CleanCategory{
		".archive-99.tar.bz2.part": config.CleanCategoryJunk,
		".extract-1234":            config.CleanCategoryJunk,
		"stale.bin":                config.CleanCategoryAsset,
	}
	if len(plan.Candidates) != len(want) {
		t.Fatalf("candidates = %+v, want %d", plan.Candidates, len(want))
	}
	for _, c := range plan.Candidates {
		if want[c.Rel] != c.Category {
			t.Errorf("candidate %q category = %q, want %q", c.Rel, c.Category, want[c.Rel])
		}
	}
	if plan.Candidates[0].Rel > plan.Candidates[len(plan.Candidates)-1].Rel {
		t.Errorf("candidates must be sorted by rel for a stable plan id: %+v", plan.Candidates)
	}
}

// candidateSection is the part of the human output that offers deletions.
func candidateSection(out string) string {
	if i := strings.Index(out, "\n  Kept ("); i >= 0 {
		return out[:i]
	}
	return out
}

// keptSection is the part that explains what was preserved and why.
func keptSection(out string) string {
	if i := strings.Index(out, "\n  Kept ("); i >= 0 {
		return out[i:]
	}
	return ""
}

func TestModelsCleanYesDeletesOnlyExtras(t *testing.T) {
	stubPersonas(t, nil, nil)
	cfg := &config.Config{STTProvider: "none", TTSProvider: "none", VADEnabled: true}
	dir := t.TempDir()
	required := filepath.Join(dir, "silero_vad.onnx")
	stale := filepath.Join(dir, "stale.bin")
	oldDir := filepath.Join(dir, "old-model")
	if err := os.WriteFile(required, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("delete"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "model.bin"), []byte("delete"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runClean(t, cfg, dir, true, false, true, false)
	if err != nil {
		t.Fatalf("runModelsClean() error = %v", err)
	}
	for _, want := range []string{"stale.bin", "old-model", "Deleted 2 candidate(s)"} {
		if !contains(out, want) {
			t.Errorf("clean --yes output missing %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(required); err != nil {
		t.Fatalf("required asset was deleted: %v", err)
	}
	for _, p := range []string{stale, oldDir} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s still exists after clean --yes", p)
		}
	}
}

func TestModelsCleanYesJSONAlwaysNeedsAPlan(t *testing.T) {
	// --json is a program, and a program cannot be the human who read the
	// list — even on a pty, where isatty says otherwise.
	stubPersonas(t, nil, nil)
	stubTTY(t, true)
	cfg := &config.Config{STTProvider: "none", TTSProvider: "none", VADEnabled: false}
	dir := t.TempDir()
	stale := filepath.Join(dir, "stale.bin")
	if err := os.WriteFile(stale, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCleanOpts(t, cfg, dir, cleanOptions{Unused: true, Yes: true, JSON: true}, "y\n")
	if err == nil || !contains(err.Error(), "--plan is required with --yes --json") {
		t.Fatalf("clean error = %v, want --plan demanded for a machine caller", err)
	}
	if out != "" {
		t.Errorf("a refused clean must print nothing:\n%s", out)
	}
	if _, statErr := os.Stat(stale); statErr != nil {
		t.Fatalf("a refused clean must delete nothing: %v", statErr)
	}
}

func TestModelsCleanCommandRegistersFlags(t *testing.T) {
	for _, name := range []string{"unused", "dry-run", "yes", "json", "plan"} {
		if modelsCleanCmd.Flags().Lookup(name) == nil {
			t.Errorf("clean command missing --%s flag", name)
		}
	}
}

func contains(s, sub string) bool {
	return bytes.Contains([]byte(s), []byte(sub))
}

func TestModelsCleanFailsClosedWhenPersonasCannotBeListed(t *testing.T) {
	// A required set that cannot see every persona is a required set that
	// classifies a persona's models as unused. Refuse instead.
	stubPersonas(t, nil, errors.New("personas/veronica/persona.yaml: unreadable"))
	dir := t.TempDir()
	stale := filepath.Join(dir, "stale.bin")
	if err := os.WriteFile(stale, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, yes := range []bool{false, true} {
		out, err := runClean(t, fullCfg(), dir, true, !yes, yes, false)
		if err == nil || !contains(err.Error(), "clean: cannot determine required assets") {
			t.Fatalf("clean error = %v, want it to refuse with an unresolved required set", err)
		}
		if contains(out, "candidate") {
			t.Errorf("a refused clean must list nothing:\n%s", out)
		}
		if _, statErr := os.Stat(stale); statErr != nil {
			t.Fatalf("a refused clean must delete nothing: %v", statErr)
		}
	}
}

func TestModelsCleanKeepsPersonaPinnedQwenPackage(t *testing.T) {
	// The 2026-08-17 incident: global TTS is kokoro, six personas speak
	// through the native qwen package, and clean deleted 6 GB of it.
	stubPersonas(t, []*persona.Profile{qwenPersonaProfile("veronica", "0.6b")}, nil)
	cfg := &config.Config{STTProvider: "none", TTSProvider: "kokoro", VADEnabled: false}
	dir := t.TempDir()
	worker := filepath.Join(dir, "qwen3-tts", "bin", "qwen3-tts-worker")
	if err := os.MkdirAll(filepath.Dir(worker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(worker, []byte("worker"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "stale.bin")
	if err := os.WriteFile(stale, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runClean(t, cfg, dir, true, false, true, false)
	if err != nil {
		t.Fatalf("runModelsClean() error = %v", err)
	}
	if _, statErr := os.Stat(worker); statErr != nil {
		t.Fatalf("persona-pinned qwen package was deleted: %v", statErr)
	}
	if contains(candidateSection(out), "qwen3-tts") {
		t.Errorf("qwen package must never be offered as a candidate:\n%s", out)
	}
	if !contains(keptSection(out), "persona veronica: qwen3-tts tier 0.6b") {
		t.Errorf("kept list must name the persona that speaks through the package:\n%s", out)
	}
	if _, statErr := os.Stat(stale); !os.IsNotExist(statErr) {
		t.Errorf("clean --yes should still remove real leftovers")
	}
}

// TestModelsCleanFixtureRoundTrips pins the wire contract the Obey Voice Mac
// app decodes. The fixture is captured from the real binary by
// testdata/capture-models-clean-fixture.sh; re-run that script if this fails
// because the payload legitimately changed.
func TestModelsCleanFixtureRoundTrips(t *testing.T) {
	raw := readFixture(t, "models-clean-dry-run.json")

	var plan config.CleanPlan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		t.Fatalf("fixture is not valid JSON: %v", err)
	}
	if plan.SchemaVersion != config.CleanPlanSchemaVersion {
		t.Errorf("schema_version = %d, want %d", plan.SchemaVersion, config.CleanPlanSchemaVersion)
	}
	wantCategories := map[string]config.CleanCategory{
		".archive-8c1d.tar.bz2.part": config.CleanCategoryJunk,
		".extract-9f2a":              config.CleanCategoryJunk,
		"kokoro-v0.19":               config.CleanCategoryAsset,
	}
	if len(plan.Candidates) != len(wantCategories) {
		t.Fatalf("candidates = %+v, want %d", plan.Candidates, len(wantCategories))
	}
	var total int64
	for _, c := range plan.Candidates {
		if want, ok := wantCategories[c.Rel]; !ok || want != c.Category {
			t.Errorf("candidate %q category = %q, want %q", c.Rel, c.Category, want)
		}
		total += c.Size
	}
	if total != plan.TotalBytes {
		t.Errorf("total_bytes = %d, want %d", plan.TotalBytes, total)
	}
	if plan.PlanID != config.CleanPlanID(plan.Candidates) {
		t.Errorf("plan_id = %q does not match its candidate list", plan.PlanID)
	}
	// The whole point of the fixture: the persona-pinned native package is
	// kept, and the payload says which persona keeps it.
	if !contains(raw, "persona veronica: qwen3-tts tier 0.6b") {
		t.Errorf("fixture must show the qwen package kept for its persona:\n%s", raw)
	}
	if !contains(raw, "config sherpa_streaming_model") {
		t.Errorf("fixture must show the configured streaming model kept in offline mode:\n%s", raw)
	}

	encoded, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		t.Fatalf("re-encoding the plan: %v", err)
	}
	if string(encoded)+"\n" != raw {
		t.Errorf("CleanPlan does not round-trip the captured payload:\ngot:\n%s\nwant:\n%s", encoded, raw)
	}
}

func TestModelsCleanApplyRefusesWithoutAPlanWhenNotInteractive(t *testing.T) {
	// A non-interactive --yes is the Mac app's one-click delete. It must name
	// the list it is deleting, or delete nothing.
	stubPersonas(t, nil, nil)
	stubTTY(t, false)
	cfg := &config.Config{STTProvider: "none", TTSProvider: "none", VADEnabled: true}
	dir, stale := cleanFixtureDir(t)

	out, err := runCleanOpts(t, cfg, dir, cleanOptions{Unused: true, Yes: true}, "y\n")
	if err == nil || !contains(err.Error(), "clean: --plan is required when not interactive") {
		t.Fatalf("clean error = %v, want the non-interactive refusal", err)
	}
	if ExitCode(err) == 0 {
		t.Error("a refused apply must exit non-zero")
	}
	if contains(out, "Deleted") {
		t.Errorf("a refused apply must delete nothing:\n%s", out)
	}
	if _, statErr := os.Stat(stale); statErr != nil {
		t.Fatalf("a refused apply must delete nothing: %v", statErr)
	}
}

func TestModelsCleanApplyRefusesAStalePlan(t *testing.T) {
	stubPersonas(t, nil, nil)
	stubTTY(t, false)
	cfg := &config.Config{STTProvider: "none", TTSProvider: "none", VADEnabled: true}
	dir, stale := cleanFixtureDir(t)
	current := currentPlanID(t, cfg, dir)

	stalePlan := filepath.Join(t.TempDir(), "plan.json")
	// A plan captured when the models dir held nothing removable: a valid
	// document, describing a list that is no longer the current one.
	staleID := config.CleanPlanID(nil)
	body := fmt.Sprintf(`{"schema_version":2,"models_dir":%q,"candidates":[],"protected":[],"total_bytes":0,"plan_id":%q}`, dir, staleID)
	if err := os.WriteFile(stalePlan, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCleanOpts(t, cfg, dir, cleanOptions{Unused: true, Yes: true, JSON: true, Plan: stalePlan}, "")
	if err == nil {
		t.Fatal("clean error = nil, want plan_changed")
	}
	if ExitCode(err) == 0 {
		t.Error("plan_changed must exit non-zero")
	}
	var payload config.PlanChangedError
	if jsonErr := json.Unmarshal([]byte(out), &payload); jsonErr != nil {
		t.Fatalf("plan_changed payload is not JSON: %v\n%s", jsonErr, out)
	}
	if payload.Kind != config.PlanChangedKind || payload.PlanID != staleID || payload.CurrentPlanID != current {
		t.Errorf("payload = %+v, want plan_changed with both ids", payload)
	}
	if _, statErr := os.Stat(stale); statErr != nil {
		t.Fatalf("a changed plan must delete nothing: %v", statErr)
	}
}

func TestModelsCleanApplyAcceptsTheReviewedPlan(t *testing.T) {
	cases := []struct {
		name string
		// plan is built from the current plan id: a document, a bare id, or
		// stdin.
		build func(t *testing.T, dir, planID string) (flag, stdin string)
	}{
		{
			name: "dry-run document from a file",
			build: func(t *testing.T, dir, planID string) (string, string) {
				path := filepath.Join(t.TempDir(), "plan.json")
				body := fmt.Sprintf(`{"schema_version":2,"models_dir":%q,"candidates":[{"path":%q,"rel":"stale.bin","size_bytes":4,"category":"asset","kind":"file"}],"protected":[],"total_bytes":4,"plan_id":%q}`,
					dir, filepath.Join(dir, "stale.bin"), planID)
				if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
				return path, ""
			},
		},
		{
			name: "document on stdin",
			build: func(t *testing.T, dir, planID string) (string, string) {
				return "-", fmt.Sprintf(`{"schema_version":2,"models_dir":%q,"candidates":[{"path":%q,"rel":"stale.bin","size_bytes":4,"category":"asset","kind":"file"}],"protected":[],"total_bytes":4,"plan_id":%q}`,
					dir, filepath.Join(dir, "stale.bin"), planID)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubPersonas(t, nil, nil)
			stubTTY(t, false)
			cfg := &config.Config{STTProvider: "none", TTSProvider: "none", VADEnabled: true}
			dir, stale := cleanFixtureDir(t)
			flag, stdin := tc.build(t, dir, currentPlanID(t, cfg, dir))

			out, err := runCleanOpts(t, cfg, dir, cleanOptions{Unused: true, Yes: true, JSON: true, Plan: flag}, stdin)
			if err != nil {
				t.Fatalf("runModelsClean() error = %v\n%s", err, out)
			}
			var result config.CleanApplyResult
			if jsonErr := json.Unmarshal([]byte(out), &result); jsonErr != nil {
				t.Fatalf("apply output is not JSON: %v\n%s", jsonErr, out)
			}
			if result.SchemaVersion != config.CleanPlanSchemaVersion {
				t.Errorf("schema_version = %d, want %d", result.SchemaVersion, config.CleanPlanSchemaVersion)
			}
			if len(result.Deleted) != 1 || result.Deleted[0].Rel != "stale.bin" || result.BytesFreed != 4 {
				t.Fatalf("result = %+v, want the reviewed leftover deleted", result)
			}
			if len(result.Skipped) != 0 {
				t.Errorf("skipped = %+v, want none", result.Skipped)
			}
			if _, statErr := os.Stat(stale); !os.IsNotExist(statErr) {
				t.Error("the reviewed candidate should be gone")
			}
			if _, statErr := os.Stat(filepath.Join(dir, "silero_vad.onnx")); statErr != nil {
				t.Errorf("a required asset must survive an apply: %v", statErr)
			}
		})
	}
}

func TestModelsCleanRejectsAnUnreadablePlan(t *testing.T) {
	stubPersonas(t, nil, nil)
	stubTTY(t, false)
	cfg := &config.Config{STTProvider: "none", TTSProvider: "none", VADEnabled: true}
	dir, stale := cleanFixtureDir(t)

	cases := []struct {
		name    string
		plan    string
		stdin   string
		wantErr string
	}{
		{name: "missing file", plan: filepath.Join(dir, "nope.json"), wantErr: "clean: reading plan"},
		{name: "not a plan", plan: "-", stdin: "yes please", wantErr: "is not a dry-run document"},
		{name: "bare plan id", plan: "-", stdin: strings.Repeat("a", 64), wantErr: "does not name the models dir"},
		{name: "document without a models dir", plan: "-", stdin: `{"schema_version":2,"candidates":[],"plan_id":"` + config.CleanPlanID(nil) + `"}`, wantErr: "missing models_dir"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runCleanOpts(t, cfg, dir, cleanOptions{Unused: true, Yes: true, JSON: true, Plan: tc.plan}, tc.stdin)
			if err == nil || !contains(err.Error(), tc.wantErr) {
				t.Fatalf("clean error = %v, want it to mention %q", err, tc.wantErr)
			}
			if _, statErr := os.Stat(stale); statErr != nil {
				t.Fatalf("an unreadable plan must delete nothing: %v", statErr)
			}
		})
	}
}

func TestModelsCleanApplyStillWorksInteractively(t *testing.T) {
	// A human who just read the printed list keeps the old one-command flow.
	stubPersonas(t, nil, nil)
	cfg := &config.Config{STTProvider: "none", TTSProvider: "none", VADEnabled: true}
	dir, stale := cleanFixtureDir(t)

	out, err := runClean(t, cfg, dir, true, false, true, false)
	if err != nil {
		t.Fatalf("runModelsClean() error = %v", err)
	}
	if !contains(out, "Deleted 1 candidate(s)") {
		t.Errorf("interactive apply output = %s", out)
	}
	if _, statErr := os.Stat(stale); !os.IsNotExist(statErr) {
		t.Error("interactive apply should delete the reviewed candidate")
	}
}

func TestModelsCleanRejectsAPlanOnADryRun(t *testing.T) {
	// A dry run produces a plan; consuming one would suggest the list had been
	// checked against something.
	stubPersonas(t, nil, nil)
	cfg := &config.Config{STTProvider: "none", TTSProvider: "none", VADEnabled: false}

	out, err := runCleanOpts(t, cfg, t.TempDir(), cleanOptions{Unused: true, DryRun: true, Plan: "plan.json"}, "")
	if err == nil || !contains(err.Error(), "--plan applies to --yes") {
		t.Fatalf("clean error = %v, want --plan rejected on a dry run", err)
	}
	if out != "" {
		t.Errorf("a rejected clean should print nothing:\n%s", out)
	}
}

func TestModelsCleanRefusesAPlanFromAnotherModelsDir(t *testing.T) {
	// Same relative candidate list, different install: the ids match but the
	// plan was never captured here.
	stubPersonas(t, nil, nil)
	stubTTY(t, false)
	cfg := &config.Config{STTProvider: "none", TTSProvider: "none", VADEnabled: true}
	dir, stale := cleanFixtureDir(t)
	elsewhere := t.TempDir()
	planID := currentPlanID(t, cfg, dir)

	planPath := filepath.Join(t.TempDir(), "plan.json")
	body := fmt.Sprintf(`{"schema_version":2,"models_dir":%q,"candidates":[{"path":%q,"rel":"stale.bin","size_bytes":4,"category":"asset","kind":"file"}],"protected":[],"total_bytes":4,"plan_id":%q}`,
		elsewhere, filepath.Join(elsewhere, "stale.bin"), planID)
	if err := os.WriteFile(planPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := runCleanOpts(t, cfg, dir, cleanOptions{Unused: true, Yes: true, JSON: true, Plan: planPath}, "")
	if err == nil {
		t.Fatal("clean error = nil, want a plan from another models dir refused")
	}
	if _, statErr := os.Stat(stale); statErr != nil {
		t.Fatalf("a foreign plan must delete nothing: %v", statErr)
	}
}

func TestModelsCleanFailsClosedOnAPersonaItCannotLoad(t *testing.T) {
	// persona.List skips a directory whose name is not a valid id rather than
	// failing. A persona clean cannot see is a persona whose models it would
	// offer to delete.
	stubPersonas(t, nil, nil)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "Veronica"), 0o755); err != nil {
		t.Fatal(err)
	}
	stubPersonaDir(t, dir)
	models, stale := cleanFixtureDir(t)

	_, err := runClean(t, fullCfg(), models, true, true, false, false)
	if err == nil || !contains(err.Error(), "could not be loaded") {
		t.Fatalf("clean error = %v, want it to refuse an unloadable persona directory", err)
	}
	if _, statErr := os.Stat(stale); statErr != nil {
		t.Fatalf("a refused clean must delete nothing: %v", statErr)
	}
}

func TestModelsCleanInteractiveApplyNeedsAYes(t *testing.T) {
	// isatty is not consent: the human who read the list has to answer.
	stubPersonas(t, nil, nil)
	stubTTY(t, true)
	cfg := &config.Config{STTProvider: "none", TTSProvider: "none", VADEnabled: true}
	dir, stale := cleanFixtureDir(t)

	out, err := runCleanOpts(t, cfg, dir, cleanOptions{Unused: true, Yes: true}, "n\n")
	if err == nil || !contains(err.Error(), "not confirmed") {
		t.Fatalf("clean error = %v, want an unconfirmed apply refused", err)
	}
	if !contains(out, "Delete 1 item(s)") {
		t.Errorf("the prompt must name the count and size:\n%s", out)
	}
	if _, statErr := os.Stat(stale); statErr != nil {
		t.Fatalf("an unconfirmed apply must delete nothing: %v", statErr)
	}
}

func TestModelsCleanJSONReportsFailuresOnStdout(t *testing.T) {
	// A --json caller must never be left with empty stdout and a banner on
	// stderr it cannot parse.
	stubPersonas(t, nil, errors.New("personas/veronica/persona.yaml: unreadable"))
	stubTTY(t, false)
	cfg := &config.Config{STTProvider: "none", TTSProvider: "none", VADEnabled: false}

	out, err := runCleanOpts(t, cfg, t.TempDir(), cleanOptions{Unused: true, DryRun: true, JSON: true}, "")
	if err == nil {
		t.Fatal("clean error = nil, want the persona failure")
	}
	var failure struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if jsonErr := json.Unmarshal([]byte(out), &failure); jsonErr != nil {
		t.Fatalf("failure is not JSON: %v\n%s", jsonErr, out)
	}
	if failure.Error != "required_assets" || !contains(failure.Message, "cannot determine required assets") {
		t.Errorf("failure = %+v, want a required_assets error naming the cause", failure)
	}
}
