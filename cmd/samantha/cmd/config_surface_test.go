package cmd

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

const surfaceConfig = `# Samantha settings.
tts_provider: kokoro

# Voice activity detection.
vad_silence_duration: 0.8
barge_in_enabled: true
`

// newSurfaceInstall points the config package at a throwaway install root and
// returns it. Nothing here may touch the real ~/.obey. A config the loader
// rejects is fine — the read surfaces are contractually resilient to one.
func newSurfaceInstall(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	// SetConfigDirForTest also swaps in a fresh viper, so a value another
	// test wrote through the writer cannot leak into this one.
	config.SetConfigDirForTest(t, dir)
	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o600); err != nil {
			t.Fatalf("seeding config: %v", err)
		}
	}
	_, _ = config.LoadRaw()
	return dir
}

// runConfigSubcommand runs one config subcommand in isolation and returns its
// stdout. The command is built fresh per call, so a --json flag set by one test
// cannot leak into the next, and stderr is kept separate so a diagnostic can
// never corrupt a JSON payload.
func runConfigSubcommand(t *testing.T, build func() *cobra.Command, args ...string) (string, error) {
	t.Helper()
	cmd := build()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func decodeJSON[T any](t *testing.T, raw string) T {
	t.Helper()
	var payload T
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decoding %q: %v", raw, err)
	}
	return payload
}

func TestConfigSchemaJSONMatchesTheSchema(t *testing.T) {
	newSurfaceInstall(t, surfaceConfig)

	out, err := runConfigSubcommand(t, newConfigSchemaCmd, "--json")
	if err != nil {
		t.Fatalf("config schema --json: %v", err)
	}
	payload := decodeJSON[configSchemaPayload](t, out)

	if payload.SchemaVersion != config.SchemaVersion {
		t.Errorf("schema_version = %d, want %d", payload.SchemaVersion, config.SchemaVersion)
	}
	if payload.ConfigFile != config.ConfigFile() {
		t.Errorf("config_file = %q, want %q", payload.ConfigFile, config.ConfigFile())
	}
	if strings.Join(payload.Groups, ",") != strings.Join(config.Groups(), ",") {
		t.Errorf("groups = %v, want %v", payload.Groups, config.Groups())
	}
	if len(payload.Keys) != len(config.Schema()) {
		t.Fatalf("keys = %d, want %d", len(payload.Keys), len(config.Schema()))
	}
}

func TestConfigSchemaSurvivesABrokenConfig(t *testing.T) {
	// A config so broken it cannot be parsed must still yield a schema: the
	// Settings screen is how a user would fix it.
	newSurfaceInstall(t, "tts_provider: [unclosed\n")

	out, err := runConfigSubcommand(t, newConfigSchemaCmd, "--json")
	if err != nil {
		t.Fatalf("config schema --json on a broken config: %v", err)
	}
	payload := decodeJSON[configSchemaPayload](t, out)
	if len(payload.Keys) == 0 {
		t.Fatal("no keys reported for a broken config")
	}
}

func TestConfigGetJSONSourcesAreCorrect(t *testing.T) {
	newSurfaceInstall(t, surfaceConfig)
	t.Setenv("OLLAMA_MODEL", "qwen2.5:14b")
	if _, err := config.LoadRaw(); err != nil {
		t.Fatalf("LoadRaw: %v", err)
	}

	out, err := runConfigSubcommand(t, newConfigGetCmd, "--json")
	if err != nil {
		t.Fatalf("config get --json: %v", err)
	}
	payload := decodeJSON[configValuesPayload](t, out)

	tests := []struct {
		key        string
		wantSource string
		wantValue  any
	}{
		{"max_history", config.SourceDefault, float64(10)},
		{"vad_silence_duration", config.SourceFile, 0.8},
		{"ollama_model", config.SourceEnv, "qwen2.5:14b"},
	}
	for _, tt := range tests {
		entry, ok := payload.Values[tt.key]
		if !ok {
			t.Errorf("%s missing from values", tt.key)
			continue
		}
		if entry.Source != tt.wantSource {
			t.Errorf("%s source = %q, want %q", tt.key, entry.Source, tt.wantSource)
		}
		if entry.Value != tt.wantValue {
			t.Errorf("%s value = %v (%T), want %v", tt.key, entry.Value, entry.Value, tt.wantValue)
		}
	}

	// The value set and the schema key set must agree, or the Mac app renders
	// controls it has no value for.
	schemaOut, err := runConfigSubcommand(t, newConfigSchemaCmd, "--json")
	if err != nil {
		t.Fatalf("config schema --json: %v", err)
	}
	schema := decodeJSON[configSchemaPayload](t, schemaOut)
	var schemaKeys, valueKeys []string
	for _, spec := range schema.Keys {
		schemaKeys = append(schemaKeys, spec.Key)
	}
	for key := range payload.Values {
		valueKeys = append(valueKeys, key)
	}
	sort.Strings(schemaKeys)
	sort.Strings(valueKeys)
	if strings.Join(schemaKeys, ",") != strings.Join(valueKeys, ",") {
		t.Error("config schema and config get disagree about the key set")
	}
}

