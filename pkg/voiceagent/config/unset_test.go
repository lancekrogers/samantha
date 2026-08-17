package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

// --- error cases: a refused unset must leave the file exactly as it was ---

func TestUnsetKeyFileRejectsUnknownKey(t *testing.T) {
	path := newInstall(t, fourSpaceConfig)

	_, err := UnsetKeyFile("vad_silence")
	setErr := setError(t, err)
	if setErr.Code != CodeUnknownKey {
		t.Fatalf("code = %q, want %q", setErr.Code, CodeUnknownKey)
	}
	if len(setErr.DidYouMean) == 0 {
		t.Error("no did_you_mean for a key that is a prefix of a real one")
	}
	if got := readConfig(t, path); got != fourSpaceConfig {
		t.Errorf("a refused unset rewrote the file:\n%s", got)
	}
}

func TestUnsetKeyFileRefusesKeysAnotherCommandOwns(t *testing.T) {
	path := newInstall(t, "active_persona: veronica\n")

	_, err := UnsetKeyFile("active_persona")
	setErr := setError(t, err)
	if setErr.Code != CodeNotEditable {
		t.Fatalf("code = %q, want %q", setErr.Code, CodeNotEditable)
	}
	if !strings.Contains(setErr.Message, "persona use") {
		t.Errorf("message %q does not name the verb that owns the key", setErr.Message)
	}
	if got := readConfig(t, path); got != "active_persona: veronica\n" {
		t.Errorf("a refused unset rewrote the file:\n%s", got)
	}
}

// Removing a key the file does not hold is not a failure: a front end's
// "reset to default" must be idempotent.
func TestUnsetKeyFileAbsentKeyIsANoOp(t *testing.T) {
	path := newInstall(t, fourSpaceConfig)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)

	result, err := UnsetKeyFile("agent_name")
	if err != nil {
		t.Fatalf("UnsetKeyFile on an absent key: %v", err)
	}
	if result.Changed {
		t.Error("changed = true for a key the file never held")
	}
	if result.OldValue != "Samantha" || result.Value != "Samantha" {
		t.Errorf("old/new = %v/%v, want the schema default both times", result.OldValue, result.Value)
	}
	if result.BackupPath != "" {
		t.Errorf("backup written for a no-op: %s", result.BackupPath)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("the file was rewritten for a no-op unset")
	}
	if got := readConfig(t, path); got != fourSpaceConfig {
		t.Errorf("a no-op unset changed the file:\n%s", got)
	}
}

// --- the write path ---

func TestUnsetKeyFileRemovesOnlyItsOwnLines(t *testing.T) {
	tests := []struct {
		name string
		key  string
		gone string
	}{
		{name: "a top-level key", key: "vad_silence_duration", gone: "vad_silence_duration: 0.5\n"},
		{name: "a key with an inline comment", key: "brain_provider", gone: "brain_provider: \"ollama\"   # quoted on purpose\n"},
		{name: "a nested key in a 4-space block", key: "speaker.live.window_ms", gone: "        window_ms: 1500\n"},
		{name: "a deep key under an untouched section", key: "meeting.route.mode", gone: "        mode: auto\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := newInstall(t, fourSpaceConfig)

			result, err := UnsetKeyFile(tt.key)
			if err != nil {
				t.Fatalf("UnsetKeyFile(%s): %v", tt.key, err)
			}
			if !result.Changed {
				t.Error("changed = false for a key the file held")
			}
			if result.BackupPath == "" {
				t.Error("no backup kept for a real removal")
			}
			want := strings.Replace(fourSpaceConfig, tt.gone, "", 1)
			if want == fourSpaceConfig {
				t.Fatalf("test fixture does not contain %q", tt.gone)
			}
			if got := readConfig(t, path); got != want {
				t.Errorf("unset changed more than its own lines:\n%s", diffReport(want, got))
			}
		})
	}
}

// The point of removing rather than blanking: the key stops being in the file,
// so `config get` reports the default as the source and a future default (or an
// env binding) is free to take effect.
// The in-process half of the same question, and the one that was broken:
// SetKeyFile installs a viper override, viper cannot remove one, and a later
// ReadInConfig does not outrank it — so a process that set a key and then unset
// it kept serving the deleted value while Source correctly reported "default".
// A long-lived front end (the TUI, the Mac agent, serve) is exactly where that
// bites. Found by adversarial review; the original test never called SetKeyFile
// first, so it could not see it.
func TestUnsetKeyFileClearsAValueThisProcessJustWrote(t *testing.T) {
	newInstall(t, "tts_provider: kokoro\n")

	if _, err := SetKeyFile("stt_mode", "streaming"); err != nil {
		t.Fatalf("SetKeyFile: %v", err)
	}
	if got := Get("stt_mode"); got != "streaming" {
		t.Fatalf("Get after set = %v, want streaming", got)
	}

	result, err := UnsetKeyFile("stt_mode")
	if err != nil {
		t.Fatalf("UnsetKeyFile: %v", err)
	}
	if result.Value != "" {
		t.Errorf("value = %v, want the empty default — not the value just removed", result.Value)
	}
	if got := Get("stt_mode"); got != "" {
		t.Errorf("Get after unset = %v, want the empty default (a stale viper override)", got)
	}
	if got := Source("stt_mode"); got != SourceDefault {
		t.Errorf("source = %q, want %q", got, SourceDefault)
	}
	cfg, err := LoadRaw()
	if err != nil {
		t.Fatalf("LoadRaw: %v", err)
	}
	if cfg.STTMode != "" {
		t.Errorf("stt_mode = %q after unset, want empty", cfg.STTMode)
	}
}

