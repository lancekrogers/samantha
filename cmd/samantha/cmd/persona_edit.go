package cmd

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/persona"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	managedqwen "github.com/lancekrogers/samantha/pkg/voiceagent/qwen"
)

// maxPromptBytes caps an identity body. A prompt this large is a paste
// accident, and every turn pays for it in tokens.
const maxPromptBytes = 64 << 10

// personaVoiceCatalogFn resolves the static voice ids a TTS provider offers.
// It is a hook because the speech stack is compiled out of the integration
// build; a nil hook (or a provider with no static catalog, like qwen3-tts,
// whose voices come from a running worker) means an unknown voice is accepted
// rather than dropped — the same answer the TUI's editor gives.
var personaVoiceCatalogFn func(provider string) ([]string, error)

func personaEditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "edit <id>",
		Short: "Edit a persona's display name, prompt, brain model, or voice",
		Long: `Edit one persona profile in place.

Only the flags you pass are written: an untouched brain or TTS field keeps its
current value, and an explicitly empty value (--voice "") clears the field back
to inheriting the app default.

Prompt edits reach the next turn; brain and TTS changes reach the next
conversation, because a running session is bound to the identity it started
with.`,
		Args: cobra.ExactArgs(1),
		RunE: runPersonaEdit,
	}
	cmd.Flags().String("display-name", "", "New display name (becomes agent_name when active)")
	cmd.Flags().String("prompt", "", "Identity body text for the persona system prompt")
	cmd.Flags().String("prompt-file", "", "Read the identity body text from a file (UTF-8)")
	cmd.Flags().Bool("allow-flatten", false, "Replace a structured prompt document with a flat body (destructive)")
	cmd.Flags().String("brain-provider", "", "Brain provider for this persona (empty clears to the app default)")
	cmd.Flags().String("brain-model", "", "Brain model for this persona (empty clears to the app default)")
	cmd.Flags().String("tts-provider", "", "TTS provider for this persona (empty clears to the app default)")
	cmd.Flags().String("voice", "", "Voice id for the effective TTS provider (empty clears to the app default)")
	cmd.Flags().String("tier", "", "Qwen3-TTS model tier: 0.6b or 1.7b (empty inherits the app default)")
	cmd.Flags().Bool("json", false, "Emit JSON")
	cmd.MarkFlagsMutuallyExclusive("prompt", "prompt-file")
	return cmd
}

func runPersonaEdit(cmd *cobra.Command, args []string) error {
	// Argument validation already passed, so anything below is a runtime
	// failure: report it without dumping the flag reference over it.
	cmd.SilenceUsage = true
	asJSON, _ := cmd.Flags().GetBool("json")
	result, err := applyPersonaEdit(cmd, args[0])
	if err != nil {
		if asJSON {
			return emitJSONError(cmd, err)
		}
		return err
	}
	if asJSON {
		return encodeJSON(cmd, result)
	}
	printPersonaEdit(cmd, result)
	return nil
}

// applyPersonaEdit overlays the passed flags onto the persona and writes them
// through the existing persona writers, in the order display name → stack →
// prompt. Each writer validates, so a failure part-way reports which steps
// already landed instead of pretending the whole edit rolled back.
func applyPersonaEdit(cmd *cobra.Command, id string) (*personaResultJSON, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	p, err := loadPersonaForCLI(id)
	if err != nil {
		return nil, err
	}

	flags := cmd.Flags()
	brain, tts, stackPaths, err := mergePersonaStack(cmd, cfg, p)
	if err != nil {
		return nil, err
	}
	promptBody, promptChanged, err := resolvePromptBody(cmd)
	if err != nil {
		return nil, err
	}

	displayName := strings.TrimSpace(p.DisplayName)
	if flags.Changed("display-name") {
		v, _ := flags.GetString("display-name")
		displayName = strings.TrimSpace(v)
		if displayName == "" {
			return nil, fmt.Errorf("--display-name is required when passed (a persona must have a name)")
		}
	}

	// Resolve the current prompt before writing: it decides whether a flat
	// write would destroy structured sections, and whether the new body is
	// even different from what is already on disk.
	current, err := persona.DescribePrompt(p)
	if err != nil {
		return nil, err
	}
	if promptChanged {
		if current.Structured {
			allowFlatten, _ := flags.GetBool("allow-flatten")
			if !allowFlatten {
				return nil, codedError(codePromptStructured,
					"persona %q uses a structured prompt document (%s): writing a flat body would drop its conversation_style/guidance/constraints/core_concepts sections; pass --allow-flatten to replace it anyway",
					p.ID, current.Path)
			}
		}
		if strings.TrimSpace(promptBody) == strings.TrimSpace(current.Body) && current.Source == "user" {
			promptChanged = false
		}
	}

	changed := []string{}
	if displayName != strings.TrimSpace(p.DisplayName) {
		if _, err := persona.UpdateDisplayName(p.ID, displayName); err != nil {
			return nil, withChanged(err, changed)
		}
		changed = append(changed, "display_name")
	}
	if len(stackPaths) > 0 {
		if _, err := persona.UpdateStack(p.ID, brain, tts); err != nil {
			return nil, withChanged(err, changed)
		}
		changed = append(changed, stackPaths...)
	}
	if promptChanged {
		if _, err := persona.UpdateSystemPrompt(p.ID, promptBody); err != nil {
			return nil, withChanged(err, changed)
		}
		changed = append(changed, "prompt")
		// Writing a body also binds the profile to its own document. When the
		// persona was riding a shared ref, that ref moved — and the shared
		// document was deliberately left intact — so say so.
		if strings.TrimSpace(p.Prompts.Persona) != p.ID {
			changed = append(changed, "prompts.persona")
		}
	}

	// Re-read so the reported profile is what a later load will see, not what
	// this process believed before three separate validated writes.
	updated, err := persona.Load(p.ID)
	if err != nil {
		return nil, withChanged(err, changed)
	}
	result := &personaResultJSON{
		Persona: updated,
		Changed: changed,
		Active:  updated.ID == persona.ActiveID(cfg),
		Applies: defaultApplies(),
	}
	if flags.Changed("prompt") || flags.Changed("prompt-file") {
		info, err := describePromptJSON(updated, promptChanged)
		if err != nil {
			return nil, err
		}
		result.Prompt = info
	}
	return result, nil
}