func TestConfigGetKeyJSON(t *testing.T) {
	newSurfaceInstall(t, surfaceConfig)

	out, err := runConfigSubcommand(t, newConfigGetCmd, "vad_silence_duration", "--json")
	if err != nil {
		t.Fatalf("config get <key> --json: %v", err)
	}
	payload := decodeJSON[configValuePayload](t, out)
	if payload.Key != "vad_silence_duration" || payload.Value != 0.8 {
		t.Errorf("payload = %+v", payload)
	}
	if payload.Type != config.TypeFloat || payload.Source != config.SourceFile {
		t.Errorf("type/source = %v/%v", payload.Type, payload.Source)
	}
	if !payload.RestartRequired {
		t.Error("restart_required = false")
	}
}

func TestConfigGetUnknownKeyPayload(t *testing.T) {
	newSurfaceInstall(t, surfaceConfig)

	out, err := runConfigSubcommand(t, newConfigGetCmd, "vad_silence", "--json")
	if err == nil {
		t.Fatal("expected an error for an unknown key")
	}
	if ExitCode(err) != exitOperationFailed {
		t.Errorf("exit code = %d, want %d", ExitCode(err), exitOperationFailed)
	}
	payload := decodeJSON[configErrorPayload](t, out)
	if payload.Error.Code != config.CodeUnknownKey {
		t.Errorf("code = %q, want %q", payload.Error.Code, config.CodeUnknownKey)
	}
	if len(payload.Error.DidYouMean) == 0 {
		t.Error("no did_you_mean suggestions")
	}
}

func TestConfigGetDoesNotWriteAnything(t *testing.T) {
	// The landmine-1 regression test: config.Load runs the persona migration,
	// which writes to disk. `config get` must use LoadRaw, so an install with no
	// personas directory comes back byte-identical.
	root := newSurfaceInstall(t, surfaceConfig)
	before := snapshotTree(t, root)

	if _, err := runConfigSubcommand(t, newConfigGetCmd, "--json"); err != nil {
		t.Fatalf("config get --json: %v", err)
	}
	if _, err := runConfigSubcommand(t, newConfigSchemaCmd, "--json"); err != nil {
		t.Fatalf("config schema --json: %v", err)
	}

	after := snapshotTree(t, root)
	if strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Errorf("the install root changed:\nbefore:\n%s\nafter:\n%s",
			strings.Join(before, "\n"), strings.Join(after, "\n"))
	}
}

// snapshotTree records every path under root with its size and mode.
func snapshotTree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out = append(out, strings.Join([]string{rel, info.Mode().String(), info.ModTime().String()}, " "))
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	sort.Strings(out)
	return out
}

func TestConfigSetJSONErrorPayloads(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode string
		wantExit int
		wantHas  string
	}{
		{
			name:     "unknown key",
			args:     []string{"vad_silence", "0.5", "--json"},
			wantCode: config.CodeUnknownKey,
			wantExit: exitOperationFailed,
			wantHas:  `"did_you_mean"`,
		},
		{
			name:     "not editable",
			args:     []string{"meeting.route.destinations", "[]", "--json"},
			wantCode: config.CodeNotEditable,
			wantExit: exitOperationFailed,
			wantHas:  "meeting destinations",
		},
		{
			name:     "invalid value",
			args:     []string{"vad_silence_duration", "fast", "--json"},
			wantCode: config.CodeInvalidValue,
			wantExit: exitOperationFailed,
			wantHas:  "between 0.1 and 3",
		},
		{
			name:     "invalid list value",
			args:     []string{"skills_disabled", "pdf-fill", "--json"},
			wantCode: config.CodeInvalidValue,
			wantExit: exitOperationFailed,
			wantHas:  "JSON array",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newSurfaceInstall(t, surfaceConfig)
			out, err := runConfigSubcommand(t, newConfigSetCmd, tt.args...)
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := ExitCode(err); got != tt.wantExit {
				t.Errorf("exit code = %d, want %d", got, tt.wantExit)
			}
			payload := decodeJSON[configErrorPayload](t, out)
			if payload.SchemaVersion != config.SchemaVersion {
				t.Errorf("schema_version = %d", payload.SchemaVersion)
			}
			if payload.Error.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", payload.Error.Code, tt.wantCode)
			}
			if !strings.Contains(out, tt.wantHas) {
				t.Errorf("payload %s\ndoes not contain %q", out, tt.wantHas)
			}
		})
	}
}