func TestUnsetKeyFileRestoresTheDefault(t *testing.T) {
	newInstall(t, "vad_silence_duration: 0.9\nstt_mode: streaming\n")

	result, err := UnsetKeyFile("vad_silence_duration")
	if err != nil {
		t.Fatalf("UnsetKeyFile: %v", err)
	}
	if result.OldValue != 0.9 {
		t.Errorf("old_value = %v, want the value the file held", result.OldValue)
	}
	if Source("vad_silence_duration") != SourceDefault {
		t.Errorf("source = %q after unset, want %q", Source("vad_silence_duration"), SourceDefault)
	}
	cfg, err := LoadRaw()
	if err != nil {
		t.Fatalf("LoadRaw: %v", err)
	}
	if cfg.VADSilenceDuration != 0.5 {
		t.Errorf("vad_silence_duration = %v after unset, want the 0.5 default", cfg.VADSilenceDuration)
	}
	if cfg.STTMode != "streaming" {
		t.Errorf("stt_mode = %q, want the untouched neighbour's value", cfg.STTMode)
	}
}

// --- M7's other half: "" is a value for the keys whose default is "" ---

func TestSetKeyFileEmptyValueFollowsAllowsEmpty(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{name: "an enum whose default is kokoro refuses empty", key: "tts_provider", wantErr: true},
		{name: "an enum whose default is ollama refuses empty", key: "brain_provider", wantErr: true},
		{name: "stt_mode clears back to its default", key: "stt_mode"},
		{name: "qwen_tts_mode clears back to its default", key: "qwen_tts_mode"},
		{name: "meeting.route.default clears back to its default", key: "meeting.route.default"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newInstall(t, "tts_provider: qwen3-tts\nbrain_provider: claude\nstt_mode: streaming\nqwen_tts_mode: customvoice\nmeeting:\n  route:\n    default: obsidian\n")

			result, err := SetKeyFile(tt.key, "")
			if tt.wantErr {
				if err == nil {
					t.Fatalf("SetKeyFile(%s, \"\") succeeded, want it refused", tt.key)
				}
				if code := setError(t, err).Code; code != CodeInvalidValue {
					t.Fatalf("code = %q, want %q", code, CodeInvalidValue)
				}
				return
			}
			if err != nil {
				t.Fatalf("SetKeyFile(%s, \"\"): %v", tt.key, err)
			}
			if !result.Changed || result.Value != "" {
				t.Fatalf("result = %+v, want an applied empty value", result)
			}
			value, _, ok := ValueFor(tt.key)
			if !ok || value.Value != "" {
				t.Errorf("%s = %v after clearing, want the empty value", tt.key, value.Value)
			}
		})
	}
}

// allows_empty must answer what the writer actually does. The first version of
// this test re-derived its expectation with the same expression production used
// ("the default is the empty string"), so it agreed with the code by
// construction and could not see that `config set agent_name ""` succeeded
// while the schema said it would not. This one drives the coercer instead —
// the flag is wrong if it disagrees with a real write — and pins a fixed table
// so the intent survives a refactor of the derivation.
func TestSchemaAllowsEmptyMatchesWhatTheWriterAccepts(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{key: "tts_provider"},              // enum, default kokoro
		{key: "brain_provider"},            // enum, default ollama
		{key: "calibre_prefer_format"},     // enum, default epub
		{key: "vad_silence_duration"},      // float
		{key: "vad_pre_roll_ms"},           // int
		{key: "barge_in_enabled"},          // bool
		{key: "skills_disabled"},           // list
		{key: "stt_mode", want: true},      // enum whose unset state is ""
		{key: "qwen_tts_mode", want: true}, // enum whose unset state is ""
		{key: "meeting.route.default", want: true},
		{key: "agent_name", want: true},    // text: "" is a value, not the default
		{key: "output_device", want: true}, // text
		{key: "ollama_host", want: true},   // text
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			spec, ok := SpecFor(tt.key)
			if !ok {
				t.Fatalf("no such key %q", tt.key)
			}
			if spec.AllowsEmpty != tt.want {
				t.Errorf("allows_empty = %v, want %v", spec.AllowsEmpty, tt.want)
			}
		})
	}

	// And across the whole schema: the flag and the coercer must never
	// disagree, whatever new keys arrive.
	for _, spec := range Schema() {
		if !spec.Editable {
			continue // `config set` refuses these before any value is read
		}
		_, err := coerceToSpec(spec, "")
		if accepted := err == nil; accepted != spec.AllowsEmpty {
			t.Errorf("%s: allows_empty = %v but an empty write is accepted = %v",
				spec.Key, spec.AllowsEmpty, accepted)
		}
	}
}

