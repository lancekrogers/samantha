package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const commentedConfig = `# Samantha settings — hand-edited, comments matter.
tts_provider: kokoro

# Voice activity detection.
vad_silence_duration: 0.5 # snappier than the old 0.8
vad_pre_roll_ms: 300
barge_in_enabled: false

speaker:
  # Live labels during a conversation.
  live:
    window_ms: 1500
`

// newInstall points the package at a throwaway install root seeded with body.
func newInstall(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	SetConfigDirForTest(t, dir)
	path := filepath.Join(dir, "config.yaml")
	if body != "" {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("seeding config: %v", err)
		}
	}
	resetViper(t)
	return path
}

func setError(t *testing.T, err error) *SetError {
	t.Helper()
	var setErr *SetError
	if !errors.As(err, &setErr) {
		t.Fatalf("error %v is not a *SetError", err)
	}
	return setErr
}

func mustSet(t *testing.T, key, raw string) SetResult {
	t.Helper()
	result, err := SetKeyFile(key, raw)
	if err != nil {
		t.Fatalf("SetKeyFile(%s, %s): %v", key, raw, err)
	}
	return result
}

func readConfig(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	return string(data)
}

func TestSetKeyFileRejectsUnknownKeyWithSuggestions(t *testing.T) {
	path := newInstall(t, commentedConfig)
	before := readConfig(t, path)

	_, err := SetKeyFile("vad_silence", "0.5")
	if err == nil {
		t.Fatal("expected an error for an unknown key")
	}
	setErr := setError(t, err)
	if setErr.Code != CodeUnknownKey {
		t.Errorf("code = %q, want %q", setErr.Code, CodeUnknownKey)
	}
	var found bool
	for _, suggestion := range setErr.DidYouMean {
		if suggestion == "vad_silence_duration" {
			found = true
		}
	}
	if !found {
		t.Errorf("did_you_mean = %v, want it to contain vad_silence_duration", setErr.DidYouMean)
	}
	if len(setErr.DidYouMean) > 5 {
		t.Errorf("did_you_mean returned %d suggestions, want at most 5", len(setErr.DidYouMean))
	}
	if readConfig(t, path) != before {
		t.Error("config changed despite an unknown key")
	}
}

func TestSetKeyFileRejectsNotEditableKey(t *testing.T) {
	path := newInstall(t, commentedConfig)
	before := readConfig(t, path)

	_, err := SetKeyFile("meeting.route.destinations", "[]")
	setErr := setError(t, err)
	if setErr.Code != CodeNotEditable {
		t.Fatalf("code = %q, want %q", setErr.Code, CodeNotEditable)
	}
	if !strings.Contains(setErr.Message, "meeting destinations") {
		t.Errorf("message %q does not name the owning verb", setErr.Message)
	}
	if readConfig(t, path) != before {
		t.Error("config changed despite a non-editable key")
	}
}

func TestSetKeyFileEnumRejectsUnknownValue(t *testing.T) {
	newInstall(t, commentedConfig)

	_, err := SetKeyFile("tts_provider", "festival")
	setErr := setError(t, err)
	if setErr.Code != CodeInvalidValue {
		t.Fatalf("code = %q, want %q", setErr.Code, CodeInvalidValue)
	}
	for _, accepted := range []string{"kokoro", "qwen3-tts"} {
		if !strings.Contains(setErr.Message, accepted) {
			t.Errorf("message %q does not list %q", setErr.Message, accepted)
		}
	}

	// Case-insensitive in, canonical casing out.
	result := mustSet(t, "tts_provider", "QWEN3-TTS")
	if result.Value != "qwen3-tts" {
		t.Errorf("value = %v, want qwen3-tts", result.Value)
	}
}

