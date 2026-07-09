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
	"testing"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/config"
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

func TestModelsStatusScoped(t *testing.T) {
	cases := []struct {
		name    string
		scope   scopeFlags
		want    []string
		exclude []string
	}{
		{
			name:    "tts excludes stt and vad",
			scope:   scopeFlags{tts: true},
			want:    []string{"kokoro-tts", "1 asset(s), 1 missing"},
			exclude: []string{"silero_vad.onnx", "whisper-base.en"},
		},
		{
			name:    "tts and vad union",
			scope:   scopeFlags{tts: true, vad: true},
			want:    []string{"kokoro-tts", "silero_vad.onnx", "2 asset(s), 2 missing"},
			exclude: []string{"whisper-base.en"},
		},
		{
			name:  "no flags includes everything",
			scope: scopeFlags{},
			want:  []string{"kokoro-tts", "silero_vad.onnx", "whisper-base.en", "3 asset(s), 3 missing"},
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
	for _, want := range []string{"silero_vad.onnx", "kokoro-tts", "whisper-base.en", "missing", "3 asset(s), 3 missing"} {
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

func runClean(t *testing.T, cfg *config.Config, modelsDir string, unused, dryRun, yes, asJSON bool) (string, error) {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	err := runModelsClean(cmd, cfg, modelsDir, unused, dryRun, yes, asJSON)
	return buf.String(), err
}

func TestModelsCleanFlagValidation(t *testing.T) {
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
	if contains(out, "silero_vad.onnx") {
		t.Errorf("clean output must not list the required asset:\n%s", out)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "stale.bin")); err != nil || len(data) != 4 {
		t.Errorf("dry run must not touch candidates: %v", err)
	}
}

func TestModelsCleanDryRunReportsNoCandidates(t *testing.T) {
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
	cfg := &config.Config{STTProvider: "none", TTSProvider: "none", VADEnabled: false}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stale.bin"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runClean(t, cfg, dir, true, true, false, true)
	if err != nil {
		t.Fatalf("runModelsClean() error = %v", err)
	}
	var candidates []config.CleanCandidate
	if err := json.Unmarshal([]byte(out), &candidates); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\n%s", err, out)
	}
	if len(candidates) != 1 || candidates[0].Path != filepath.Join(dir, "stale.bin") || candidates[0].Size != 4 {
		t.Fatalf("json candidates = %+v, want one 4-byte stale.bin", candidates)
	}
}

func TestModelsCleanYesDeletesOnlyExtras(t *testing.T) {
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

func TestModelsCleanYesJSONReportsDeletedCandidates(t *testing.T) {
	cfg := &config.Config{STTProvider: "none", TTSProvider: "none", VADEnabled: false}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stale.bin"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runClean(t, cfg, dir, true, false, true, true)
	if err != nil {
		t.Fatalf("runModelsClean() error = %v", err)
	}
	var result config.CleanApplyResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\n%s", err, out)
	}
	if len(result.Deleted) != 1 || result.Deleted[0].Path != filepath.Join(dir, "stale.bin") || result.Bytes != 4 {
		t.Fatalf("json result = %+v, want one deleted 4-byte stale.bin", result)
	}
}

func TestModelsCleanCommandRegistersFlags(t *testing.T) {
	for _, name := range []string{"unused", "dry-run", "yes", "json"} {
		if modelsCleanCmd.Flags().Lookup(name) == nil {
			t.Errorf("clean command missing --%s flag", name)
		}
	}
}

func contains(s, sub string) bool {
	return bytes.Contains([]byte(s), []byte(sub))
}
