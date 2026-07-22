package cmd

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/persona"
)

func init() {
	rootCmd.AddCommand(personaCmd)
	personaCmd.AddCommand(personaListCmd)
	personaCmd.AddCommand(personaShowCmd)
	personaCmd.AddCommand(personaUseCmd)
}

var personaCmd = &cobra.Command{
	Use:   "persona",
	Short: "List and switch voice agent personas",
	Long: `Manage voice agent personas (festival-voice multi-persona profiles).

Profiles live under:
  ~/.obey/agents/voice/<AppSlug>/personas/<id>/persona.yaml

The active persona overlays agent_name, persona prompt name, and tts_voice
on config load. Prompt bodies stay in the prompts catalog.`,
}

var personaListCmd = &cobra.Command{
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
		fmt.Fprintln(w, "  ID\tNAME\tVOICE\tPROMPT\tACTIVE")
		for _, p := range list {
			mark := ""
			if p.ID == active {
				mark = "*"
			}
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n", p.ID, p.DisplayName, p.TTS.Voice, p.Prompts.Persona, mark)
		}
		_ = w.Flush()
		fmt.Fprintf(cmd.OutOrStdout(), "\n  Profiles dir: %s\n", persona.Dir())
		return nil
	},
}

var personaShowCmd = &cobra.Command{
	Use:   "show [id]",
	Short: "Show one persona profile (default: active)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		id := persona.ActiveID(cfg)
		if len(args) == 1 {
			id = args[0]
		}
		p, err := persona.Load(id)
		if err != nil {
			return err
		}
		if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(p)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "\n  %s\n", titleStyle.Render("Persona: "+p.ID))
		fmt.Fprintf(cmd.OutOrStdout(), "  display_name  %s\n", p.DisplayName)
		fmt.Fprintf(cmd.OutOrStdout(), "  builtin       %v\n", p.Builtin)
		fmt.Fprintf(cmd.OutOrStdout(), "  tts.voice     %s\n", p.TTS.Voice)
		fmt.Fprintf(cmd.OutOrStdout(), "  prompts.persona %s\n", p.Prompts.Persona)
		if p.Prompts.Turn != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "  prompts.turn    %s\n", p.Prompts.Turn)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  path          %s\n\n", p.Path)
		return nil
	},
}

var personaUseCmd = &cobra.Command{
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
		if cfg.TTSVoice != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "  Voice: %s\n", cfg.TTSVoice)
		}
		return nil
	},
}

func init() {
	personaListCmd.Flags().Bool("json", false, "Emit JSON")
	personaShowCmd.Flags().Bool("json", false, "Emit JSON")
}