func TestSetKeyFileRejectsOutOfRangeValues(t *testing.T) {
	newInstall(t, commentedConfig)

	tests := []struct {
		name string
		key  string
		raw  string
		want string
	}{
		{"float above max", "vad_silence_duration", "9", "between 0.1 and 3"},
		{"float below min", "vad_silence_duration", "0.01", "between 0.1 and 3"},
		{"float unparseable", "vad_silence_duration", "fast", `got "fast"`},
		{"int above max", "vad_pre_roll_ms", "9000", "between 0 and 2000"},
		{"int unparseable", "vad_pre_roll_ms", "1.5", "whole number"},
		{"bool unparseable", "barge_in_enabled", "sometimes", "true or false"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := SetKeyFile(tt.key, tt.raw)
			setErr := setError(t, err)
			if setErr.Code != CodeInvalidValue {
				t.Fatalf("code = %q, want %q", setErr.Code, CodeInvalidValue)
			}
			if !strings.Contains(setErr.Message, tt.want) {
				t.Errorf("message = %q, want it to contain %q", setErr.Message, tt.want)
			}
		})
	}
}

func TestSetKeyFilePreservesCommentsAndOrder(t *testing.T) {
	path := newInstall(t, commentedConfig)

	mustSet(t, "vad_silence_duration", "0.8")
	mustSet(t, "tts_provider", "qwen3-tts")
	mustSet(t, "speaker.live.window_ms", "2000")

	got := readConfig(t, path)
	for _, comment := range []string{
		"# Samantha settings — hand-edited, comments matter.",
		"# Voice activity detection.",
		"# snappier than the old 0.8",
		"# Live labels during a conversation.",
	} {
		if !strings.Contains(got, comment) {
			t.Errorf("comment lost after three writes: %s\n---\n%s", comment, got)
		}
	}

	wantOrder := []string{"tts_provider", "vad_silence_duration", "vad_pre_roll_ms", "barge_in_enabled", "speaker:"}
	last := -1
	for _, key := range wantOrder {
		at := strings.Index(got, key)
		if at < 0 {
			t.Fatalf("key %q missing from:\n%s", key, got)
		}
		if at < last {
			t.Errorf("key order changed around %q:\n%s", key, got)
		}
		last = at
	}

	// Only the three touched lines differ from the seed.
	changed := diffLines(commentedConfig, got)
	if len(changed) != 3 {
		t.Errorf("changed lines = %v, want exactly the three written keys", changed)
	}
}

func diffLines(before, after string) []string {
	beforeLines := strings.Split(before, "\n")
	afterLines := strings.Split(after, "\n")
	seen := map[string]bool{}
	for _, line := range beforeLines {
		seen[line] = true
	}
	var changed []string
	for _, line := range afterLines {
		if strings.TrimSpace(line) != "" && !seen[line] {
			changed = append(changed, strings.TrimSpace(line))
		}
	}
	return changed
}

func TestSetKeyFileWritesTypedScalars(t *testing.T) {
	path := newInstall(t, commentedConfig)

	mustSet(t, "barge_in_enabled", "true")
	mustSet(t, "vad_pre_roll_ms", "250")
	mustSet(t, "speech_speed", "1.1")
	mustSet(t, "tts_voice", "am_michael")

	got := readConfig(t, path)
	for _, want := range []string{
		"barge_in_enabled: true",
		"vad_pre_roll_ms: 250",
		"speech_speed: 1.1",
		"tts_voice: am_michael",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}

	cfg, err := LoadRaw()
	if err != nil {
		t.Fatalf("LoadRaw after typed writes: %v", err)
	}
	if !cfg.BargeInEnabled {
		t.Error("barge_in_enabled did not round-trip as a bool")
	}
	if cfg.VADPreRollMS != 250 {
		t.Errorf("vad_pre_roll_ms = %d, want 250", cfg.VADPreRollMS)
	}
	if cfg.SpeechSpeed != 1.1 {
		t.Errorf("speech_speed = %v, want 1.1", cfg.SpeechSpeed)
	}
}

func TestSetKeyFileWholeNumberedFloatStaysFloat(t *testing.T) {
	path := newInstall(t, commentedConfig)

	mustSet(t, "speech_speed", "1")
	if got := readConfig(t, path); !strings.Contains(got, "speech_speed: 1.0") {
		t.Errorf("whole float written as an int:\n%s", got)
	}
	cfg, err := LoadRaw()
	if err != nil {
		t.Fatalf("LoadRaw: %v", err)
	}
	if cfg.SpeechSpeed != 1 {
		t.Errorf("speech_speed = %v, want 1", cfg.SpeechSpeed)
	}
}

