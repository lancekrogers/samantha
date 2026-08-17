package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/persona"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

func personaUseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "use <id>",
		Short: "Set the active persona and persist it",
		Args:  cobra.ExactArgs(1),
		RunE:  runPersonaUse,
	}
	cmd.Flags().Bool("json", false, "Emit JSON")
	return cmd
}

func runPersonaUse(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	asJSON, _ := cmd.Flags().GetBool("json")
	cfg, err := usePersona(args[0])
	if err != nil {
		if asJSON {
			return emitJSONError(cmd, err)
		}
		return err
	}
	if asJSON {
		return encodeJSON(cmd, personaUseResult(cfg))
	}
	printPersonaUse(cmd, cfg)
	return nil
}

func usePersona(id string) (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if _, err := loadPersonaForCLI(id); err != nil {
		return nil, err
	}
	if err := persona.Use(cfg, id); err != nil {
		return nil, err
	}
	return cfg, nil
}

// personaUseResult reports the stack as cfg holds it after Use returns, not as
// the profile declares it: the profile's empty fields mean "inherit", and a
// caller switching personas wants the values that actually took effect.
func personaUseResult(cfg *config.Config) personaUseJSON {
	return personaUseJSON{
		ActivePersona: cfg.ActivePersona,
		DisplayName:   cfg.AgentName,
		Brain: persona.Brain{
			Provider: cfg.BrainProvider,
			Model:    persona.ModelForProvider(cfg, cfg.BrainProvider),
		},
		TTS: persona.TTS{
			Provider: cfg.TTSProvider,
			Voice:    activeVoiceFor(cfg),
			Tier:     activeTierFor(cfg),
		},
	}
}

// activeVoiceFor reads the voice key the configured provider actually speaks
// from. Qwen and Kokoro keep separate keys, so reading tts_voice alone would
// report the wrong voice for half the personas.
func activeVoiceFor(cfg *config.Config) string {
	if isQwenTTS(cfg.TTSProvider) && strings.TrimSpace(cfg.QwenTTSVoice) != "" {
		return cfg.QwenTTSVoice
	}
	return cfg.TTSVoice
}

// activeTierFor reports the model tier only where one is read: the tier key is
// global, and reporting it under Kokoro would imply a setting that does nothing.
func activeTierFor(cfg *config.Config) string {
	if isQwenTTS(cfg.TTSProvider) {
		return cfg.QwenTTSModelTier
	}
	return ""
}

func isQwenTTS(provider string) bool {
	return strings.EqualFold(strings.TrimSpace(provider), "qwen3-tts")
}

func printPersonaUse(cmd *cobra.Command, cfg *config.Config) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "  Active persona: %s (%s)\n", cfg.ActivePersona, cfg.AgentName)
	if cfg.TTSProvider != "" {
		fmt.Fprintf(out, "  TTS provider:   %s\n", cfg.TTSProvider)
	}
	if voice := activeVoiceFor(cfg); voice != "" {
		fmt.Fprintf(out, "  Voice:          %s\n", voice)
	}
}

func personaCreateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create <display-name>",
		Short: "Create a new persona and make it active",
		Long: `Create a user persona under personas/<id>/persona.yaml.

The id is derived from the display name (kebab-case). Brain and TTS settings
are cloned from the current config as a snapshot. Pass --prompt or
--prompt-file to write a custom system prompt document under
prompts/persona/<id>.yaml (supports {agent_name}).

--no-activate creates the persona without changing which one is active, so a
front end can offer "create" and "switch" as separate decisions.`,
		Args: cobra.MinimumNArgs(1),
		RunE: runPersonaCreate,
	}
	cmd.Flags().String("prompt", "", "Custom system prompt body (writes prompts/persona/<id>.yaml)")
	cmd.Flags().String("prompt-file", "", "Read the system prompt body from a file (UTF-8)")
	cmd.Flags().Bool("no-activate", false, "Create without making the new persona active")
	cmd.Flags().Bool("json", false, "Emit JSON")
	cmd.MarkFlagsMutuallyExclusive("prompt", "prompt-file")
	return cmd
}

func runPersonaCreate(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	asJSON, _ := cmd.Flags().GetBool("json")
	result, err := createPersona(cmd, args)
	if err != nil {
		if asJSON {
			return emitJSONError(cmd, err)
		}
		return err
	}
	if asJSON {
		return encodeJSON(cmd, result)
	}
	printPersonaCreate(cmd, result)
	return nil
}

func createPersona(cmd *cobra.Command, args []string) (*personaCreateJSON, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	body, wrotePrompt, err := resolvePromptBody(cmd)
	if err != nil {
		return nil, err
	}
	opts := persona.CreateOpts{
		DisplayName:  strings.TrimSpace(strings.Join(args, " ")),
		SystemPrompt: body,
	}

	noActivate, _ := cmd.Flags().GetBool("no-activate")
	create := persona.CreateAndUseWithOpts
	if noActivate {
		// CreateWithOpts leaves active_persona alone, so a front end can create
		// a persona in the background without hijacking the running agent.
		create = persona.CreateWithOpts
	}
	p, err := create(cfg, opts)
	if err != nil {
		return nil, err
	}

	result := &personaCreateJSON{Persona: p, Created: true, Activated: !noActivate}
	if wrotePrompt {
		info, err := describePromptJSON(p, true)
		if err != nil {
			return nil, err
		}
		result.Prompt = info
	}
	return result, nil
}

func printPersonaCreate(cmd *cobra.Command, r *personaCreateJSON) {
	out := cmd.OutOrStdout()
	p := r.Persona
	fmt.Fprintf(out, "  Created persona: %s (%s)\n", p.DisplayName, p.ID)
	if p.TTS.Provider != "" || p.TTS.Voice != "" {
		fmt.Fprintf(out, "  TTS: %s %s\n", p.TTS.Provider, p.TTS.Voice)
	}
	if r.Prompt != nil {
		fmt.Fprintf(out, "  System prompt: %s\n", r.Prompt.Path)
	}
	if r.Activated {
		fmt.Fprintf(out, "  Active now. Edit in TUI (Personas → e) or under %s\n", persona.Dir())
		return
	}
	fmt.Fprintf(out, "  Not active. Run `samantha persona use %s` to switch to it.\n", p.ID)
}
