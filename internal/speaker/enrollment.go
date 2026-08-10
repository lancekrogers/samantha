package speaker

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/lancekrogers/samantha/internal/audio"
)

// ErrNotEnrolled is returned when a named profile does not exist.
var ErrNotEnrolled = errors.New("speaker: not enrolled")

const (
	profilesFileName  = "profiles.yaml"
	embeddingsDirName = "embeddings"
)

// Profile describes one enrolled, named speaker. Embeddings live in a
// sidecar file (embeddings/<slug>.f32) and are only comparable to
// embeddings produced by the same model — ModelRev records which.
type Profile struct {
	Name      string    `yaml:"name" json:"name"`
	ModelRev  string    `yaml:"model_revision" json:"model_revision"`
	Dim       int       `yaml:"dim" json:"dim"`
	Samples   int       `yaml:"samples" json:"samples"`
	CreatedAt time.Time `yaml:"created_at" json:"created_at"`
	UpdatedAt time.Time `yaml:"updated_at" json:"updated_at"`
}

type profilesFile struct {
	Profiles []Profile `yaml:"profiles"`
}

// Enrollment persists named speaker profiles under one directory:
//
//	<dir>/profiles.yaml
//	<dir>/embeddings/<slug>.f32   raw little-endian float32, Samples×Dim
//
// It is the durable counterpart to the sherpa embedding manager, which is
// in-memory only. Names are unique case-insensitively; re-enrolling a name
// replaces its embeddings in place.
type Enrollment struct {
	dir string

	mu       sync.Mutex
	profiles map[string]Profile // key = slug
}

// LoadEnrollment opens the store only when it already exists on disk.
// Runtime paths use this so unenrolled installs stay byte-for-byte
// untouched — no directories are created. Returns (nil, nil) when absent.
func LoadEnrollment(dir string) (*Enrollment, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, nil
	}
	if _, err := os.Stat(filepath.Join(dir, profilesFileName)); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("speaker: stat enrollment store: %w", err)
	}
	return OpenEnrollment(dir)
}

// SeedFromStore registers every stored profile whose model revision matches
// the engine's current embedding model. Stale-revision profiles are skipped
// and returned by name (re-enroll to refresh them). Engines without seeding
// support, and nil stores, seed nothing.
func SeedFromStore(eng Engine, store *Enrollment) (seeded int, skipped []string, err error) {
	seeder, ok := eng.(EnrolledSeeder)
	if !ok || store == nil {
		return 0, nil, nil
	}
	rev := seeder.EmbeddingRev()
	for _, p := range store.List() {
		if p.ModelRev != rev {
			skipped = append(skipped, p.Name)
			continue
		}
		embs, err := store.Embeddings(p.Name)
		if err != nil {
			return seeded, skipped, err
		}
		if err := seeder.SeedEnrolled(p.Name, p.ModelRev, embs); err != nil {
			return seeded, skipped, err
		}
		seeded++
	}
	return seeded, skipped, nil
}

// OpenEnrollment loads (or initializes) the store at dir.
func OpenEnrollment(dir string) (*Enrollment, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("speaker: enrollment dir is empty")
	}
	if err := os.MkdirAll(filepath.Join(dir, embeddingsDirName), 0o700); err != nil {
		return nil, fmt.Errorf("speaker: create enrollment dir: %w", err)
	}
	e := &Enrollment{dir: dir, profiles: map[string]Profile{}}
	raw, err := os.ReadFile(filepath.Join(dir, profilesFileName))
	if errors.Is(err, os.ErrNotExist) {
		return e, nil
	}
	if err != nil {
		return nil, fmt.Errorf("speaker: read profiles: %w", err)
	}
	var pf profilesFile
	if err := yaml.Unmarshal(raw, &pf); err != nil {
		return nil, fmt.Errorf("speaker: parse profiles: %w", err)
	}
	for _, p := range pf.Profiles {
		slug, err := profileSlug(p.Name)
		if err != nil {
			return nil, fmt.Errorf("speaker: stored profile %q: %w", p.Name, err)
		}
		if prev, ok := e.profiles[slug]; ok {
			return nil, fmt.Errorf("speaker: stored profiles %q and %q collide on key %q", prev.Name, p.Name, slug)
		}
		e.profiles[slug] = p
	}
	return e, nil
}

// Dir returns the store's directory.
func (e *Enrollment) Dir() string { return e.dir }