func TestSetKeyFileCreatesNestedPaths(t *testing.T) {
	t.Run("no speaker block", func(t *testing.T) {
		path := newInstall(t, "tts_provider: kokoro\n")
		mustSet(t, "speaker.live.window_ms", "2000")

		cfg, err := LoadRaw()
		if err != nil {
			t.Fatalf("LoadRaw: %v", err)
		}
		if cfg.Speaker.Live.WindowMS != 2000 {
			t.Fatalf("speaker.live.window_ms = %d, want 2000\n%s", cfg.Speaker.Live.WindowMS, readConfig(t, path))
		}
	})

	t.Run("existing speaker block edits in place", func(t *testing.T) {
		path := newInstall(t, commentedConfig)
		mustSet(t, "speaker.live.window_ms", "2000")
		mustSet(t, "speaker.live.threshold", "0.7")

		got := readConfig(t, path)
		if strings.Count(got, "speaker:") != 1 {
			t.Errorf("speaker section duplicated:\n%s", got)
		}
		if strings.Count(got, "live:") != 1 {
			t.Errorf("live section duplicated:\n%s", got)
		}
		cfg, err := LoadRaw()
		if err != nil {
			t.Fatalf("LoadRaw: %v", err)
		}
		if cfg.Speaker.Live.WindowMS != 2000 || cfg.Speaker.Live.Threshold != 0.7 {
			t.Errorf("nested values = %d/%v, want 2000/0.7", cfg.Speaker.Live.WindowMS, cfg.Speaker.Live.Threshold)
		}
	})

	t.Run("scalar in the way is an error, not a silent overwrite", func(t *testing.T) {
		newInstall(t, "speaker: yes\n")
		_, err := SetKeyFile("speaker.live.window_ms", "2000")
		setErr := setError(t, err)
		if setErr.Code != CodeWriteFailed {
			t.Fatalf("code = %q, want %q", setErr.Code, CodeWriteFailed)
		}
	})
}

func TestSetKeyFileListValue(t *testing.T) {
	path := newInstall(t, commentedConfig)

	result := mustSet(t, "skills_disabled", `["pdf-fill","calibre"]`)
	if !result.Changed {
		t.Error("changed = false for a new list value")
	}
	got := readConfig(t, path)
	if !strings.Contains(got, "skills_disabled:") || !strings.Contains(got, "- pdf-fill") {
		t.Errorf("list not written as a YAML sequence:\n%s", got)
	}
	cfg, err := LoadRaw()
	if err != nil {
		t.Fatalf("LoadRaw: %v", err)
	}
	if strings.Join(cfg.SkillsDisabled, ",") != "pdf-fill,calibre" {
		t.Errorf("skills_disabled = %v, want [pdf-fill calibre]", cfg.SkillsDisabled)
	}

	_, err = SetKeyFile("skills_disabled", "pdf-fill")
	setErr := setError(t, err)
	if setErr.Code != CodeInvalidValue {
		t.Fatalf("code = %q, want %q", setErr.Code, CodeInvalidValue)
	}
	if !strings.Contains(setErr.Message, "JSON array") {
		t.Errorf("message %q does not show the JSON form", setErr.Message)
	}

	if _, err := SetKeyFile("skills_disabled", "[]"); err != nil {
		t.Fatalf("empty list rejected: %v", err)
	}
}

func TestSetKeyFileNoopDoesNotWriteOrBackup(t *testing.T) {
	path := newInstall(t, commentedConfig)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	result, err := SetKeyFile("vad_pre_roll_ms", "300")
	if err != nil {
		t.Fatalf("SetKeyFile: %v", err)
	}
	if result.Changed {
		t.Error("changed = true for a value already in the file")
	}
	if result.BackupPath != "" {
		t.Errorf("backup %q taken for a no-op write", result.BackupPath)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("config rewritten for a no-op set")
	}
	if backups := backupsFor(t, path); len(backups) != 0 {
		t.Errorf("backups = %v, want none", backups)
	}

	// A key absent from the file whose new value equals the default is also a
	// no-op: the effective value does not change.
	result, err = SetKeyFile("max_history", "10")
	if err != nil {
		t.Fatalf("SetKeyFile: %v", err)
	}
	if result.Changed {
		t.Error("changed = true for a write matching the default")
	}
}

