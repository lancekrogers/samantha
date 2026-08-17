//go:build !integration

package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	appTUI "github.com/lancekrogers/samantha/internal/tui"
	"github.com/lancekrogers/samantha/pkg/voiceagent/audio"
	"github.com/lancekrogers/samantha/pkg/voiceagent/brain"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	"github.com/lancekrogers/samantha/pkg/voiceagent/session"
	"github.com/lancekrogers/samantha/pkg/voiceagent/stt"
	"github.com/lancekrogers/samantha/pkg/voiceagent/tts"
)

var testCmd = &cobra.Command{
	Use:   "test",
	Short: "Test microphone and speaker",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}

		fmt.Printf("\n  %s\n", titleStyle.Render("Samantha Audio Test"))
		fmt.Printf("  %s\n\n", dimStyle.Render(fmt.Sprintf("TTS: %s | STT: %s", cfg.TTSProvider, cfg.STTProvider)))

		// TTS test
		fmt.Printf("  %s %s\n", sectionStyle.Render("1."), "Testing speaker (TTS)...")
		speakerErr := runSpeakerTest(cmd.Context(), cfg)
		if speakerErr != nil {
			fmt.Printf("  %s %v\n\n", failStyle.Render("FAIL:"), speakerErr)
		} else {
			fmt.Printf("  %s Played speaker test clip\n\n", okStyle.Render("PASS:"))
		}

		// STT test placeholder
		fmt.Printf("  %s %s\n", sectionStyle.Render("2."), "Testing microphone (STT)...")
		fmt.Println(dimStyle.Render("  (mic test requires full pipeline — use 'samantha' to test)"))
		fmt.Println()
		if speakerErr != nil {
			fmt.Println(failStyle.Render("  Test failed."))
			fmt.Println()
			return fmt.Errorf("speaker (TTS) test failed: %w", speakerErr)
		}
		fmt.Println(okStyle.Render("  Test complete."))
		fmt.Println()
		return nil
	},
}

func runSpeakerTest(ctx context.Context, cfg *config.Config) error {
	if err := config.EnsureRuntimeAssets(ctx, cfg, config.AssetRequest{NeedTTS: true}, nil); err != nil {
		return err
	}

	ttsProvider, cleanup, err := tts.NewProvider(cfg)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}

	player := audio.NewPlayerWithDevice(cfg.OutputDevice)
	defer func() { _ = player.Close() }()

	// Speaker test uses the configured voice name so multi-provider previews
	// match Settings → Voice audition lines.
	voiceName := strings.TrimSpace(cfg.TTSVoice)
	if strings.EqualFold(strings.TrimSpace(cfg.TTSProvider), "qwen3-tts") && strings.TrimSpace(cfg.QwenTTSVoice) != "" {
		voiceName = cfg.QwenTTSVoice
	}
	stream, err := ttsProvider.Synthesize(ctx, tts.SpokenPreviewLine(voiceName))
	if err != nil {
		return err
	}

	playback, err := player.PlayStream(ctx, stream)
	if err != nil {
		return err
	}

	result := <-playback.Done()
	if result.Err != nil && !result.Interrupted {
		return result.Err
	}
	return nil
}

var providersJSON bool

var providersCmd = &cobra.Command{
	Use:   "providers",
	Short: "Show available TTS and STT providers",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		return runProviders(cmd, cfg, providersJSON)
	},
}

// providerEntryJSON is one provider row under providers --json.
type providerEntryJSON struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Detail      string `json:"detail,omitempty"`
	Active      bool   `json:"active"`
	Implemented bool   `json:"implemented"`
}

// providersGroupJSON is the brain/tts group shape under providers --json.
type providersGroupJSON struct {
	Active    string              `json:"active"`
	Providers []providerEntryJSON `json:"providers"`
}

// sttProvidersGroupJSON is the STT group shape: it additionally carries the
// raw configured value and the mode-resolved alias's mode, alongside Active
// (the mode-resolved alias itself) — the same resolution the human path uses.
type sttProvidersGroupJSON struct {
	Active     string              `json:"active"`
	Configured string              `json:"configured"`
	Mode       string              `json:"mode"`
	Providers  []providerEntryJSON `json:"providers"`
}

// providersJSONDoc is the full providers --json document.
type providersJSONDoc struct {
	Brain providersGroupJSON    `json:"brain"`
	TTS   providersGroupJSON    `json:"tts"`
	STT   sttProvidersGroupJSON `json:"stt"`
}

// resolveSTTActive mirrors the human providers path: the mode-resolved alias
// when NormalizeSTTWithMode succeeds (falling back to the bare provider when
// the alias is empty, i.e. the default), otherwise the raw configured value
// (doctor reports the underlying error).
func resolveSTTActive(cfg *config.Config) (active, mode string) {
	norm, err := config.NormalizeSTTWithMode(cfg.STTProvider, cfg.STTMode)
	if err != nil {
		return cfg.STTProvider, ""
	}
	active = norm.Alias
	if active == "" {
		active = norm.Provider
	}
	return active, norm.Mode
}

