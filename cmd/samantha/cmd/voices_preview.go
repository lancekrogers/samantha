//go:build !integration

package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/persona"
	"github.com/lancekrogers/samantha/pkg/voiceagent/audio"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	managedqwen "github.com/lancekrogers/samantha/pkg/voiceagent/qwen"
	"github.com/lancekrogers/samantha/pkg/voiceagent/tts"
)

// voicePreviewJSON is the body of `voices preview --json`.
type voicePreviewJSON struct {
	Provider   string `json:"provider"`
	Voice      string `json:"voice"`
	Tier       string `json:"tier"`
	Text       string `json:"text"`
	Path       string `json:"path"`
	// SampleRate is whatever Synthesize reported. A player that assumes a rate
	// instead of reading this one plays the clip at the wrong speed.
	SampleRate int   `json:"sample_rate"`
	DurationMS int64 `json:"duration_ms"`
	Bytes      int64 `json:"bytes"`
	// Warning carries a non-fatal note, e.g. a --tier passed to a provider
	// that has no tiers.
	Warning string `json:"warning,omitempty"`
}

func newVoicesPreviewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "preview",
		Short: "Render one sample sentence in a voice to a WAV file",
		Long: `Synthesize a short sample line and write it to --out as a WAV.

Nothing is played and nothing is downloaded: missing assets are reported as
assets_missing so a Preview button cannot start a large download by surprise.
Pass --ensure to opt into installing them first.

The provider, voice, and tier are applied to a copy of the config, so an
audition never changes what the agent speaks with.`,
		Args: cobra.NoArgs,
		RunE: runVoicesPreview,
	}
	cmd.Flags().StringP("provider", "p", "", "TTS provider to audition (default: the configured one)")
	cmd.Flags().String("voice", "", "Voice id to audition")
	cmd.Flags().String("tier", "", "Qwen3-TTS model tier: 0.6b or 1.7b (qwen3-tts only)")
	cmd.Flags().String("text", "", "Sample text (default: the standard preview line for the voice)")
	cmd.Flags().String("out", "", "WAV file to write")
	cmd.Flags().Bool("ensure", false, "Install missing TTS assets first instead of failing")
	cmd.Flags().Bool("json", false, "Emit JSON")
	_ = cmd.MarkFlagRequired("voice")
	_ = cmd.MarkFlagRequired("out")
	return cmd
}

func runVoicesPreview(cmd *cobra.Command, _ []string) error {
	cmd.SilenceUsage = true
	asJSON, _ := cmd.Flags().GetBool("json")
	result, err := renderVoicePreview(cmd)
	if err != nil {
		if asJSON {
			return emitJSONError(cmd, err)
		}
		return err
	}
	if asJSON {
		return encodeJSON(cmd, result)
	}
	printVoicePreview(cmd, result)
	return nil
}

func renderVoicePreview(cmd *cobra.Command) (*voicePreviewJSON, error) {
	cfg, err := voicesConfig(cmd)
	if err != nil {
		return nil, err
	}
	voice, _ := cmd.Flags().GetString("voice")
	voice = strings.TrimSpace(voice)
	if voice == "" {
		return nil, codedError(codeInvalidVoice, "--voice is required")
	}
	if err := validateVoiceForProvider(cfg.TTSProvider, voice); err != nil {
		return nil, err
	}
	warning, err := applyPreviewVoice(cmd, cfg, voice)
	if err != nil {
		return nil, err
	}

	out, _ := cmd.Flags().GetString("out")
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, fmt.Errorf("--out is required")
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return nil, fmt.Errorf("creating output dir for %s: %w", out, err)
	}

	if ensure, _ := cmd.Flags().GetBool("ensure"); ensure {
		if err := voiceStack.ensureAssets(cmd.Context(), cfg, config.AssetRequest{NeedTTS: true}, nil); err != nil {
			return nil, fmt.Errorf("installing TTS assets: %w", err)
		}
	} else if err := requireTTSAssets(cfg); err != nil {
		return nil, err
	}

	text, _ := cmd.Flags().GetString("text")
	if strings.TrimSpace(text) == "" {
		text = tts.SpokenPreviewLine(voice)
	}

	provider, cleanup, err := voiceStack.newProvider(cfg)
	if err != nil {
		return nil, codedError(codeInvalidProvider, "init TTS: %v", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	stream, err := provider.Synthesize(cmd.Context(), text)
	if err != nil {
		return nil, fmt.Errorf("synthesizing preview: %w", err)
	}
	samples, rate, err := drainPCMStream(cmd.Context(), stream, 0)
	if err != nil {
		return nil, fmt.Errorf("reading preview audio: %w", err)
	}
	if len(samples) == 0 {
		return nil, fmt.Errorf("%s produced no audio for voice %q", cfg.TTSProvider, voice)
	}
	if err := audio.WriteWAVFloat32(out, rate, samples); err != nil {
		return nil, fmt.Errorf("writing %s: %w", out, err)
	}
	info, err := os.Stat(out)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", out, err)
	}

	return &voicePreviewJSON{
		Provider:   cfg.TTSProvider,
		Voice:      voice,
		Tier:       previewTier(cfg),
		Text:       text,
		Path:       out,
		SampleRate: rate,
		DurationMS: int64(len(samples)) * 1000 / int64(rate),
		Bytes:      info.Size(),
		Warning:    warning,
	}, nil
}

// applyPreviewVoice routes the voice and tier onto the config copy the way
// Apply routes a persona's, and reports a warning rather than an error when a
// tier is passed to a provider that has none.
func applyPreviewVoice(cmd *cobra.Command, cfg *config.Config, voice string) (string, error) {
	tier, _ := cmd.Flags().GetString("tier")
	tier = strings.TrimSpace(tier)

	if !isQwenTTS(cfg.TTSProvider) {
		cfg.TTSVoice = voice
		if tier == "" {
			return "", nil
		}
		return fmt.Sprintf("--tier %q ignored: only qwen3-tts selects a model tier", tier), nil
	}

	cfg.QwenTTSVoice = voice
	cfg.QwenTTSMode = string(tts.VoiceModeCustomVoice)
	if strings.TrimSpace(cfg.QwenTTSLanguage) == "" {
		cfg.QwenTTSLanguage = "Auto"
	}
	if tier == "" {
		return "", nil
	}
	if err := persona.ValidateTier(tier); err != nil {
		return "", codedError(codeInvalidTier, "--tier %q: %v", tier, err)
	}
	cfg.QwenTTSModelTier = managedqwen.NormalizeModelTier(tier)
	return "", nil
}

// previewTier reports the tier that was actually in force, empty where the
// provider does not read one.
func previewTier(cfg *config.Config) string {
	if isQwenTTS(cfg.TTSProvider) {
		return cfg.QwenTTSModelTier
	}
	return ""
}

func printVoicePreview(cmd *cobra.Command, r *voicePreviewJSON) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "\n  %s\n", titleStyle.Render("Preview: "+r.Voice))
	fmt.Fprintf(out, "  provider     %s\n", r.Provider)
	if r.Tier != "" {
		fmt.Fprintf(out, "  tier         %s\n", r.Tier)
	}
	fmt.Fprintf(out, "  text         %s\n", r.Text)
	fmt.Fprintf(out, "  path         %s\n", r.Path)
	fmt.Fprintf(out, "  sample_rate  %d\n", r.SampleRate)
	fmt.Fprintf(out, "  duration     %d ms\n", r.DurationMS)
	if r.Warning != "" {
		fmt.Fprintf(out, "  %s\n", dimStyle.Render("warning: "+r.Warning))
	}
	fmt.Fprintln(out)
}