func backupsFor(t *testing.T, path string) []string {
	t.Helper()
	matches, err := filepath.Glob(path + ".bak.*")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	return matches
}

func TestSetKeyFileAtomicAndBacked(t *testing.T) {
	path := newInstall(t, commentedConfig)

	for i, value := range []string{"0.6", "0.7", "0.8", "0.9", "1.0", "1.1"} {
		result := mustSet(t, "vad_silence_duration", value)
		if !result.Changed {
			t.Fatalf("write %d reported no change", i)
		}
		if result.BackupPath == "" {
			t.Fatalf("write %d took no backup", i)
		}
		if _, err := os.Stat(result.BackupPath); err != nil {
			t.Fatalf("backup %q missing: %v", result.BackupPath, err)
		}
	}

	if backups := backupsFor(t, path); len(backups) != keptBackups {
		t.Errorf("backups = %d, want %d after six writes", len(backups), keptBackups)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config mode = %v, want 0600", perm)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp.") {
			t.Errorf("temp file left behind: %s", entry.Name())
		}
		if strings.HasSuffix(entry.Name(), ".lock") {
			t.Errorf("lock file left behind: %s", entry.Name())
		}
	}
}

func TestSetKeyFileLockRejectsConcurrentWriter(t *testing.T) {
	path := newInstall(t, commentedConfig)
	before := readConfig(t, path)

	lockPath := path + ".lock"
	if err := os.WriteFile(lockPath, []byte("999999\n"), 0o600); err != nil {
		t.Fatalf("seeding lock: %v", err)
	}
	defer os.Remove(lockPath)

	_, err := SetKeyFile("vad_silence_duration", "0.8")
	setErr := setError(t, err)
	if setErr.Code != CodeLocked {
		t.Fatalf("code = %q, want %q", setErr.Code, CodeLocked)
	}
	if readConfig(t, path) != before {
		t.Error("config changed while another writer held the lock")
	}
}