// buildProvidersJSON builds the providers --json document for cfg. It is
// read-only and offline: every input comes from the compiled-in provider
// spec tables and cfg, never from a constructed provider or a network call.
func buildProvidersJSON(cfg *config.Config) providersJSONDoc {
	brainActive := strings.TrimSpace(cfg.BrainProvider)
	if brainActive == "" {
		brainActive = "ollama"
	}
	brainEntries := make([]providerEntryJSON, 0, len(brain.Providers()))
	for _, spec := range brain.Providers() {
		brainEntries = append(brainEntries, providerEntryJSON{
			Name: spec.Name, Description: spec.Description,
			Active: spec.Name == brainActive, Implemented: brainImplemented(spec.Name),
		})
	}

	ttsActive := cfg.TTSProvider
	if ttsActive == "" {
		ttsActive = "kokoro"
	}
	ttsEntries := make([]providerEntryJSON, 0, len(tts.Providers()))
	for _, spec := range tts.Providers() {
		ttsEntries = append(ttsEntries, providerEntryJSON{
			Name: spec.Name, Description: spec.Description,
			Active: spec.Name == ttsActive, Implemented: ttsImplemented(spec.Name),
		})
	}

	sttActive, sttMode := resolveSTTActive(cfg)
	sttEntries := make([]providerEntryJSON, 0, len(stt.Providers()))
	for _, spec := range stt.Providers() {
		sttEntries = append(sttEntries, providerEntryJSON{
			Name: spec.Name, Description: spec.Description, Detail: sttSpecDetail(spec.Name),
			Active: spec.Name == sttActive, Implemented: sttImplemented(spec.Name),
		})
	}

	return providersJSONDoc{
		Brain: providersGroupJSON{Active: brainActive, Providers: brainEntries},
		TTS:   providersGroupJSON{Active: ttsActive, Providers: ttsEntries},
		STT: sttProvidersGroupJSON{
			Active: sttActive, Configured: cfg.STTProvider, Mode: sttMode, Providers: sttEntries,
		},
	}
}

// runProviders reports the brain/TTS/STT providers compiled into this build,
// which is active, and (for STT) the mode-resolved alias — read-only and
// offline in both output forms; --json never constructs a provider.
func runProviders(cmd *cobra.Command, cfg *config.Config, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(buildProvidersJSON(cfg))
	}

	fmt.Println()
	fmt.Printf("  %s\n", titleStyle.Render("Providers"))
	fmt.Printf("  %s\n", sectionStyle.Render("Brain:"))
	brainActive := strings.TrimSpace(cfg.BrainProvider)
	if brainActive == "" {
		brainActive = "ollama"
	}
	for _, spec := range brain.Providers() {
		printProvider(brainActive == spec.Name, spec.Name, spec.Description)
	}
	if !brainImplemented(brainActive) {
		fmt.Printf("    %s %s\n", dimStyle.Render("[config]"), dimStyle.Render(brainActive+" — configured but not implemented in this build"))
	}

	fmt.Println()
	fmt.Printf("  %s\n", sectionStyle.Render("TTS (text-to-speech):"))
	ttsActive := cfg.TTSProvider
	if ttsActive == "" {
		ttsActive = "kokoro"
	}
	for _, spec := range tts.Providers() {
		printProvider(ttsActive == spec.Name, spec.Name, spec.Description)
	}
	if !ttsImplemented(ttsActive) {
		fmt.Printf("    %s %s\n", dimStyle.Render("[config]"), dimStyle.Render(ttsActive+" — configured but not implemented in this build"))
	}

	fmt.Println()
	fmt.Printf("  %s\n", sectionStyle.Render("STT (speech-to-text):"))
	// Highlight the mode-resolved alias so sherpa + stt_mode=streaming
	// marks the streaming row; a misconfigured pair falls back to the raw
	// configured value (doctor reports the error).
	sttActive := cfg.STTProvider
	if norm, err := config.NormalizeSTTWithMode(cfg.STTProvider, cfg.STTMode); err == nil {
		sttActive = norm.Alias
		if sttActive == "" {
			sttActive = norm.Provider
		}
	}
	for _, spec := range stt.Providers() {
		printProvider(sttActive == spec.Name, spec.Name, spec.Description)
		if detail := sttSpecDetail(spec.Name); detail != "" {
			fmt.Printf("      %s\n", dimStyle.Render(detail))
		}
	}
	if !sttImplemented(sttActive) {
		fmt.Printf("    %s %s\n", dimStyle.Render("[config]"), dimStyle.Render(sttActive+" — configured but not implemented in this build"))
	}
	fmt.Println()
	return nil
}