// Add persists a named profile with one embedding row per sample clip,
// replacing any existing profile with the same (case-insensitive) name.
// A different name that maps to the same storage key is rejected rather
// than silently overwriting.
func (e *Enrollment) Add(ctx context.Context, name, modelRev string, embeddings [][]float32) (Profile, error) {
	if err := ctx.Err(); err != nil {
		return Profile{}, err
	}
	slug, err := profileSlug(name)
	if err != nil {
		return Profile{}, err
	}
	if len(embeddings) == 0 {
		return Profile{}, fmt.Errorf("speaker: enroll %q: no embeddings", name)
	}
	dim := len(embeddings[0])
	if dim == 0 {
		return Profile{}, fmt.Errorf("speaker: enroll %q: empty embedding", name)
	}
	for i, emb := range embeddings {
		if len(emb) != dim {
			return Profile{}, fmt.Errorf("speaker: enroll %q: embedding %d has dim %d, want %d", name, i, len(emb), dim)
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	now := time.Now().UTC()
	profile := Profile{
		Name:      name,
		ModelRev:  modelRev,
		Dim:       dim,
		Samples:   len(embeddings),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if prev, ok := e.profiles[slug]; ok {
		// Same slug is only a re-enroll when the names match case-insensitively;
		// distinct names ("Mary Jane" vs "Mary-Jane") must never overwrite.
		if !strings.EqualFold(strings.TrimSpace(prev.Name), strings.TrimSpace(name)) {
			return Profile{}, fmt.Errorf("speaker: enroll %q: collides with enrolled profile %q (both stored as %q); remove it first or choose a distinct name", name, prev.Name, slug)
		}
		profile.CreatedAt = prev.CreatedAt
	}

	if err := writeEmbeddingsFile(e.embeddingsPath(slug), embeddings); err != nil {
		return Profile{}, err
	}
	e.profiles[slug] = profile
	if err := e.saveLocked(); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

// Remove deletes the named profile and its embedding file from disk.
func (e *Enrollment) Remove(name string) error {
	slug, err := profileSlug(name)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.profiles[slug]; !ok {
		return fmt.Errorf("speaker: remove %q: %w", name, ErrNotEnrolled)
	}
	if err := os.Remove(e.embeddingsPath(slug)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("speaker: remove %q embeddings: %w", name, err)
	}
	delete(e.profiles, slug)
	return e.saveLocked()
}

// List returns all profiles sorted by name.
func (e *Enrollment) List() []Profile {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Profile, 0, len(e.profiles))
	for _, p := range e.profiles {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Embeddings loads the stored embedding rows for a name.
func (e *Enrollment) Embeddings(name string) ([][]float32, error) {
	slug, err := profileSlug(name)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	p, ok := e.profiles[slug]
	path := e.embeddingsPath(slug)
	e.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("speaker: embeddings %q: %w", name, ErrNotEnrolled)
	}
	return readEmbeddingsFile(path, p.Dim, p.Samples)
}

func (e *Enrollment) embeddingsPath(slug string) string {
	return filepath.Join(e.dir, embeddingsDirName, slug+".f32")
}

func (e *Enrollment) saveLocked() error {
	pf := profilesFile{Profiles: make([]Profile, 0, len(e.profiles))}
	for _, p := range e.profiles {
		pf.Profiles = append(pf.Profiles, p)
	}
	sort.Slice(pf.Profiles, func(i, j int) bool { return pf.Profiles[i].Name < pf.Profiles[j].Name })
	raw, err := yaml.Marshal(pf)
	if err != nil {
		return fmt.Errorf("speaker: encode profiles: %w", err)
	}
	return atomicWrite(filepath.Join(e.dir, profilesFileName), raw)
}

// profileSlug validates an enrollment name and derives its file-safe key.
// Reserved labels (unknown, speaker-N) and path-unsafe names are rejected so
// enrolled names can never collide with anonymous cluster labels.
func profileSlug(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("speaker: enrollment name is empty")
	}
	if len(trimmed) > 64 {
		return "", fmt.Errorf("speaker: enrollment name %q exceeds 64 characters", trimmed)
	}
	lower := strings.ToLower(trimmed)
	if lower == LabelUnknown || strings.HasPrefix(lower, LabelSpeakerPrefix) {
		return "", fmt.Errorf("speaker: enrollment name %q is reserved", trimmed)
	}
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == ' ', r == '-', r == '_', r == '.':
		default:
			return "", fmt.Errorf("speaker: enrollment name %q contains unsupported character %q", trimmed, r)
		}
	}
	if strings.HasPrefix(trimmed, ".") || strings.HasPrefix(trimmed, "-") {
		return "", fmt.Errorf("speaker: enrollment name %q must start with a letter or digit", trimmed)
	}
	return strings.ReplaceAll(lower, " ", "-"), nil
}

func writeEmbeddingsFile(path string, embeddings [][]float32) error {
	buf := make([]byte, 0, len(embeddings)*len(embeddings[0])*4)
	var scratch [4]byte
	for _, emb := range embeddings {
		for _, v := range emb {
			binary.LittleEndian.PutUint32(scratch[:], math.Float32bits(v))
			buf = append(buf, scratch[:]...)
		}
	}
	return atomicWrite(path, buf)
}

func readEmbeddingsFile(path string, dim, samples int) ([][]float32, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("speaker: read embeddings: %w", err)
	}
	if dim <= 0 || samples <= 0 || len(raw) != dim*samples*4 {
		return nil, fmt.Errorf("speaker: embeddings file %s is %d bytes, want %d (dim %d × samples %d × 4)",
			filepath.Base(path), len(raw), dim*samples*4, dim, samples)
	}
	out := make([][]float32, samples)
	for i := range out {
		row := make([]float32, dim)
		for j := range row {
			off := (i*dim + j) * 4
			row[j] = math.Float32frombits(binary.LittleEndian.Uint32(raw[off : off+4]))
		}
		out[i] = row
	}
	return out, nil
}

