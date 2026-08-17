package speaker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lancekrogers/samantha/pkg/voiceagent/audio"
)

func TestProfileSlugRejectsInvalidNames(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"blank", "   "},
		{"reserved unknown", "unknown"},
		{"reserved unknown cased", "Unknown"},
		{"reserved anonymous", "speaker-3"},
		{"reserved anonymous cased", "Speaker-1"},
		{"path separator", "a/b"},
		{"parent traversal", ".."},
		{"leading dot", ".hidden"},
		{"leading dash", "-x"},
		{"too long", string(make([]byte, 70))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if slug, err := profileSlug(tc.in); err == nil {
				t.Fatalf("profileSlug(%q) = %q, want error", tc.in, slug)
			}
		})
	}
}

func TestProfileSlugNormalizes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Lance", "lance"},
		{"Lance R", "lance-r"},
		{"guest_2", "guest_2"},
		{" padded ", "padded"},
	}
	for _, tc := range cases {
		got, err := profileSlug(tc.in)
		if err != nil {
			t.Fatalf("profileSlug(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("profileSlug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEnrollmentAddErrorsFirst(t *testing.T) {
	store, err := OpenEnrollment(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if _, err := store.Add(ctx, "lance", "rev", nil); err == nil {
		t.Fatal("no embeddings must fail")
	}
	if _, err := store.Add(ctx, "lance", "rev", [][]float32{{}}); err == nil {
		t.Fatal("empty embedding must fail")
	}
	if _, err := store.Add(ctx, "lance", "rev", [][]float32{{1, 2}, {1}}); err == nil {
		t.Fatal("dim mismatch must fail")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Add(canceled, "lance", "rev", [][]float32{{1, 2}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ctx: got %v, want context.Canceled", err)
	}
	if len(store.List()) != 0 {
		t.Fatalf("failed adds must not persist, have %v", store.List())
	}
}

func TestEnrollmentRoundTripAndReplace(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenEnrollment(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	first := [][]float32{{1, 2, 3}, {4, 5, 6}}
	p, err := store.Add(ctx, "Lance", "titanet-small", first)
	if err != nil {
		t.Fatal(err)
	}
	if p.Dim != 3 || p.Samples != 2 || p.Name != "Lance" {
		t.Fatalf("profile = %+v", p)
	}

	// A fresh open must see the same profile and identical embeddings.
	reopened, err := OpenEnrollment(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Embeddings("lance") // case-insensitive lookup
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, first) {
		t.Fatalf("embeddings = %v, want %v", got, first)
	}

	// Re-enrolling the same name (any case) replaces in place.
	second := [][]float32{{9, 9, 9, 9}}
	p2, err := reopened.Add(ctx, "LANCE", "titanet-small", second)
	if err != nil {
		t.Fatal(err)
	}
	if !p2.CreatedAt.Equal(p.CreatedAt) {
		t.Fatalf("replace must keep CreatedAt: %v vs %v", p2.CreatedAt, p.CreatedAt)
	}
	if p2.Samples != 1 || p2.Dim != 4 {
		t.Fatalf("replaced profile = %+v", p2)
	}
	if n := len(reopened.List()); n != 1 {
		t.Fatalf("replace created a second profile: %d", n)
	}
}

func TestEnrollmentRejectsSlugCollision(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenEnrollment(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	first := [][]float32{{1, 2, 3}}
	if _, err := store.Add(ctx, "Mary Jane", "rev", first); err != nil {
		t.Fatal(err)
	}
	// Distinct name, same storage key — must be rejected, not overwrite.
	if _, err := store.Add(ctx, "Mary-Jane", "rev", [][]float32{{9, 9, 9}}); err == nil {
		t.Fatal("colliding name overwrote existing profile")
	}
	got, err := store.Embeddings("Mary Jane")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, first) {
		t.Fatalf("original embeddings clobbered: %v", got)
	}
	profiles := store.List()
	if len(profiles) != 1 || profiles[0].Name != "Mary Jane" {
		t.Fatalf("profiles = %+v", profiles)
	}
	// Case variants of the SAME name still re-enroll in place.
	if _, err := store.Add(ctx, "mary jane", "rev", [][]float32{{7, 7, 7}}); err != nil {
		t.Fatalf("case-variant re-enroll rejected: %v", err)
	}
}

func TestEnrollmentRemoveDeletesEmbeddingFile(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenEnrollment(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Remove("ghost"); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("remove unknown: %v, want ErrNotEnrolled", err)
	}

	if _, err := store.Add(context.Background(), "guest", "rev", [][]float32{{1}}); err != nil {
		t.Fatal(err)
	}
	embPath := filepath.Join(dir, embeddingsDirName, "guest.f32")
	if _, err := os.Stat(embPath); err != nil {
		t.Fatalf("embedding file missing after add: %v", err)
	}
	if err := store.Remove("guest"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(embPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("embedding file must be deleted from disk, stat: %v", err)
	}
	if len(store.List()) != 0 {
		t.Fatal("profile still listed after remove")
	}
	reopened, err := OpenEnrollment(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.List()) != 0 {
		t.Fatal("removed profile survived reopen")
	}
}

func TestEnrollmentCorruptEmbeddingFile(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenEnrollment(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(context.Background(), "guest", "rev", [][]float32{{1, 2}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, embeddingsDirName, "guest.f32"), []byte{1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Embeddings("guest"); err == nil {
		t.Fatal("truncated embeddings file must error, not return garbage")
	}
}

func TestEnrollmentRenameUnknownName(t *testing.T) {
	store, err := OpenEnrollment(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Rename("ghost", "someone"); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("rename unknown: %v, want ErrNotEnrolled", err)
	}
}

func TestEnrollmentRenameInvalidNewName(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenEnrollment(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := store.Add(ctx, "Lance", "rev", [][]float32{{1, 2}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Rename("Lance", "speaker-3"); err == nil {
		t.Fatal("rename to a reserved name must fail")
	}
	if _, err := store.Rename("Lance", "a/b"); err == nil {
		t.Fatal("rename to a path-unsafe name must fail")
	}
}

func TestEnrollmentRenameSameSlugNoFileMove(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenEnrollment(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	added, err := store.Add(ctx, "lance", "rev", [][]float32{{1, 2}})
	if err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(dir, embeddingsDirName, "lance.f32")
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("embedding file missing before rename: %v", err)
	}

	// Case/spacing-only change: same slug, no file move.
	renamed, err := store.Rename("lance", "Lance")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "Lance" {
		t.Fatalf("renamed.Name = %q, want %q", renamed.Name, "Lance")
	}
	if !renamed.CreatedAt.Equal(added.CreatedAt) {
		t.Fatalf("rename must preserve CreatedAt: got %v, want %v", renamed.CreatedAt, added.CreatedAt)
	}
	if renamed.ModelRev != added.ModelRev || renamed.Dim != added.Dim || renamed.Samples != added.Samples {
		t.Fatalf("rename must preserve ModelRev/Dim/Samples, got %+v", renamed)
	}
	if !renamed.UpdatedAt.After(added.UpdatedAt) && !renamed.UpdatedAt.Equal(added.UpdatedAt) {
		t.Fatalf("rename must bump UpdatedAt: got %v, was %v", renamed.UpdatedAt, added.UpdatedAt)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("embedding file must stay in place on a same-slug rename: %v", err)
	}

	reopened, err := OpenEnrollment(dir)
	if err != nil {
		t.Fatal(err)
	}
	profiles := reopened.List()
	if len(profiles) != 1 || profiles[0].Name != "Lance" {
		t.Fatalf("profiles after reopen = %+v", profiles)
	}
}

func TestEnrollmentRenameMovesEmbeddingFile(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenEnrollment(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	added, err := store.Add(ctx, "Lance", "rev", [][]float32{{1, 2, 3}})
	if err != nil {
		t.Fatal(err)
	}

	renamed, err := store.Rename("Lance", "Lance R")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "Lance R" {
		t.Fatalf("renamed.Name = %q, want %q", renamed.Name, "Lance R")
	}
	if !renamed.CreatedAt.Equal(added.CreatedAt) {
		t.Fatalf("rename must preserve CreatedAt: got %v, want %v", renamed.CreatedAt, added.CreatedAt)
	}

	oldPath := filepath.Join(dir, embeddingsDirName, "lance.f32")
	newPath := filepath.Join(dir, embeddingsDirName, "lance-r.f32")
	if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old embedding file must be gone after rename, stat: %v", err)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("new embedding file missing after rename: %v", err)
	}

	got, err := store.Embeddings("Lance R")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, [][]float32{{1, 2, 3}}) {
		t.Fatalf("embeddings after rename = %v", got)
	}

	reopened, err := OpenEnrollment(dir)
	if err != nil {
		t.Fatal(err)
	}
	profiles := reopened.List()
	if len(profiles) != 1 || profiles[0].Name != "Lance R" {
		t.Fatalf("profiles after reopen = %+v", profiles)
	}
}

func TestEnrollmentRenameCollisionRefused(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenEnrollment(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := store.Add(ctx, "Lance", "rev", [][]float32{{1, 2}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(ctx, "Guest", "rev", [][]float32{{3, 4}}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Rename("Lance", "Guest"); err == nil {
		t.Fatal("rename onto an existing, differently-named profile must be refused")
	}

	// Neither profile changed.
	profiles := store.List()
	names := map[string]bool{}
	for _, p := range profiles {
		names[p.Name] = true
	}
	if !names["Lance"] || !names["Guest"] || len(profiles) != 2 {
		t.Fatalf("profiles after refused collision = %+v", profiles)
	}
}

func TestEnrollmentRenameRollsBackOnSaveFailure(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenEnrollment(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := store.Add(ctx, "Lance", "rev", [][]float32{{1, 2}}); err != nil {
		t.Fatal(err)
	}

	// Make the store dir read-only so saveLocked's atomic write of
	// profiles.yaml fails after the embedding file has already moved.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }()

	if _, err := store.Rename("Lance", "Lance R"); err == nil {
		t.Fatal("rename must fail when the profiles.yaml write fails")
	}

	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	oldPath := filepath.Join(dir, embeddingsDirName, "lance.f32")
	newPath := filepath.Join(dir, embeddingsDirName, "lance-r.f32")
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("rollback must restore the original embedding file: %v", err)
	}
	if _, err := os.Stat(newPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rollback must remove the moved embedding file, stat: %v", err)
	}

	profiles := store.List()
	if len(profiles) != 1 || profiles[0].Name != "Lance" {
		t.Fatalf("in-memory profiles after rollback = %+v", profiles)
	}
	reopened, err := OpenEnrollment(dir)
	if err != nil {
		t.Fatal(err)
	}
	reopenedProfiles := reopened.List()
	if len(reopenedProfiles) != 1 || reopenedProfiles[0].Name != "Lance" {
		t.Fatalf("profiles.yaml after rollback = %+v", reopenedProfiles)
	}
}

func TestEnrollFromWAVs(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenEnrollment(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	eng := &FakeEngine{EmbedDim: 4}

	writeWAV := func(t *testing.T, name string, seconds float64, rate int) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), name)
		samples := make([]float32, int(seconds*float64(rate)))
		for i := range samples {
			samples[i] = 0.25
		}
		if err := audio.WriteWAVFloat32(path, rate, samples); err != nil {
			t.Fatal(err)
		}
		return path
	}

	// Error cases first.
	if _, err := EnrollFromWAVs(ctx, eng, store, "lance", "rev", 0, []string{"x.wav"}); err == nil {
		t.Fatal("zero window must fail — callers pass Config.LiveWindowMS")
	}
	if _, err := EnrollFromWAVs(ctx, eng, store, "lance", "rev", 1500, nil); err == nil {
		t.Fatal("no clips must fail")
	}
	if _, err := EnrollFromWAVs(ctx, eng, store, "lance", "rev", 1500, []string{filepath.Join(dir, "missing.wav")}); err == nil {
		t.Fatal("missing clip must fail")
	}
	short := writeWAV(t, "short.wav", 0.2, audio.SampleRate)
	if _, err := EnrollFromWAVs(ctx, eng, store, "lance", "rev", 1500, []string{short}); err == nil {
		t.Fatal("clip under the minimum embed window must fail")
	}
	wrongRate := writeWAV(t, "rate.wav", 1.0, 44100)
	if _, err := EnrollFromWAVs(ctx, eng, store, "lance", "rev", 1500, []string{wrongRate}); err == nil {
		t.Fatal("wrong sample rate must fail")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	good := writeWAV(t, "good.wav", 1.0, audio.SampleRate)
	if _, err := EnrollFromWAVs(canceled, eng, store, "lance", "rev", 1500, []string{good}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ctx: %v", err)
	}
	if len(store.List()) != 0 {
		t.Fatal("failed enrollments must not persist")
	}

	// Happy path: short clips embed whole; long clips split into
	// live-window-sized rows (duration-matched to verification).
	second := writeWAV(t, "good2.wav", 1.5, audio.SampleRate)
	long := writeWAV(t, "long.wav", 4.0, audio.SampleRate)
	p, err := EnrollFromWAVs(ctx, eng, store, "Lance", "titanet", 1500, []string{good, second, long})
	if err != nil {
		t.Fatal(err)
	}
	// At 1500ms: 1.0s → 1 whole-clip row; 1.5s → 1 window; 4.0s → 2 windows.
	if p.Samples != 4 || p.Dim != 4 || p.ModelRev != "titanet" {
		t.Fatalf("profile = %+v", p)
	}

	// A non-default window resizes the split: at 1000ms, 4.0s → 4 windows.
	p, err = EnrollFromWAVs(ctx, eng, store, "Lance", "titanet", 1000, []string{long})
	if err != nil {
		t.Fatal(err)
	}
	if p.Samples != 4 {
		t.Fatalf("1000ms window over 4s clip: samples = %d, want 4", p.Samples)
	}
}

func TestEnrollWindows(t *testing.T) {
	const window = 3 * audio.SampleRate / 2 // 1.5s default live window
	cases := []struct {
		name    string
		samples int
		want    int
	}{
		{"short clip embeds whole", window - 1, 1},
		{"exactly one window", window, 1},
		{"two windows, remainder dropped", 2*window + 100, 2},
		{"capped at max", 20 * window, maxEnrollWindowsPerClip},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(enrollWindows(make([]float32, tc.samples), window)); got != tc.want {
				t.Fatalf("enrollWindows(%d samples) = %d windows, want %d", tc.samples, got, tc.want)
			}
		})
	}
}