// loadPersonaForCLI loads a persona and turns the two ways it can fail to
// exist into the coded errors a --json runner discriminates on.
func loadPersonaForCLI(id string) (*persona.Profile, error) {
	if err := persona.ValidateID(id); err != nil {
		return nil, codedError(codeInvalidID, "%v", err)
	}
	p, err := persona.Load(id)
	if err == nil {
		return p, nil
	}
	if !os.IsNotExist(err) && !strings.Contains(err.Error(), "no such file") {
		return nil, err
	}
	available, listErr := persona.List()
	if listErr != nil || len(available) == 0 {
		return nil, codedError(codeNotFound, "persona %q not found (no personas under %s)", id, persona.Dir())
	}
	ids := make([]string, 0, len(available))
	for _, x := range available {
		ids = append(ids, x.ID)
	}
	return nil, codedError(codeNotFound, "persona %q not found (available: %s)", id, strings.Join(ids, ", "))
}

// mergePersonaStack overlays the brain/TTS flags the user actually passed onto
// the persona's current stack and reports the dotted paths whose value really
// changes. UpdateStack replaces both structs wholesale, so passing anything
// other than a full merge here would silently clear the fields left untouched.
func mergePersonaStack(cmd *cobra.Command, cfg *config.Config, p *persona.Profile) (persona.Brain, persona.TTS, []string, error) {
	flags := cmd.Flags()
	// Trim the base as well as the flags. UpdateStack trims what it writes, so
	// comparing a raw on-disk value against a trimmed one would report a field
	// as changed for an edit that never touched it.
	base := persona.Brain{
		Provider: strings.TrimSpace(p.Brain.Provider),
		Model:    strings.TrimSpace(p.Brain.Model),
	}
	baseTTS := persona.TTS{
		Provider: strings.TrimSpace(p.TTS.Provider),
		Voice:    strings.TrimSpace(p.TTS.Voice),
		Tier:     strings.TrimSpace(p.TTS.Tier),
	}
	brain, tts := base, baseTTS

	stringFlag := func(name string) (string, bool) {
		if !flags.Changed(name) {
			return "", false
		}
		v, _ := flags.GetString(name)
		return strings.TrimSpace(v), true
	}

	if v, ok := stringFlag("brain-provider"); ok {
		brain.Provider = v
	}
	if v, ok := stringFlag("brain-model"); ok {
		brain.Model = v
	}
	if v, ok := stringFlag("tts-provider"); ok {
		if err := validateTTSProvider(v); err != nil {
			return brain, tts, nil, err
		}
		tts.Provider = v
	}
	if v, ok := stringFlag("voice"); ok {
		tts.Voice = v
	}
	if v, ok := stringFlag("tier"); ok {
		if err := persona.ValidateTier(v); err != nil {
			return brain, tts, nil, codedError(codeInvalidTier, "--tier %q: %v", v, err)
		}
		tts.Tier = managedqwen.NormalizeModelTier(v)
		if strings.TrimSpace(v) == "" {
			tts.Tier = ""
		}
	}
	if flags.Changed("voice") && tts.Voice != "" {
		if err := validateVoiceForProvider(effectiveTTSProvider(cfg, tts.Provider), tts.Voice); err != nil {
			return brain, tts, nil, err
		}
	}

	var paths []string
	if brain.Provider != base.Provider {
		paths = append(paths, "brain.provider")
	}
	if brain.Model != base.Model {
		paths = append(paths, "brain.model")
	}
	if tts.Provider != baseTTS.Provider {
		paths = append(paths, "tts.provider")
	}
	if tts.Voice != baseTTS.Voice {
		paths = append(paths, "tts.voice")
	}
	if tts.Tier != baseTTS.Tier {
		paths = append(paths, "tts.tier")
	}
	return brain, tts, paths, nil
}

