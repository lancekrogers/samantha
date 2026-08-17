//go:build !integration

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/pkg/voiceagent/audio"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	"github.com/lancekrogers/samantha/pkg/voiceagent/tts"
)

// fakeTTS stands in for a real engine: constructing Kokoro or Qwen pages a
// model into memory, which no unit test can assume is on disk.
type fakeTTS struct {
	voices     []tts.Voice
	sampleRate int
	samples    int
	lastText   string
}

func (f *fakeTTS) Available() bool { return true }

func (f *fakeTTS) ListVoices(locale, gender string) []tts.Voice {
	var out []tts.Voice
	for _, v := range f.voices {
		if locale != "" && !strings.EqualFold(v.Locale, locale) {
			continue
		}
		if gender != "" && !strings.EqualFold(v.Gender, gender) {
			continue
		}
		out = append(out, v)
	}
	return out
}

func (f *fakeTTS) Synthesize(ctx context.Context, text string) (*audio.PCMStream, error) {
	f.lastText = text
	stream := audio.NewPCMStream(ctx)
	if err := stream.SetSampleRate(f.sampleRate); err != nil {
		return nil, err
	}
	go func() {
		defer stream.Close()
		frame := make([]float32, f.samples)
		for i := range frame {
			frame[i] = float32(math.Sin(float64(i) / 8))
		}
		_ = stream.Write(frame)
	}()
	return stream, nil
}

// stubVoiceStack swaps the heavy speech dependencies for the duration of t.
func stubVoiceStack(t *testing.T, provider *fakeTTS, missing []string) *int {
	t.Helper()
	orig := voiceStack
	ensured := 0
	voiceStack = voiceStackDeps{
		newProvider: func(*config.Config) (tts.Provider, func(), error) {
			return provider, nil, nil
		},
		missingAssets: func(*config.Config) []string { return missing },
		ensureAssets: func(context.Context, *config.Config, config.AssetRequest, func(string, float64)) error {
			ensured++
			return nil
		},
	}
	t.Cleanup(func() { voiceStack = orig })
	return &ensured
}

func kokoroFake() *fakeTTS {
	return &fakeTTS{
		voices: []tts.Voice{
			{Name: "af_heart", FriendlyName: "Kokoro Heart (en-us)", Gender: "female", Locale: "en-us"},
			{Name: "bm_george", FriendlyName: "Kokoro George (en-gb)", Gender: "male", Locale: "en-gb"},
		},
		sampleRate: 24000,
		samples:    24000,
	}
}

func runVoicesCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newVoicesCmd()
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(args)
	cmd.SetContext(context.Background())
	// Subcommands inherit the parent's streams only when they are set before
	// execution, which cobra does; the context has to be passed explicitly.
	for _, sub := range cmd.Commands() {
		sub.SetOut(&out)
		sub.SetErr(&errBuf)
	}
	err := cmd.Execute()
	return out.String(), err
}

func TestVoicesJSONCatalog(t *testing.T) {
	personaEnv(t)
	stubVoiceStack(t, kokoroFake(), nil)

	out, err := runVoicesCmd(t, "--json")
	if err != nil {
		t.Fatalf("voices --json error = %v (out %s)", err, out)
	}
	var got voicesJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if got.Provider != "kokoro" || got.ActiveVoice != "af_heart" {
		t.Fatalf("catalog = %+v, want the configured kokoro/af_heart", got)
	}
	if len(got.Voices) != 2 {
		t.Fatalf("voices = %+v, want 2", got.Voices)
	}
	if !got.Voices[0].Active || got.Voices[1].Active {
		t.Fatalf("active marks = %+v, want only af_heart", got.Voices)
	}
}

func TestVoicesJSONFilters(t *testing.T) {
	personaEnv(t)
	stubVoiceStack(t, kokoroFake(), nil)

	out, err := runVoicesCmd(t, "--json", "--gender", "male")
	if err != nil {
		t.Fatalf("voices --json error = %v (out %s)", err, out)
	}
	var got voicesJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Voices) != 1 || got.Voices[0].Name != "bm_george" {
		t.Fatalf("voices = %+v, want only bm_george", got.Voices)
	}
}