func TestConfigSetLockedPayload(t *testing.T) {
	root := newSurfaceInstall(t, surfaceConfig)
	lockPath := filepath.Join(root, "config.yaml.lock")
	if err := os.WriteFile(lockPath, []byte("1\n"), 0o600); err != nil {
		t.Fatalf("seeding lock: %v", err)
	}

	out, err := runConfigSubcommand(t, newConfigSetCmd, "vad_silence_duration", "0.9", "--json")
	if err == nil {
		t.Fatal("expected an error while another writer holds the lock")
	}
	if ExitCode(err) != exitOperationFailed {
		t.Errorf("exit code = %d, want %d", ExitCode(err), exitOperationFailed)
	}
	if code := decodeJSON[configErrorPayload](t, out).Error.Code; code != config.CodeLocked {
		t.Errorf("code = %q, want %q", code, config.CodeLocked)
	}
}

func TestConfigSetUsageErrorExitsTwo(t *testing.T) {
	newSurfaceInstall(t, surfaceConfig)

	for _, args := range [][]string{{"vad_silence_duration"}, {}, {"a", "b", "c"}} {
		_, err := runConfigSubcommand(t, newConfigSetCmd, args...)
		if err == nil {
			t.Fatalf("args %v: expected a usage error", args)
		}
		if got := ExitCode(err); got != exitUsage {
			t.Errorf("args %v: exit code = %d, want %d", args, got, exitUsage)
		}
	}
}

func TestConfigSetJSONSuccessAndNoop(t *testing.T) {
	newSurfaceInstall(t, surfaceConfig)

	out, err := runConfigSubcommand(t, newConfigSetCmd, "vad_silence_duration", "0.9", "--json")
	if err != nil {
		t.Fatalf("config set --json: %v", err)
	}
	payload := decodeJSON[configSetPayload](t, out)
	if !payload.Changed || payload.OldValue != 0.8 || payload.Value != 0.9 {
		t.Errorf("payload = %+v", payload)
	}
	if payload.BackupPath == "" {
		t.Error("no backup reported")
	}

	out, err = runConfigSubcommand(t, newConfigSetCmd, "vad_silence_duration", "0.9", "--json")
	if err != nil {
		t.Fatalf("no-op config set --json: %v", err)
	}
	noop := decodeJSON[configSetPayload](t, out)
	if noop.Changed {
		t.Error("changed = true for a no-op")
	}
	if strings.Contains(out, `"backup"`) {
		t.Errorf("a no-op reported a backup:\n%s", out)
	}
}

func TestConfigSetHelpDocumentsTheListForm(t *testing.T) {
	if !strings.Contains(newConfigSetCmd().Long, `'["pdf-fill","calibre"]'`) {
		t.Error("config set --help does not show the JSON array form for list values")
	}
}

// runRoot drives the real command tree, so registration and the config
// subtree's PersistentPreRunE override are exercised, not just the RunE bodies.
func runRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SilenceUsage = true
	rootCmd.SilenceErrors = true
	rootCmd.SetArgs(args)
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})
	err := rootCmd.Execute()
	return out.String(), err
}

func TestConfigSubcommandsAreRegistered(t *testing.T) {
	newSurfaceInstall(t, surfaceConfig)

	out, err := runRoot(t, "config", "schema", "--json")
	if err != nil {
		t.Fatalf("samantha config schema --json: %v", err)
	}
	if len(decodeJSON[configSchemaPayload](t, out).Keys) == 0 {
		t.Error("no keys through the real command tree")
	}

	out, err = runRoot(t, "config", "get", "vad_silence_duration", "--json")
	if err != nil {
		t.Fatalf("samantha config get: %v", err)
	}
	if got := decodeJSON[configValuePayload](t, out).Value; got != 0.8 {
		t.Errorf("value = %v, want 0.8", got)
	}

	if _, err := runRoot(t, "config", "set", "vad_silence_duration", "0.9", "--json"); err != nil {
		t.Fatalf("samantha config set: %v", err)
	}
	if got := config.Get("vad_silence_duration"); got != 0.9 {
		t.Errorf("set through the tree did not take: %v", got)
	}
}

