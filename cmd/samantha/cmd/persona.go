package cmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/persona"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

func init() {
	rootCmd.AddCommand(newPersonaCmd())
}

// newPersonaCmd builds the persona command tree fresh on every call. Cobra
// flag state is sticky per command instance, so constructing rather than
// sharing package-level vars keeps a test run from inheriting the previous
// run's --json or Changed() flags.
func newPersonaCmd() *cobra.Command {
	cmd := personaCmd()
	cmd.AddCommand(personaListCmd())
	cmd.AddCommand(personaShowCmd())
	cmd.AddCommand(personaUseCmd())
	cmd.AddCommand(personaCreateCmd())
	cmd.AddCommand(personaEditCmd())
	cmd.AddCommand(personaDeleteCmd())
	return cmd
}

func personaCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "persona",
		Short: "List and switch voice agent personas",
		Long: `Manage voice agent personas (festival-voice multi-persona profiles).

Profiles live under:
  ~/.obey/agents/voice/<AppSlug>/personas/<id>/persona.yaml

The active persona overlays agent_name, persona prompt name, and tts_voice
on config load. Prompt bodies stay in the prompts catalog.`,
	}
}

func personaListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List installed persona profiles",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			list, err := persona.List()
			if err != nil {
				return err
			}
			if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
				type row struct {
					ID          string `json:"id"`
					DisplayName string `json:"display_name"`
					Provider    string `json:"provider,omitempty"`
					Voice       string `json:"voice,omitempty"`
					Prompt      string `json:"prompt"`
					Builtin     bool   `json:"builtin"`
					Active      bool   `json:"active"`
					Path        string `json:"path"`
				}
				active := persona.ActiveID(cfg)
				out := make([]row, 0, len(list))
				for _, p := range list {
					out = append(out, row{
						ID:          p.ID,
						DisplayName: p.DisplayName,
						Provider:    p.TTS.Provider,
						Voice:       p.TTS.Voice,
						Prompt:      p.Prompts.Persona,
						Builtin:     p.Builtin,
						Active:      p.ID == active,
						Path:        p.Path,
					})
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			if len(list) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "  No personas found. Load config once to migrate defaults.")
				return nil
			}
			active := persona.ActiveID(cfg)
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(w, "  ID\tNAME\tPROVIDER\tVOICE\tPROMPT\tACTIVE")
			for _, p := range list {
				mark := ""
				if p.ID == active {
					mark = "*"
				}
				fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n",
					p.ID, p.DisplayName, p.TTS.Provider, p.TTS.Voice, p.Prompts.Persona, mark)
			}
			_ = w.Flush()
			fmt.Fprintf(cmd.OutOrStdout(), "\n  Profiles dir: %s\n", persona.Dir())
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "Emit JSON")
	return cmd
}

func personaShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show [id]",
		Short: "Show one persona profile (default: active)",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runPersonaShow,
	}
	cmd.Flags().Bool("json", false, "Emit JSON")
	cmd.Flags().Bool("with-prompt", false, "Include the persona's system prompt document (and its body)")
	return cmd
}

func runPersonaShow(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	asJSON, _ := cmd.Flags().GetBool("json")
	result, err := describePersona(cmd, args)
	if err != nil {
		if asJSON {
			return emitJSONError(cmd, err)
		}
		return err
	}
	if asJSON {
		return encodeJSON(cmd, result)
	}
	printPersonaShow(cmd, result)
	return nil
}

// describePersona reports one persona in the same envelope `persona edit`
// returns, so a caller decodes one shape for both verbs. --with-prompt adds
// the identity body an editor needs to load the text without ever parsing
// samantha.prompt.v1 itself.
func describePersona(cmd *cobra.Command, args []string) (*personaResultJSON, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	id := persona.ActiveID(cfg)
	if len(args) == 1 {
		id = args[0]
	}
	p, err := loadPersonaForCLI(id)
	if err != nil {
		return nil, err
	}
	result := &personaResultJSON{
		Persona: p,
		Changed: []string{},
		Active:  p.ID == persona.ActiveID(cfg),
		Applies: defaultApplies(),
	}
	if withPrompt, _ := cmd.Flags().GetBool("with-prompt"); withPrompt {
		doc, err := persona.DescribePrompt(p)
		if err != nil {
			return nil, err
		}
		body := doc.Body
		result.Prompt = &promptJSON{
			Name:       doc.Name,
			Path:       doc.Path,
			Source:     doc.Source,
			Structured: doc.Structured,
			Hash:       doc.Hash,
			Body:       &body,
		}
	}
	return result, nil
}