func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("speaker: write %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("speaker: finalize %s: %w", filepath.Base(path), err)
	}
	return nil
}

// maxEnrollWindowsPerClip bounds embeds for very long clips.
const maxEnrollWindowsPerClip = 8

// EnrollFromWAVs embeds each 16 kHz mono clip with eng in live-window-sized
// pieces (one row per window, whole clip when shorter — sherpa RegisterV
// multi-sample shape) and persists the named profile in store.
//
// windowMS must be the live analysis window (Config.LiveWindowMS) so
// enrollment embeds are duration-matched to verification embeds: whole-span
// embeds of 20-45s audio measured FRR 0.80 against 1.5s verify windows on
// the meeting fixture; windowed enrollment measured 0.30 at the same 0.00
// FAR (tests/ownerverify).
func EnrollFromWAVs(ctx context.Context, eng Engine, store *Enrollment, name, modelRev string, windowMS int, wavPaths []string) (Profile, error) {
	if windowMS <= 0 {
		return Profile{}, fmt.Errorf("speaker: enroll %q: window %d ms; pass Config.LiveWindowMS", name, windowMS)
	}
	if len(wavPaths) == 0 {
		return Profile{}, fmt.Errorf("speaker: enroll %q: no clips given", name)
	}
	windowSamples := windowMS * audio.SampleRate / 1000
	embeddings := make([][]float32, 0, len(wavPaths))
	for _, path := range wavPaths {
		if err := ctx.Err(); err != nil {
			return Profile{}, err
		}
		samples, rate, err := audio.ReadWAVFloat32(path)
		if err != nil {
			return Profile{}, fmt.Errorf("speaker: enroll clip %s: %w", path, err)
		}
		if rate != audio.SampleRate {
			return Profile{}, fmt.Errorf("speaker: enroll clip %s: sample rate %d Hz, want %d Hz mono", path, rate, audio.SampleRate)
		}
		if len(samples) < minLiveEmbedSamples {
			return Profile{}, fmt.Errorf("speaker: enroll clip %s: %.2fs of audio, want at least %.1fs",
				path, float64(len(samples))/float64(audio.SampleRate), float64(minLiveEmbedSamples)/float64(audio.SampleRate))
		}
		for _, win := range enrollWindows(samples, windowSamples) {
			emb, err := eng.Embed(ctx, win)
			if err != nil {
				return Profile{}, fmt.Errorf("speaker: enroll clip %s: %w", path, err)
			}
			embeddings = append(embeddings, emb)
		}
	}
	return store.Add(ctx, name, modelRev, embeddings)
}

// enrollWindows splits a clip into live-window-sized pieces; a clip shorter
// than one window embeds whole.
func enrollWindows(samples []float32, windowSamples int) [][]float32 {
	if len(samples) < windowSamples {
		return [][]float32{samples}
	}
	out := make([][]float32, 0, maxEnrollWindowsPerClip)
	for start := 0; start+windowSamples <= len(samples) && len(out) < maxEnrollWindowsPerClip; start += windowSamples {
		out = append(out, samples[start:start+windowSamples])
	}
	return out
}