func TestLegacyConfigFormsSurvive(t *testing.T) {
	newSurfaceInstall(t, surfaceConfig)
	legacy := func(args ...string) (string, error) {
		return runRoot(t, append([]string{"config"}, args...)...)
	}

	if out, err := legacy("vad_silence_duration"); err != nil {
		t.Fatalf("legacy `config <key>`: %v", err)
	} else if !strings.Contains(out, "0.8") {
		t.Errorf("legacy `config <key>` output = %q", out)
	}

	if _, err := legacy("vad_silence_duration", "0.9"); err != nil {
		t.Fatalf("legacy `config <key> <value>`: %v", err)
	}
	if got := config.Get("vad_silence_duration"); got != 0.9 {
		t.Errorf("legacy set did not take: %v", got)
	}

	if out, err := legacy(); err != nil {
		t.Fatalf("legacy `config`: %v", err)
	} else if !strings.Contains(out, "vad_silence_duration") {
		t.Errorf("legacy dump missing keys: %q", out)
	}
}

func TestCapturedFixturesMatchTheCurrentPayloads(t *testing.T) {
	// The fixtures the Mac app decodes are captured from this binary. If a
	// payload's shape changes without re-capturing them, the app is testing
	// against a wire format samantha no longer speaks.
	newSurfaceInstall(t, surfaceConfig)

	t.Run("config-schema", func(t *testing.T) {
		out, err := runConfigSubcommand(t, newConfigSchemaCmd, "--json")
		if err != nil {
			t.Fatalf("config schema --json: %v", err)
		}
		live := decodeJSON[configSchemaPayload](t, out)
		fixture := decodeJSON[configSchemaPayload](t, readFixture(t, "config-schema.json"))
		if len(live.Keys) != len(fixture.Keys) {
			t.Fatalf("fixture has %d keys, the binary emits %d — re-run testdata/capture-config-fixtures.sh",
				len(fixture.Keys), len(live.Keys))
		}
		for i := range live.Keys {
			if live.Keys[i].Key != fixture.Keys[i].Key {
				t.Fatalf("key %d: fixture %q, binary %q — re-run testdata/capture-config-fixtures.sh",
					i, fixture.Keys[i].Key, live.Keys[i].Key)
			}
		}
	})

	t.Run("config-get", func(t *testing.T) {
		root := newSurfaceInstall(t, surfaceConfig)
		seedPersona(t, root, "ada", adaProfile)
		out, err := runConfigSubcommand(t, newConfigGetCmd, "--json")
		if err != nil {
			t.Fatalf("config get --json: %v", err)
		}
		live := decodeJSON[configValuesPayload](t, out)
		fixture := decodeJSON[configValuesPayload](t, readFixture(t, "config-get.json"))
		if fixture.Persona == nil {
			t.Fatal("the captured fixture has no persona block — re-run testdata/capture-config-fixtures.sh")
		}
		if !reflect.DeepEqual(live.Persona.Overrides, fixture.Persona.Overrides) {
			t.Errorf("persona overrides drifted from the fixture:\n live: %v\nfixture: %v",
				live.Persona.Overrides, fixture.Persona.Overrides)
		}
		if len(live.Values) != len(fixture.Values) {
			t.Errorf("fixture has %d values, the binary emits %d — re-run testdata/capture-config-fixtures.sh",
				len(fixture.Values), len(live.Values))
		}
	})

	t.Run("config-set-error", func(t *testing.T) {
		out, _ := runConfigSubcommand(t, newConfigSetCmd, "vad_silence_duration", "fast", "--json")
		live := decodeJSON[configErrorPayload](t, out)
		fixture := decodeJSON[configErrorPayload](t, readFixture(t, "config-set-error.json"))
		if !reflect.DeepEqual(live.Error, fixture.Error) {
			t.Errorf("error payload drifted from the fixture:\n live: %+v\nfixture: %+v", live.Error, fixture.Error)
		}
	})
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return string(data)
}

const adaProfile = `schema: festival-voice.persona.v1
id: ada
display_name: Ada
brain:
  provider: ollama
  model: llama3.1
tts:
  provider: kokoro
  voice: af_sky
prompts:
  persona: ada
`

