//go:build !integration

package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	managedqwen "github.com/lancekrogers/samantha/pkg/voiceagent/qwen"
	"github.com/lancekrogers/samantha/pkg/voiceagent/tts"
)

// voiceStackDeps are the heavy dependencies of the voices verbs: constructing
// a provider pages a model into memory, and ensuring assets can download
// hundreds of megabytes. Tests swap them for fakes so the JSON contract and
// the WAV path are exercised without a model on disk.
type voiceStackDeps struct {
	newProvider   func(*config.Config) (tts.Provider, func(), error)
	missingAssets func(*config.Config) []string
	ensureAssets  func(context.Context, *config.Config, config.AssetRequest, func(string, float64)) error
}

var voiceStack = voiceStackDeps{
	newProvider:   tts.NewProvider,
	missingAssets: missingTTSAssets,
	ensureAssets:  config.EnsureRuntimeAssets,
}

// voiceJSON is one row of `voices --json`.
type voiceJSON struct {
	Name         string `json:"name"`
	FriendlyName string `json:"friendly_name"`
	Gender       string `json:"gender"`
	Locale       string `json:"locale"`
	Active       bool   `json:"active"`
}

// voicesJSON is the body of `voices --json`: the catalog of one provider,
// with the voice that provider is currently configured to speak marked.
type voicesJSON struct {
	Provider    string      `json:"provider"`
	Source      string      `json:"source"` // "provider" (constructed and queried live) | "static" (compile-time catalog, no provider construction)
	ActiveVoice string      `json:"active_voice"`
	Voices      []voiceJSON `json:"voices"`
	// Languages lists qwen3-tts's supported synthesis languages (independent
	// of which voice is selected); omitted for every other provider.
	Languages []string `json:"languages,omitempty"`
}

func newVoicesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "voices",
		Short: "List available TTS voices",
		Long: `List the voices of a TTS provider.

--provider lists another provider's catalog without changing any config: the
provider is built from a copy of the loaded config, never the live one.

--json never downloads. When the provider's assets are missing it reports
assets_missing and names the command that installs them.`,
		RunE: runVoices,
	}
	cmd.Flags().StringP("locale", "l", "", "Filter by locale (e.g. en-US)")
	cmd.Flags().StringP("gender", "g", "", "Filter by gender (male/female)")
	cmd.Flags().StringP("provider", "p", "", "List this provider's catalog instead of the configured one")
	cmd.Flags().Bool("json", false, "Emit JSON")
	cmd.AddCommand(newVoicesPreviewCmd())
	return cmd
}

func runVoices(cmd *cobra.Command, _ []string) error {
	cmd.SilenceUsage = true
	asJSON, _ := cmd.Flags().GetBool("json")
	if !asJSON {
		return runVoicesTable(cmd)
	}
	catalog, err := voicesCatalog(cmd)
	if err != nil {
		return emitJSONError(cmd, err)
	}
	return encodeJSON(cmd, catalog)
}