// `value` in the shared set/unset payload means "what this key holds now". For
// a key with an environment binding that is the exported value, not the schema
// default — reporting the default there would tell a front end the agent is
// about to use something it is not.
func TestUnsetKeyFileReportsTheEffectiveValueNotJustTheDefault(t *testing.T) {
	t.Run("no environment binding set: the schema default", func(t *testing.T) {
		newInstall(t, "stt_mode: cli\n")
		result, err := UnsetKeyFile("stt_mode")
		if err != nil {
			t.Fatalf("UnsetKeyFile: %v", err)
		}
		if result.Value != "" {
			t.Errorf("value = %v, want the empty default", result.Value)
		}
	})

	t.Run("an exported binding wins", func(t *testing.T) {
		t.Setenv("STT_MODE", "streaming")
		newInstall(t, "stt_mode: cli\n")

		result, err := UnsetKeyFile("stt_mode")
		if err != nil {
			t.Fatalf("UnsetKeyFile: %v", err)
		}
		if result.OldValue != "cli" {
			t.Errorf("old_value = %v, want what the file held", result.OldValue)
		}
		if result.Value != "streaming" {
			t.Errorf("value = %v, want the exported STT_MODE the key now resolves to", result.Value)
		}
		if got := Source("stt_mode"); got != SourceEnv {
			t.Errorf("source = %q, want %q", got, SourceEnv)
		}
	})
}

// A flow document lives on one line, so removing a key from it by line would
// take the whole document with it — and `verifyUnset` could not object, because
// the key really was gone. `config unset agent_name` on `{agent_name: Sam,
// vad_silence_duration: 0.5}` left a 0-byte file. Found by adversarial review;
// the write path had this guard and the delete path did not.
func TestUnsetKeyFileRefusesAFlowDocument(t *testing.T) {
	const flowDoc = "{agent_name: Sam, vad_silence_duration: 0.5}\n"
	path := newInstall(t, flowDoc)

	_, err := UnsetKeyFile("agent_name")
	setErr := setError(t, err)
	if setErr.Code != CodeParseFailed {
		t.Fatalf("code = %q, want %q", setErr.Code, CodeParseFailed)
	}
	if !strings.Contains(setErr.Message, "flow style") {
		t.Errorf("message %q does not name the problem", setErr.Message)
	}
	if got := readConfig(t, path); got != flowDoc {
		t.Fatalf("the document was destroyed by a refused unset: %q", got)
	}
}

// The two remaining paths that replaced the whole config file. Both are how a
// TUI or Mac-app user changes a setting, so M1's defect was still reachable
// from a front end after `config set` was fixed. Found by adversarial review.
func TestTheOtherWritersAreSurgicalToo(t *testing.T) {
	const handEdited = `# Samantha settings — hand edited.
brain_provider: claude

# Voice activity detection.
vad_silence_duration: 0.5   # snappier

speaker:
    live:
        window_ms: 1500
`

	t.Run("SetAndSaveBrainProvider keeps the file it was given", func(t *testing.T) {
		path := newInstall(t, handEdited)
		cfg, err := LoadRaw()
		if err != nil {
			t.Fatalf("LoadRaw: %v", err)
		}

		if err := SetAndSaveBrainProvider(cfg, "ollama"); err != nil {
			t.Fatalf("SetAndSaveBrainProvider: %v", err)
		}

		got := readConfig(t, path)
		for _, want := range []string{
			"# Samantha settings — hand edited.",
			"# Voice activity detection.",
			"vad_silence_duration: 0.5   # snappier",
			"        window_ms: 1500",
			"brain_provider: ollama",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("lost %q from the file:\n%s", want, got)
			}
		}
		if strings.Contains(got, "agent_name:") {
			t.Errorf("a built-in default was baked into the file:\n%s", got)
		}
		if cfg.BrainProvider != "ollama" {
			t.Errorf("cfg.BrainProvider = %q, want ollama", cfg.BrainProvider)
		}
	})

	t.Run("config migrate --write keeps the file it was given", func(t *testing.T) {
		const legacy = `# Samantha settings — hand edited.
stt_provider: sherpa-streaming

speaker:
    live:
        window_ms: 1500
`
		path := newInstall(t, legacy)
		cfg, err := LoadRaw()
		if err != nil {
			t.Fatalf("LoadRaw: %v", err)
		}

		result, err := WriteSTTConfigMigration(cfg, path)
		if err != nil {
			t.Fatalf("WriteSTTConfigMigration: %v", err)
		}
		if !result.Wrote {
			t.Fatal("migration reported no write; the fixture should need one")
		}

		// stt_provider is edited where it sits; stt_mode is not in the file, so
		// it joins the document at the end — the same rule any new top-level
		// key follows. Everything else is byte-identical.
		want := strings.Replace(legacy, "stt_provider: sherpa-streaming", "stt_provider: sherpa", 1) +
			"stt_mode: streaming\n"
		if got := readConfig(t, path); got != want {
			t.Fatalf("the migration rewrote lines it was not asked to:\n%s", diffReport(want, got))
		}
	})
}