// Listing another provider's catalog is a read: config.yaml must be untouched.
func TestVoicesProviderOverrideNeverWritesConfig(t *testing.T) {
	dir := personaEnv(t)
	stubVoiceStack(t, kokoroFake(), nil)
	before, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	out, err := runVoicesCmd(t, "--json", "--provider", "qwen3-tts")
	if err != nil {
		t.Fatalf("voices --json error = %v (out %s)", err, out)
	}
	var got voicesJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Provider != "qwen3-tts" {
		t.Fatalf("provider = %q, want the override", got.Provider)
	}
	after, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("config.yaml changed:\n%s", after)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TTSProvider != "kokoro" {
		t.Fatalf("live tts_provider = %q, want kokoro", cfg.TTSProvider)
	}
}

func TestVoicesJSONUnknownProvider(t *testing.T) {
	personaEnv(t)
	stubVoiceStack(t, kokoroFake(), nil)

	out, err := runVoicesCmd(t, "--json", "--provider", "elevenlabs")
	if err == nil {
		t.Fatalf("voices --json error = nil, want invalid_provider (out %s)", out)
	}
	if code, _, _ := decodeErrorJSON(t, out); code != codeInvalidProvider {
		t.Fatalf("code = %q, want %q", code, codeInvalidProvider)
	}
}

func TestVoicesJSONMissingAssets(t *testing.T) {
	personaEnv(t)
	ensured := stubVoiceStack(t, kokoroFake(), []string{"Kokoro model"})

	out, err := runVoicesCmd(t, "--json")
	if err == nil {
		t.Fatalf("voices --json error = nil, want assets_missing (out %s)", out)
	}
	code, msg, _ := decodeErrorJSON(t, out)
	if code != codeAssetsMissing {
		t.Fatalf("code = %q, want %q", code, codeAssetsMissing)
	}
	if !strings.Contains(msg, "samantha models ensure --tts") {
		t.Fatalf("error = %q, want the remediation command", msg)
	}
	if *ensured != 0 {
		t.Fatalf("ensureAssets called %d times, want 0 — --json must never download", *ensured)
	}
}

// The clip has to be a real WAV: read it back and check the reported rate and
// a non-zero duration.
func TestVoicesPreviewWritesReadableWAV(t *testing.T) {
	dir := personaEnv(t)
	fake := kokoroFake()
	stubVoiceStack(t, fake, nil)
	out := filepath.Join(dir, "preview", "af_heart.wav")

	stdout, err := runVoicesCmd(t, "preview", "--voice", "af_heart", "--out", out, "--json")
	if err != nil {
		t.Fatalf("voices preview error = %v (out %s)", err, stdout)
	}
	var got voicePreviewJSON
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decoding %q: %v", stdout, err)
	}
	if got.Provider != "kokoro" || got.Voice != "af_heart" || got.Tier != "" {
		t.Fatalf("result = %+v", got)
	}
	if got.Text != tts.SpokenPreviewLine("af_heart") {
		t.Fatalf("text = %q, want the standard preview line", got.Text)
	}
	if fake.lastText != got.Text {
		t.Fatalf("synthesized %q but reported %q", fake.lastText, got.Text)
	}
	if got.SampleRate != 24000 || got.DurationMS != 1000 {
		t.Fatalf("sample_rate/duration = %d/%d, want 24000/1000", got.SampleRate, got.DurationMS)
	}

	samples, rate, err := audio.ReadWAVFloat32(out)
	if err != nil {
		t.Fatalf("reading back %s: %v", out, err)
	}
	if rate != got.SampleRate {
		t.Fatalf("wav rate = %d, want the reported %d", rate, got.SampleRate)
	}
	if len(samples) != 24000 {
		t.Fatalf("wav samples = %d, want 24000", len(samples))
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != got.Bytes {
		t.Fatalf("bytes = %d, want the file size %d", got.Bytes, info.Size())
	}
}