func voicesCatalog(cmd *cobra.Command) (*voicesJSON, error) {
	cfg, err := voicesConfig(cmd)
	if err != nil {
		return nil, err
	}
	locale, _ := cmd.Flags().GetString("locale")
	gender, _ := cmd.Flags().GetString("gender")

	if err := requireTTSAssets(cfg); err != nil {
		// qwen3-tts's 9 CustomVoice presets are model-native, not
		// install-dependent (tts.StaticVoices), so a fresh machine can still
		// browse them before running `models ensure --tts`. Every other
		// provider keeps the original refuse-on-missing contract: --json
		// must never download, and a live catalog it cannot safely list
		// without a model on disk is not silently swapped for something
		// incomplete.
		if isQwenTTS(cfg.TTSProvider) {
			if voices, staticErr := tts.StaticVoices(cfg.TTSProvider, locale, gender); staticErr == nil && len(voices) > 0 {
				return buildVoicesJSON(cfg, voices, "static"), nil
			}
		}
		return nil, err
	}

	provider, cleanup, err := voiceStack.newProvider(cfg)
	if err != nil {
		return nil, codedError(codeInvalidProvider, "init TTS: %v", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	return buildVoicesJSON(cfg, provider.ListVoices(locale, gender), "provider"), nil
}

// buildVoicesJSON assembles the voices --json body from an already-filtered
// voice list and source ("provider" | "static"); Languages is populated for
// qwen3-tts regardless of source, since it does not vary with it.
func buildVoicesJSON(cfg *config.Config, voices []tts.Voice, source string) *voicesJSON {
	active := activeVoiceFor(cfg)
	out := &voicesJSON{Provider: cfg.TTSProvider, Source: source, ActiveVoice: active, Voices: []voiceJSON{}}
	for _, v := range voices {
		out.Voices = append(out.Voices, voiceJSON{
			Name:         v.Name,
			FriendlyName: v.FriendlyName,
			Gender:       v.Gender,
			Locale:       v.Locale,
			Active:       strings.EqualFold(v.Name, active),
		})
	}
	if isQwenTTS(cfg.TTSProvider) {
		out.Languages = managedqwen.SupportedLanguages()
	}
	return out
}

// voicesConfig loads the config and applies --provider to a copy. Listing a
// catalog is a read: it must never leave the requested provider behind in
// config.yaml or in the process-wide viper state.
func voicesConfig(cmd *cobra.Command) (*config.Config, error) {
	loaded, err := config.Load()
	if err != nil {
		return nil, err
	}
	cfg := *loaded
	if override, _ := cmd.Flags().GetString("provider"); strings.TrimSpace(override) != "" {
		if err := validateTTSProvider(strings.TrimSpace(override)); err != nil {
			return nil, err
		}
		cfg.TTSProvider = strings.TrimSpace(override)
	}
	if strings.TrimSpace(cfg.TTSProvider) == "" {
		cfg.TTSProvider = "kokoro"
	}
	return &cfg, nil
}

// requireTTSAssets refuses rather than downloads. A voice picker opening or a
// Preview button being pressed must not start a hundreds-of-megabyte fetch the
// user never asked for; `samantha models ensure --tts` is the explicit path.
func requireTTSAssets(cfg *config.Config) error {
	missing := voiceStack.missingAssets(cfg)
	if len(missing) == 0 {
		return nil
	}
	return codedError(codeAssetsMissing,
		"%s assets are not installed (%s): run `samantha models ensure --tts`",
		cfg.TTSProvider, strings.Join(missing, ", "))
}

// missingTTSAssets names the not-yet-installed assets for cfg's TTS provider,
// reading the manifest offline. It mirrors `samantha models status --tts`,
// including the managed Qwen native package that lives outside the manifest.
func missingTTSAssets(cfg *config.Config) []string {
	manifest, err := config.ManifestFor(cfg, config.AssetRequest{NeedTTS: true})
	if err != nil {
		return []string{err.Error()}
	}
	modelsDir := config.ModelsDirFrom(cfg)
	var missing []string
	for _, s := range manifest.Status(modelsDir) {
		if !s.Installed {
			missing = append(missing, s.Name)
		}
	}
	if isQwenTTS(cfg.TTSProvider) && managedqwen.UseManaged(cfg.QwenTTSBinary, cfg.QwenTTSModel) {
		if native := managedqwen.InspectNative(modelsDir, cfg.QwenTTSModelTier); !native.Installed {
			missing = append(missing, "Qwen3-TTS native package")
		}
	}
	return missing
}

// runVoicesTable keeps the pre-existing human path byte-for-byte, including
// its download-on-demand behaviour: a terminal user asking for the list can
// answer a progress bar, a front end parsing --json cannot.
func runVoicesTable(cmd *cobra.Command) error {
	cfg, err := voicesConfig(cmd)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "\n  %s %s\n\n", titleStyle.Render("Voices for:"), sectionStyle.Render(cfg.TTSProvider))

	if err := voiceStack.ensureAssets(cmd.Context(), cfg, config.AssetRequest{NeedTTS: true}, nil); err != nil {
		return err
	}

	provider, cleanup, err := voiceStack.newProvider(cfg)
	if err != nil {
		return fmt.Errorf("init TTS: %w", err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	locale, _ := cmd.Flags().GetString("locale")
	gender, _ := cmd.Flags().GetString("gender")
	voices := provider.ListVoices(locale, gender)
	if len(voices) == 0 {
		fmt.Fprintln(out, dimStyle.Render("  No voices found."))
		return nil
	}

	activeVoice := activeVoiceFor(cfg)
	for _, v := range voices {
		active := ""
		if strings.EqualFold(v.Name, activeVoice) {
			active = " " + activeStyle.Render("●")
		}
		fmt.Fprintf(out, "  %s %s  %s\n", keyStyle.Render(fmt.Sprintf("%-16s", v.Name)), v.FriendlyName, dimStyle.Render(fmt.Sprintf("%s / %s", v.Gender, v.Locale))+active)
	}
	fmt.Fprintf(out, "\n  %s\n\n", dimStyle.Render(fmt.Sprintf("%d voices found.", len(voices))))
	return nil
}