func TestSetKeyFileBreaksAStaleLock(t *testing.T) {
	path := newInstall(t, commentedConfig)
	lockPath := path + ".lock"
	if err := os.WriteFile(lockPath, []byte("999999\n"), 0o600); err != nil {
		t.Fatalf("seeding lock: %v", err)
	}
	stale := time.Now().Add(-2 * configLockStale)
	if err := os.Chtimes(lockPath, stale, stale); err != nil {
		t.Fatalf("aging lock: %v", err)
	}

	if _, err := SetKeyFile("vad_silence_duration", "0.8"); err != nil {
		t.Fatalf("a lock abandoned by a crashed writer must not block writes: %v", err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Error("lock file left behind after a successful write")
	}
}

func TestSetKeyFileReportsOldValueAndRestart(t *testing.T) {
	newInstall(t, commentedConfig)

	result := mustSet(t, "vad_silence_duration", "0.8")
	if result.OldValue != 0.5 {
		t.Errorf("old_value = %v, want 0.5", result.OldValue)
	}
	if result.Value != 0.8 {
		t.Errorf("value = %v, want 0.8", result.Value)
	}
	if result.Type != TypeFloat {
		t.Errorf("type = %v, want float", result.Type)
	}
	if !result.RestartRequired {
		t.Error("restart_required = false for an unverified key")
	}

	// A key absent from the file reports the schema default as old_value.
	result = mustSet(t, "max_history", "20")
	if result.OldValue != 10 {
		t.Errorf("old_value = %v, want the default 10", result.OldValue)
	}

	// tts_provider is the one voice key proven not to need a restart.
	result = mustSet(t, "tts_provider", "qwen3-tts")
	if result.RestartRequired {
		t.Error("restart_required = true for tts_provider")
	}
}

func TestSetKeyFileRefreshesInProcessValue(t *testing.T) {
	newInstall(t, commentedConfig)

	mustSet(t, "vad_silence_duration", "0.8")
	if got := Get("vad_silence_duration"); got != 0.8 {
		t.Errorf("Get after set = %v, want 0.8 — the file and the process disagree", got)
	}
}

func TestSetKeyFileCreatesAMissingConfigFile(t *testing.T) {
	path := newInstall(t, "")

	result := mustSet(t, "vad_silence_duration", "0.8")
	if result.BackupPath != "" {
		t.Errorf("backup %q taken when there was no file to back up", result.BackupPath)
	}
	if !strings.Contains(readConfig(t, path), "vad_silence_duration: 0.8") {
		t.Errorf("value missing from the new file:\n%s", readConfig(t, path))
	}
}

func TestSetKeyFileValueAcceptsTypedCallers(t *testing.T) {
	path := newInstall(t, commentedConfig)

	if _, err := SetKeyFileValue("barge_in_enabled", true); err != nil {
		t.Fatalf("bool: %v", err)
	}
	if _, err := SetKeyFileValue("vad_pre_roll_ms", 250); err != nil {
		t.Fatalf("int: %v", err)
	}
	if _, err := SetKeyFileValue("speaker.threshold", float32(0.75)); err != nil {
		t.Fatalf("float32: %v", err)
	}
	if _, err := SetKeyFileValue("skills_disabled", []string{"calibre"}); err != nil {
		t.Fatalf("list: %v", err)
	}

	got := readConfig(t, path)
	for _, want := range []string{"barge_in_enabled: true", "vad_pre_roll_ms: 250", "threshold: 0.75", "- calibre"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}

	if _, err := SetKeyFileValue("vad_pre_roll_ms", 1.5); err == nil {
		t.Error("a fractional value was accepted for an int key")
	}
}

func TestSetAndSaveWritesSurgically(t *testing.T) {
	path := newInstall(t, commentedConfig)

	if err := SetAndSave("barge_in_enabled", true); err != nil {
		t.Fatalf("SetAndSave: %v", err)
	}
	got := readConfig(t, path)
	if !strings.Contains(got, "# Voice activity detection.") {
		t.Errorf("the TUI's writer still rewrites the whole file:\n%s", got)
	}
	if !strings.Contains(got, "barge_in_enabled: true") {
		t.Errorf("value not written:\n%s", got)
	}
}

func TestSetKeyFileChangesExactlyOneLine(t *testing.T) {
	// Manual verification step 4 as a test: after a write, the file differs from
	// the original by exactly the line that changed — blank lines, comments,
	// inline comments and key order all survive byte for byte.
	path := newInstall(t, commentedConfig)
	mustSet(t, "vad_silence_duration", "0.8")

	before := strings.Split(commentedConfig, "\n")
	after := strings.Split(readConfig(t, path), "\n")
	if len(before) != len(after) {
		t.Fatalf("line count changed: %d -> %d\n%s", len(before), len(after), readConfig(t, path))
	}
	var differing []int
	for i := range before {
		if before[i] != after[i] {
			differing = append(differing, i)
		}
	}
	if len(differing) != 1 {
		t.Fatalf("changed lines %v, want exactly one\n---\n%s", differing, readConfig(t, path))
	}
	if want := "vad_silence_duration: 0.8 # snappier than the old 0.8"; after[differing[0]] != want {
		t.Errorf("changed line = %q, want %q", after[differing[0]], want)
	}
}

func TestSetKeyFileFillsAnEmptySection(t *testing.T) {
	// `speaker:` with nothing under it parses as null. Writing into it must
	// fill the section, not refuse and not replace the user's line.
	newInstall(t, "tts_provider: kokoro\nspeaker:\n")

	if _, err := SetKeyFile("speaker.live.window_ms", "2000"); err != nil {
		t.Fatalf("writing into an empty section: %v", err)
	}
	cfg, err := LoadRaw()
	if err != nil {
		t.Fatalf("LoadRaw: %v", err)
	}
	if cfg.Speaker.Live.WindowMS != 2000 {
		t.Errorf("speaker.live.window_ms = %d, want 2000", cfg.Speaker.Live.WindowMS)
	}
}