func printPersonaShow(cmd *cobra.Command, r *personaResultJSON) {
	out := cmd.OutOrStdout()
	p := r.Persona
	fmt.Fprintf(out, "\n  %s\n", titleStyle.Render("Persona: "+p.ID))
	fmt.Fprintf(out, "  display_name    %s\n", p.DisplayName)
	fmt.Fprintf(out, "  builtin         %v\n", p.Builtin)
	fmt.Fprintf(out, "  active          %v\n", r.Active)
	if p.Brain.Provider != "" || p.Brain.Model != "" {
		fmt.Fprintf(out, "  brain.provider  %s\n", p.Brain.Provider)
		fmt.Fprintf(out, "  brain.model     %s\n", p.Brain.Model)
	}
	fmt.Fprintf(out, "  tts.provider    %s\n", p.TTS.Provider)
	fmt.Fprintf(out, "  tts.voice       %s\n", p.TTS.Voice)
	if p.TTS.Tier != "" {
		fmt.Fprintf(out, "  tts.tier        %s\n", p.TTS.Tier)
	}
	fmt.Fprintf(out, "  prompts.persona %s\n", p.Prompts.Persona)
	if p.Prompts.Turn != "" {
		fmt.Fprintf(out, "  prompts.turn    %s\n", p.Prompts.Turn)
	}
	fmt.Fprintf(out, "  path            %s\n", p.Path)
	if r.Prompt != nil {
		fmt.Fprintf(out, "  prompt.source   %s\n", r.Prompt.Source)
		fmt.Fprintf(out, "  prompt.hash     %s\n", r.Prompt.Hash)
		if r.Prompt.Body != nil && *r.Prompt.Body != "" {
			fmt.Fprintf(out, "\n%s\n", *r.Prompt.Body)
		}
	}
	fmt.Fprintln(out)
}

func personaUseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "use <id>",
		Short: "Set the active persona and persist it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if err := persona.Use(cfg, args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  Active persona: %s (%s)\n", cfg.ActivePersona, cfg.AgentName)
			if cfg.TTSProvider != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "  TTS provider:   %s\n", cfg.TTSProvider)
			}
			voice := cfg.TTSVoice
			if strings.EqualFold(strings.TrimSpace(cfg.TTSProvider), "qwen3-tts") && cfg.QwenTTSVoice != "" {
				voice = cfg.QwenTTSVoice
			}
			if voice != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "  Voice:          %s\n", voice)
			}
			return nil
		},
	}
	return cmd
}

func personaCreateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create <display-name>",
		Short: "Create a new persona and make it active",
		Long: `Create a user persona under personas/<id>/persona.yaml.

The id is derived from the display name (kebab-case). TTS provider/voice are
cloned from the current config. Pass --prompt to write a custom system prompt
document under prompts/persona/<id>.yaml (supports {agent_name}).`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			name := strings.TrimSpace(strings.Join(args, " "))
			prompt, _ := cmd.Flags().GetString("prompt")
			p, err := persona.CreateAndUseWithOpts(cfg, persona.CreateOpts{
				DisplayName:  name,
				SystemPrompt: prompt,
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  Created persona: %s (%s)\n", p.DisplayName, p.ID)
			if p.TTS.Provider != "" || p.TTS.Voice != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "  TTS: %s %s\n", p.TTS.Provider, p.TTS.Voice)
			}
			if strings.TrimSpace(prompt) != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "  System prompt: prompts/persona/%s.yaml\n", p.ID)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  Active now. Edit in TUI (Personas → e) or under %s\n", persona.Dir())
			return nil
		},
	}
	cmd.Flags().String("prompt", "", "Custom system prompt body (writes prompts/persona/<id>.yaml)")
	return cmd
}
