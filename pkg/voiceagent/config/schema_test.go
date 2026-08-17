package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// loadIntoEmptyInstall points the package at a fresh install root with an empty
// config file so AllKeys reflects defaults plus env bindings only, with no
// leftovers from whatever another test in this package last read.
func loadIntoEmptyInstall(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	SetConfigDirForTest(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("writing empty config: %v", err)
	}
	if _, err := LoadRaw(); err != nil {
		t.Fatalf("LoadRaw: %v", err)
	}
}

func TestSchemaCoversEveryConfigKey(t *testing.T) {
	loadIntoEmptyInstall(t)

	inSchema := map[string]bool{}
	for _, key := range SchemaKeys() {
		if inSchema[key] {
			t.Errorf("duplicate key in schema: %s", key)
		}
		inSchema[key] = true
	}

	inConfig := map[string]bool{}
	for _, key := range AllKeys() {
		inConfig[key] = true
	}

	var missing, extra []string
	for key := range inConfig {
		if !inSchema[key] {
			missing = append(missing, key)
		}
	}
	for key := range inSchema {
		if !inConfig[key] {
			extra = append(extra, key)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)

	if len(missing) > 0 {
		t.Errorf("config keys with no KeySpec (add one in schema_keys*.go): %v", missing)
	}
	if len(extra) > 0 {
		t.Errorf("KeySpecs for keys config does not define (add a SetDefault or drop the spec): %v", extra)
	}
}

func TestSchemaDefaultsMatchConfigDefaults(t *testing.T) {
	defaults := viper.New()
	setDefaults(defaults)

	for _, spec := range Schema() {
		if !defaults.IsSet(spec.Key) {
			// Keys that exist only through an env binding (compact_prompt) have
			// no viper default; the schema's zero value is the contract.
			if spec.Default != "" && spec.Default != nil {
				t.Errorf("%s: schema default %#v but config sets none", spec.Key, spec.Default)
			}
			continue
		}
		want := fmt.Sprintf("%v", defaults.Get(spec.Key))
		got := fmt.Sprintf("%v", spec.Default)
		if spec.Type == TypeStringList || spec.Type == TypeOpaque {
			// Slice defaults print differently ([] vs []interface {}{}); only
			// emptiness is contractual.
			continue
		}
		if want != got {
			t.Errorf("%s: schema default %q, config default %q", spec.Key, got, want)
		}
	}
}

func TestSchemaOrderIsStable(t *testing.T) {
	schema := Schema()
	rank := map[string]int{}
	for i, group := range Groups() {
		rank[group] = i
	}
	for i := 1; i < len(schema); i++ {
		prev, cur := schema[i-1], schema[i]
		if _, ok := rank[cur.Group]; !ok {
			t.Fatalf("%s: unknown group %q", cur.Key, cur.Group)
		}
		if rank[prev.Group] > rank[cur.Group] {
			t.Fatalf("group order broken at %s -> %s", prev.Key, cur.Key)
		}
		if rank[prev.Group] == rank[cur.Group] && prev.Key >= cur.Key {
			t.Fatalf("keys unsorted within %s: %s before %s", cur.Group, prev.Key, cur.Key)
		}
	}
}

func TestSchemaCopiesAreIndependent(t *testing.T) {
	first := Schema()
	for i := range first {
		if len(first[i].Enum) > 0 {
			first[i].Enum[0] = "mutated"
		}
	}
	for _, spec := range Schema() {
		for _, value := range spec.Enum {
			if value == "mutated" {
				t.Fatalf("%s: Schema handed out a shared enum slice", spec.Key)
			}
		}
	}
}

func TestSpecForIsCaseInsensitiveAndRejectsUnknown(t *testing.T) {
	if _, ok := SpecFor("nope_not_a_key"); ok {
		t.Fatal("SpecFor accepted an unknown key")
	}
	spec, ok := SpecFor("  VAD_Silence_Duration ")
	if !ok {
		t.Fatal("SpecFor rejected a known key in mixed case")
	}
	if spec.Key != "vad_silence_duration" || spec.Type != TypeFloat {
		t.Fatalf("unexpected spec: %+v", spec)
	}
}

func TestSchemaCopyRules(t *testing.T) {
	for _, spec := range Schema() {
		if strings.TrimSpace(spec.Title) == "" {
			t.Errorf("%s: empty title", spec.Key)
		}
		if strings.TrimSpace(spec.Help) == "" {
			t.Errorf("%s: empty help", spec.Key)
		}
		if strings.HasSuffix(spec.Title, ":") {
			t.Errorf("%s: title ends with a colon", spec.Key)
		}
		// "Sets vad_silence_duration." is the failure mode: help must state the
		// consequence, not the mechanism. A key that is also an ordinary word
		// ("language") may legitimately appear in a sentence.
		if strings.ContainsAny(spec.Key, "_.") && strings.Contains(spec.Help, spec.Key) {
			t.Errorf("%s: help names the key instead of the consequence", spec.Key)
		}
		if strings.HasPrefix(spec.Help, "Sets ") {
			t.Errorf("%s: help describes the mechanism, not the consequence", spec.Key)
		}
		if spec.Type == TypeEnum && spec.Key != "meeting.route.default" && len(spec.Enum) == 0 {
			t.Errorf("%s: enum type with no values", spec.Key)
		}
		if spec.Type != TypeEnum && len(spec.Enum) > 0 {
			t.Errorf("%s: enum values on a %s key", spec.Key, spec.Type)
		}
		if spec.Min != nil && spec.Max != nil && *spec.Min > *spec.Max {
			t.Errorf("%s: min %v above max %v", spec.Key, *spec.Min, *spec.Max)
		}
		if (spec.ManagedBy == "") != spec.Editable {
			t.Errorf("%s: editable=%v with managed_by=%q", spec.Key, spec.Editable, spec.ManagedBy)
		}
		if spec.Type == TypeOpaque && spec.Editable {
			t.Errorf("%s: opaque keys are never editable", spec.Key)
		}
	}
}

func TestSchemaCarriesTheHelpStringsTheCodeEnforces(t *testing.T) {
	// Three help strings state traps the code enforces; they are contract, not
	// copy, and are reproduced verbatim from the settings spec.
	want := map[string]string{
		"speaker.live.window_ms":       "Must match the window used when speakers were enrolled — a mismatch roughly doubles missed matches.",
		"meeting.route.body":           "Both options include the full transcript in what gets exported.",
		"speaker.meeting.record_audio": "Keeps the meeting audio file. Without it, speakers can't be re-analysed after the meeting ends.",
		"barge_in_enabled":             "Lets you talk over her. Needs the voice front-end on, and only affects this Mac's microphone — phone clients still need the interrupt button.",
		"remote_tools_enabled":         "Lets turns that arrive over the network run tools on this Mac. Off by default — a phone or browser can otherwise run commands here.",
	}
	for key, help := range want {
		spec, ok := SpecFor(key)
		if !ok {
			t.Errorf("%s: missing from schema", key)
			continue
		}
		if spec.Help != help {
			t.Errorf("%s help:\n got: %s\nwant: %s", key, spec.Help, help)
		}
	}
}

func TestSchemaRestartTruthComesFromRestartTables(t *testing.T) {
	for _, spec := range Schema() {
		if got, want := spec.RestartRequired, RestartRequired(spec.Key); got != want {
			t.Errorf("%s: restart_required %v, restart table says %v", spec.Key, got, want)
		}
		if got, want := spec.RestartVerified, RestartVerified(spec.Key); got != want {
			t.Errorf("%s: restart_verified %v, restart table says %v", spec.Key, got, want)
		}
	}
	for key := range restartNotRequired {
		if _, ok := SpecFor(key); !ok {
			t.Errorf("restartNotRequired names %q, which is not a config key", key)
		}
	}
	for key := range restartVerified {
		if _, ok := SpecFor(key); !ok {
			t.Errorf("restartVerified names %q, which is not a config key", key)
		}
	}
	// An unverified key must still report restart_required, the honest default.
	for _, spec := range Schema() {
		if !spec.RestartVerified && !spec.RestartRequired {
			t.Errorf("%s: claims no restart is needed without verification", spec.Key)
		}
	}
}

func TestSchemaEnvBindingsMatchTheLoader(t *testing.T) {
	for _, spec := range Schema() {
		if got, want := spec.Env, envBindings[spec.Key]; got != want {
			t.Errorf("%s: schema env %q, loader binds %q", spec.Key, got, want)
		}
	}
	for key := range envBindings {
		if _, ok := SpecFor(key); !ok {
			t.Errorf("loader binds %q to an env var, but it has no KeySpec", key)
		}
	}
}

func TestPersonaOverridableMatchesApplyRoutingTable(t *testing.T) {
	// The union of the keys persona.Apply writes. internal/persona owns the
	// routing table; this asserts the schema agrees with it, and
	// TestOverriddenKeysMatchesApply asserts the routing table agrees with Apply.
	want := []string{
		"active_persona", "agent_name", "brain_provider", "grok_model",
		"ollama_model", "persona", "qwen_tts_model_tier", "qwen_tts_voice",
		"tts_provider", "tts_voice",
	}
	var got []string
	for _, spec := range Schema() {
		if spec.PersonaOverridable {
			got = append(got, spec.Key)
		}
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("persona_overridable set:\n got: %v\nwant: %v", got, want)
	}
}

func TestSchemaForFillsDestinationIDs(t *testing.T) {
	cfg := &Config{}
	cfg.Meeting.Route.Destinations = []MeetingDestinationConfig{
		{ID: "campaign"}, {ID: " "}, {ID: "notes"},
	}
	var spec KeySpec
	for _, candidate := range SchemaFor(cfg) {
		if candidate.Key == "meeting.route.default" {
			spec = candidate
		}
	}
	if strings.Join(spec.Enum, ",") != "campaign,notes" {
		t.Fatalf("destination enum = %v", spec.Enum)
	}
	for _, candidate := range Schema() {
		if candidate.Key == "meeting.route.default" && len(candidate.Enum) != 0 {
			t.Fatalf("SchemaFor leaked into the static schema: %v", candidate.Enum)
		}
	}
	if len(SchemaFor(nil)) != len(Schema()) {
		t.Fatal("SchemaFor(nil) must degrade to Schema()")
	}
}