func TestVoicesPreviewCustomText(t *testing.T) {
	dir := personaEnv(t)
	fake := kokoroFake()
	stubVoiceStack(t, fake, nil)
	out := filepath.Join(dir, "preview.wav")

	if _, err := runVoicesCmd(t, "preview", "--voice", "af_heart", "--text", "Ready when you are.", "--out", out, "--json"); err != nil {
		t.Fatalf("voices preview error = %v", err)
	}
	if fake.lastText != "Ready when you are." {
		t.Fatalf("synthesized %q, want the custom text", fake.lastText)
	}
}

// A Preview button must never start a download: missing assets are an error,
// and --ensure is the explicit opt-in.
func TestVoicesPreviewAssetPolicy(t *testing.T) {
	t.Run("missing assets refuse without downloading", func(t *testing.T) {
		dir := personaEnv(t)
		ensured := stubVoiceStack(t, kokoroFake(), []string{"Kokoro model"})
		out := filepath.Join(dir, "preview.wav")

		stdout, err := runVoicesCmd(t, "preview", "--voice", "af_heart", "--out", out, "--json")
		if err == nil {
			t.Fatalf("voices preview error = nil, want assets_missing (out %s)", stdout)
		}
		if code, _, _ := decodeErrorJSON(t, stdout); code != codeAssetsMissing {
			t.Fatalf("code = %q, want %q", code, codeAssetsMissing)
		}
		if *ensured != 0 {
			t.Fatalf("ensureAssets called %d times, want 0", *ensured)
		}
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Fatalf("wrote %s despite missing assets", out)
		}
	})

	t.Run("--ensure installs first", func(t *testing.T) {
		dir := personaEnv(t)
		ensured := stubVoiceStack(t, kokoroFake(), []string{"Kokoro model"})
		out := filepath.Join(dir, "preview.wav")

		stdout, err := runVoicesCmd(t, "preview", "--voice", "af_heart", "--out", out, "--ensure", "--json")
		if err != nil {
			t.Fatalf("voices preview --ensure error = %v (out %s)", err, stdout)
		}
		if *ensured != 1 {
			t.Fatalf("ensureAssets called %d times, want 1", *ensured)
		}
	})
}

// A tier on a provider with no tiers is a warning, not a failure: the caller
// still gets its audio.
func TestVoicesPreviewTierOffQwenWarns(t *testing.T) {
	dir := personaEnv(t)
	stubVoiceStack(t, kokoroFake(), nil)
	out := filepath.Join(dir, "preview.wav")

	stdout, err := runVoicesCmd(t, "preview", "--voice", "af_heart", "--tier", "1.7b", "--out", out, "--json")
	if err != nil {
		t.Fatalf("voices preview error = %v (out %s)", err, stdout)
	}
	var got voicePreviewJSON
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatal(err)
	}
	if got.Warning == "" || !strings.Contains(got.Warning, "qwen3-tts") {
		t.Fatalf("warning = %q, want it to name qwen3-tts", got.Warning)
	}
	if got.Tier != "" {
		t.Fatalf("tier = %q, want empty for a provider that reads none", got.Tier)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("no audio written: %v", err)
	}
}

func TestVoicesPreviewQwenTierCanonicalized(t *testing.T) {
	dir := personaEnv(t)
	fake := kokoroFake()
	fake.voices = nil // qwen publishes no static catalog
	stubVoiceStack(t, fake, nil)
	out := filepath.Join(dir, "preview.wav")

	stdout, err := runVoicesCmd(t, "preview", "--provider", "qwen3-tts", "--voice", "Uncle_Fu", "--tier", "1.7", "--out", out, "--json")
	if err != nil {
		t.Fatalf("voices preview error = %v (out %s)", err, stdout)
	}
	var got voicePreviewJSON
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatal(err)
	}
	if got.Tier != "1.7b" {
		t.Fatalf("tier = %q, want the canonical 1.7b", got.Tier)
	}
	if got.Warning != "" {
		t.Fatalf("warning = %q, want none on qwen", got.Warning)
	}
}

