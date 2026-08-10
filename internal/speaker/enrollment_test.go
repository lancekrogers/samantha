package speaker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lancekrogers/samantha/internal/audio"
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
	if _, err := EnrollFromWAVs(ctx, eng, store, "lance", "rev", nil); err == nil {
		t.Fatal("no clips must fail")
	}
	if _, err := EnrollFromWAVs(ctx, eng, store, "lance", "rev", []string{filepath.Join(dir, "missing.wav")}); err == nil {
		t.Fatal("missing clip must fail")
	}
	short := writeWAV(t, "short.wav", 0.2, audio.SampleRate)
	if _, err := EnrollFromWAVs(ctx, eng, store, "lance", "rev", []string{short}); err == nil {
		t.Fatal("clip under the minimum embed window must fail")
	}
	wrongRate := writeWAV(t, "rate.wav", 1.0, 44100)
	if _, err := EnrollFromWAVs(ctx, eng, store, "lance", "rev", []string{wrongRate}); err == nil {
		t.Fatal("wrong sample rate must fail")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	good := writeWAV(t, "good.wav", 1.0, audio.SampleRate)
	if _, err := EnrollFromWAVs(canceled, eng, store, "lance", "rev", []string{good}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ctx: %v", err)
	}
	if len(store.List()) != 0 {
		t.Fatal("failed enrollments must not persist")
	}

	// Happy path: one embedding row per clip.
	second := writeWAV(t, "good2.wav", 1.5, audio.SampleRate)
	p, err := EnrollFromWAVs(ctx, eng, store, "Lance", "titanet", []string{good, second})
	if err != nil {
		t.Fatal(err)
	}
	if p.Samples != 2 || p.Dim != 4 || p.ModelRev != "titanet" {
		t.Fatalf("profile = %+v", p)
	}
}
