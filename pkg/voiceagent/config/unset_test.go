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

// allows_empty is derived, not listed: it is exactly the keys whose schema
// default is the empty string. A second hand-maintained list is how a front end
// ends up offering "(App default)" for a key that refuses it.
func TestSchemaAllowsEmptyMatchesTheDefault(t *testing.T) {
	var sawTrue, sawFalse bool
	for _, spec := range Schema() {
		text, isString := spec.Default.(string)
		want := isString && text == ""
		if spec.AllowsEmpty != want {
			t.Errorf("%s: allows_empty = %v, want %v (default %#v)", spec.Key, spec.AllowsEmpty, want, spec.Default)
		}
		if want {
			sawTrue = true
		} else {
			sawFalse = true
		}
	}
	if !sawTrue || !sawFalse {
		t.Errorf("the schema no longer covers both cases (saw true: %v, saw false: %v)", sawTrue, sawFalse)
	}
	spec, ok := SpecFor("stt_mode")
	if !ok || !spec.AllowsEmpty {
		t.Error("stt_mode must be allows_empty — it is the key the acceptance run could not clear")
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