// seedPersona writes a persona profile into the current install root and makes
// it active in the config file.
func seedPersona(t *testing.T, root, id, profile string) {
	t.Helper()
	dir := filepath.Join(root, "personas", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating persona dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "persona.yaml"), []byte(profile), 0o600); err != nil {
		t.Fatalf("writing persona: %v", err)
	}
	configPath := filepath.Join(root, "config.yaml")
	existing, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	if err := os.WriteFile(configPath, append(existing, []byte("active_persona: "+id+"\n")...), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	if _, err := config.LoadRaw(); err != nil {
		t.Fatalf("LoadRaw: %v", err)
	}
}

func TestConfigGetReportsPersonaOverrides(t *testing.T) {
	root := newSurfaceInstall(t, surfaceConfig)
	seedPersona(t, root, "ada", adaProfile)

	out, err := runConfigSubcommand(t, newConfigGetCmd, "--json")
	if err != nil {
		t.Fatalf("config get --json: %v", err)
	}
	payload := decodeJSON[configValuesPayload](t, out)
	if payload.Persona == nil {
		t.Fatal("no persona block for an active persona with overrides")
	}
	if payload.Persona.ID != "ada" || payload.Persona.DisplayName != "Ada" {
		t.Errorf("persona = %+v", payload.Persona)
	}
	want := "agent_name,persona,active_persona,brain_provider,ollama_model,tts_provider,tts_voice"
	if got := strings.Join(payload.Persona.Overrides, ","); got != want {
		t.Errorf("overrides = %s, want %s", got, want)
	}

	// The values themselves stay app-level: the badge says the persona wins,
	// it does not rewrite what the file holds.
	if got := payload.Values["tts_provider"].Value; got != "kokoro" {
		t.Errorf("tts_provider = %v, want the file's value", got)
	}

	// The single-key form carries the same truth.
	out, err = runConfigSubcommand(t, newConfigGetCmd, "tts_voice", "--json")
	if err != nil {
		t.Fatalf("config get tts_voice --json: %v", err)
	}
	if !decodeJSON[configValuePayload](t, out).OverriddenByPersona {
		t.Error("overridden_by_persona = false for a key the persona sets")
	}
	out, err = runConfigSubcommand(t, newConfigGetCmd, "vad_silence_duration", "--json")
	if err != nil {
		t.Fatalf("config get vad_silence_duration --json: %v", err)
	}
	if decodeJSON[configValuePayload](t, out).OverriddenByPersona {
		t.Error("overridden_by_persona = true for a key no persona touches")
	}
}

func TestConfigGetOmitsPersonaBlockWhenTheProfileIsMissingOrBroken(t *testing.T) {
	tests := []struct {
		name    string
		profile string
		write   bool
	}{
		{name: "no profile on disk", write: false},
		{name: "unparseable profile", profile: "schema: [unclosed\n", write: true},
		{name: "empty profile", profile: "", write: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newSurfaceInstall(t, surfaceConfig)
			if tt.write {
				seedPersona(t, root, "ada", tt.profile)
			} else {
				configPath := filepath.Join(root, "config.yaml")
				existing, err := os.ReadFile(configPath)
				if err != nil {
					t.Fatalf("reading config: %v", err)
				}
				if err := os.WriteFile(configPath, append(existing, []byte("active_persona: ada\n")...), 0o600); err != nil {
					t.Fatalf("writing config: %v", err)
				}
				if _, err := config.LoadRaw(); err != nil {
					t.Fatalf("LoadRaw: %v", err)
				}
			}

			out, err := runConfigSubcommand(t, newConfigGetCmd, "--json")
			if err != nil {
				t.Fatalf("a broken persona must not fail config get: %v", err)
			}
			if payload := decodeJSON[configValuesPayload](t, out); payload.Persona != nil {
				t.Errorf("persona block reported for a %s: %+v", tt.name, payload.Persona)
			}
		})
	}
}

func TestConfigGetWithAPersonaStillWritesNothing(t *testing.T) {
	// Loading a persona profile must not migrate it. This is landmine 1 again,
	// now with a profile actually present.
	root := newSurfaceInstall(t, surfaceConfig)
	seedPersona(t, root, "ada", adaProfile)
	before := snapshotTree(t, root)

	if _, err := runConfigSubcommand(t, newConfigGetCmd, "--json"); err != nil {
		t.Fatalf("config get --json: %v", err)
	}
	if after := snapshotTree(t, root); strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Errorf("the install root changed:\nbefore:\n%s\nafter:\n%s",
			strings.Join(before, "\n"), strings.Join(after, "\n"))
	}
}