var resumeCmd = &cobra.Command{
	Use:   "resume [session-id]",
	Short: "Resume a past conversation",
	Long:  "Pick a past conversation to resume. Shows recent sessions if no ID given.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}

		if len(args) == 1 {
			// Resume specific session.
			sess, err := session.Load(args[0])
			if err != nil {
				return fmt.Errorf("load session: %w", err)
			}
			return startResumed(cfg, sess)
		}

		// List sessions and let user pick.
		sessions := session.List()
		if len(sessions) == 0 {
			fmt.Println(dimStyle.Render("  No saved sessions."))
			return nil
		}

		fmt.Println()
		fmt.Printf("  %s\n\n", titleStyle.Render("Recent sessions"))
		max := 10
		if len(sessions) < max {
			max = len(sessions)
		}
		for i, s := range sessions[:max] {
			turns := len(s.Turns)
			age := fmtAge(s.UpdatedAt)
			meta := dimStyle.Render(fmt.Sprintf("%d turns, %s ago", turns, age))
			fmt.Printf("  %s %s %s %s\n",
				dimStyle.Render(fmt.Sprintf("%2d.", i+1)),
				sectionStyle.Render(s.ID),
				s.Summary,
				dimStyle.Render("— ")+meta)
		}
		fmt.Println()
		fmt.Printf("  %s\n\n", dimStyle.Render("Usage: samantha resume <session-id>"))
		return nil
	},
}

var continueCmd = &cobra.Command{
	Use:     "continue",
	Short:   "Continue the most recent conversation",
	Aliases: []string{"c"},
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}

		sess := session.Latest()
		if sess == nil {
			fmt.Println(dimStyle.Render("  No saved sessions to continue."))
			return nil
		}

		return startResumed(cfg, sess)
	},
}

// startResumed lands an interactive resume directly in the conversation
// screen with history seeded. Gate matches root useConversationTUI (D3).
func startResumed(cfg *config.Config, sess *session.Session) error {
	if useConversationTUI() {
		return appTUI.RunConversation(cfg, conversationRuntimeBuilder(sess))
	}
	return startPipeline(cfg, sess)
}

func fmtAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// printProvider renders one provider row with a green marker when it is the
// active provider.
func printProvider(active bool, name, desc string) {
	marker := "  "
	style := keyStyle
	if active {
		marker = activeStyle.Render("●") + " "
		style = activeStyle
	}
	fmt.Printf("    %s%s %s\n", marker, style.Render(name), dimStyle.Render("— "+desc))
}

func brainImplemented(name string) bool {
	for _, spec := range brain.Providers() {
		if spec.Name == name {
			return true
		}
	}
	return false
}

// sttSpecDetail renders one STT provider's capability line from the spec side
// table. It returns "" when the name has no spec, so the row prints unchanged.
func sttSpecDetail(name string) string {
	norm, ok := config.NormalizeSTT(name)
	if !ok {
		return ""
	}
	spec, ok := stt.SpecForNormalized(norm)
	if !ok {
		return ""
	}

	parts := []string{spec.Provider + "/" + spec.Mode}
	if spec.EmitsPartial {
		parts = append(parts, "partials")
	}
	if spec.UsesEndpoint {
		parts = append(parts, "self-endpoint")
	}
	if spec.SupportsEOF {
		parts = append(parts, "eof")
	}
	if spec.RequiresVAD {
		parts = append(parts, "requires vad")
	}
	if spec.RecommendedUse != "" {
		parts = append(parts, spec.RecommendedUse)
	}
	return strings.Join(parts, " · ")
}

func sttImplemented(name string) bool {
	for _, spec := range stt.Providers() {
		if spec.Name == name {
			return true
		}
	}
	return false
}

func ttsImplemented(name string) bool {
	for _, spec := range tts.Providers() {
		if spec.Name == name {
			return true
		}
	}
	return false
}

// ttsVoiceCatalog lists the voice ids a TTS provider publishes without loading
// its model. `persona edit --voice` runs on every save from the Mac editor, so
// it validates against this static catalog rather than constructing a provider
// and paging a few hundred megabytes of weights into memory. Providers whose
// voices only exist inside a running worker (qwen3-tts) return no names, and
// the caller then keeps the value as typed.
func ttsVoiceCatalog(provider string) ([]string, error) {
	voices, err := tts.StaticVoices(provider, "", "")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(voices))
	for _, v := range voices {
		names = append(names, v.Name)
	}
	return names, nil
}

func init() {
	personaVoiceCatalogFn = ttsVoiceCatalog
	providersCmd.Flags().BoolVar(&providersJSON, "json", false, "Output machine-readable JSON")

	rootCmd.AddCommand(testCmd)
	rootCmd.AddCommand(newVoicesCmd())
	rootCmd.AddCommand(providersCmd)
	rootCmd.AddCommand(resumeCmd)
	rootCmd.AddCommand(continueCmd)
	rootCmd.AddCommand(newRenderCmd(runRenderText, config.Load))
	rootCmd.AddCommand(newAudiobookCmd(runRenderText, config.Load))
	rootCmd.AddCommand(newNarrateCmd())
	rootCmd.AddCommand(newMeetingCmd())
	rootCmd.AddCommand(newSpeakerCmd(config.Load))
	rootCmd.AddCommand(newLibraryCmd(config.Load))
	rootCmd.AddCommand(newSkillsCmd(config.Load))
}