// effectiveTTSProvider is the provider a voice will actually be routed to: the
// persona's own override when it has one, otherwise the app default.
func effectiveTTSProvider(cfg *config.Config, personaProvider string) string {
	if p := strings.TrimSpace(personaProvider); p != "" {
		return p
	}
	if cfg != nil {
		return strings.TrimSpace(cfg.TTSProvider)
	}
	return ""
}

// validateTTSProvider rejects a provider this build cannot speak. An unknown
// name would otherwise persist and only fail later, at synthesis time.
func validateTTSProvider(provider string) error {
	if provider == "" || personaVoiceCatalogFn == nil {
		return nil
	}
	if _, err := personaVoiceCatalogFn(provider); err != nil {
		return codedError(codeInvalidProvider, "--tts-provider %q: %v", provider, err)
	}
	return nil
}

// validateVoiceForProvider refuses a voice the provider's catalog does not
// list. Providers whose voices come from a running worker publish no static
// catalog; there the value is kept as typed rather than dropped, matching the
// TUI's persona form.
func validateVoiceForProvider(provider, voice string) error {
	if personaVoiceCatalogFn == nil {
		return nil
	}
	names, err := personaVoiceCatalogFn(provider)
	if err != nil {
		return codedError(codeInvalidProvider, "tts provider %q: %v", provider, err)
	}
	if len(names) == 0 {
		return nil
	}
	for _, n := range names {
		if strings.EqualFold(n, voice) {
			return nil
		}
	}
	return codedError(codeInvalidVoice, "--voice %q: not a %s voice (try `samantha voices --json --provider %s`)", voice, provider, provider)
}

// resolvePromptBody reads the identity body from --prompt or --prompt-file.
// The body is plain text: samantha owns the samantha.prompt.v1 wrapper, so a
// caller never has to author the document format.
func resolvePromptBody(cmd *cobra.Command) (string, bool, error) {
	flags := cmd.Flags()
	var body string
	switch {
	case flags.Changed("prompt-file"):
		path, _ := flags.GetString("prompt-file")
		data, err := os.ReadFile(path)
		if err != nil {
			return "", false, fmt.Errorf("reading --prompt-file %s: %w", path, err)
		}
		body = string(data)
	case flags.Changed("prompt"):
		body, _ = flags.GetString("prompt")
	default:
		return "", false, nil
	}
	if len(body) > maxPromptBytes {
		return "", false, fmt.Errorf("prompt body is %d bytes, limit is %d", len(body), maxPromptBytes)
	}
	if !utf8.ValidString(body) {
		return "", false, fmt.Errorf("prompt body is not valid UTF-8")
	}
	if strings.TrimSpace(body) == "" {
		return "", false, codedError(codePromptEmpty, "prompt body is empty")
	}
	return body, true, nil
}

// withChanged attaches the steps that already landed to a mid-sequence
// failure, so a caller knows the edit was partial rather than atomic.
func withChanged(err error, changed []string) error {
	if len(changed) == 0 {
		return err
	}
	return &cliError{Err: err, Changed: append([]string(nil), changed...)}
}

func printPersonaEdit(cmd *cobra.Command, r *personaResultJSON) {
	out := cmd.OutOrStdout()
	if len(r.Changed) == 0 {
		fmt.Fprintf(out, "\n  %s is unchanged.\n\n", r.Persona.ID)
		return
	}
	fmt.Fprintf(out, "\n  %s\n", titleStyle.Render("Updated persona: "+r.Persona.ID))
	fmt.Fprintf(out, "  changed         %s\n", strings.Join(r.Changed, ", "))
	fmt.Fprintf(out, "  display_name    %s\n", r.Persona.DisplayName)
	fmt.Fprintf(out, "  brain           %s %s\n", r.Persona.Brain.Provider, r.Persona.Brain.Model)
	fmt.Fprintf(out, "  tts             %s %s %s\n", r.Persona.TTS.Provider, r.Persona.TTS.Voice, r.Persona.TTS.Tier)
	fmt.Fprintf(out, "  path            %s\n", r.Persona.Path)
	fmt.Fprintf(out, "\n  %s\n\n", dimStyle.Render("Prompt edits apply on the next turn; brain and voice on the next conversation."))
}