func TestVoicesPreviewBadTier(t *testing.T) {
	dir := personaEnv(t)
	fake := kokoroFake()
	fake.voices = nil
	stubVoiceStack(t, fake, nil)
	out := filepath.Join(dir, "preview.wav")

	stdout, err := runVoicesCmd(t, "preview", "--provider", "qwen3-tts", "--voice", "Uncle_Fu", "--tier", "3b", "--out", out, "--json")
	if err == nil {
		t.Fatalf("voices preview error = nil, want invalid_tier (out %s)", stdout)
	}
	if code, _, _ := decodeErrorJSON(t, stdout); code != codeInvalidTier {
		t.Fatalf("code = %q, want %q", code, codeInvalidTier)
	}
}

func TestVoicesPreviewUnknownVoice(t *testing.T) {
	dir := personaEnv(t)
	stubVoiceStack(t, kokoroFake(), nil)
	out := filepath.Join(dir, "preview.wav")

	stdout, err := runVoicesCmd(t, "preview", "--voice", "nope", "--out", out, "--json")
	if err == nil {
		t.Fatalf("voices preview error = nil, want invalid_voice (out %s)", stdout)
	}
	if code, _, _ := decodeErrorJSON(t, stdout); code != codeInvalidVoice {
		t.Fatalf("code = %q, want %q", code, codeInvalidVoice)
	}
}

func TestVoicesPreviewRequiresOut(t *testing.T) {
	personaEnv(t)
	stubVoiceStack(t, kokoroFake(), nil)

	if _, err := runVoicesCmd(t, "preview", "--voice", "af_heart", "--json"); err == nil {
		t.Fatal("voices preview error = nil, want the required --out")
	}
}

// Guard the seam the integration binary depends on: the voices tree is only
// compiled into the speech build, so nothing here may leak into the other one.
func TestVoicesCommandTree(t *testing.T) {
	cmd := newVoicesCmd()
	var preview *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "preview" {
			preview = sub
		}
	}
	if preview == nil {
		t.Fatal("voices has no preview subcommand")
	}
	for _, name := range []string{"voice", "out", "tier", "text", "provider", "ensure", "json"} {
		if preview.Flags().Lookup(name) == nil {
			t.Errorf("preview is missing --%s", name)
		}
	}
}

// A refused preview leaves nothing behind — not even the directory it would
// have written into.
func TestVoicesPreviewRefusalCreatesNoDirectory(t *testing.T) {
	dir := personaEnv(t)
	stubVoiceStack(t, kokoroFake(), []string{"Kokoro model"})
	outDir := filepath.Join(dir, "previews")
	out := filepath.Join(outDir, "af_heart.wav")

	if _, err := runVoicesCmd(t, "preview", "--voice", "af_heart", "--out", out, "--json"); err == nil {
		t.Fatal("voices preview error = nil, want assets_missing")
	}
	if _, err := os.Stat(outDir); !os.IsNotExist(err) {
		t.Fatalf("created %s on a refused preview: %v", outDir, err)
	}
}

// A provider that reports no sample rate must produce a clear error, not a
// divide-by-zero on duration_ms.
func TestVoicesPreviewRejectsZeroSampleRate(t *testing.T) {
	dir := personaEnv(t)
	fake := kokoroFake()
	fake.sampleRate = 0
	stubVoiceStack(t, fake, nil)
	out := filepath.Join(dir, "preview.wav")

	stdout, err := runVoicesCmd(t, "preview", "--voice", "af_heart", "--out", out, "--json")
	if err == nil {
		t.Fatalf("voices preview error = nil, want a sample-rate failure (out %s)", stdout)
	}
	if _, msg, _ := decodeErrorJSON(t, stdout); !strings.Contains(msg, "sample rate") {
		t.Fatalf("error = %q, want it to name the sample rate", msg)
	}
}
