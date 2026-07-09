package cmd

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/prompts"
)

var (
	promptsListJSON bool
	promptsShowJSON bool
)

var promptsCmd = &cobra.Command{
	Use:   "prompts",
	Short: "Inspect prompt documents (read-only)",
}

var promptsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List embedded and user prompt documents",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		entries, err := prompts.Catalog(config.PromptsDir())
		if err != nil {
			return err
		}
		if promptsListJSON {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(entries)
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "KIND\tNAME\tSOURCE\tHASH")
		for _, e := range entries {
			source := string(e.Source)
			if e.Path != "" {
				source = e.Path
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", e.Kind, e.Name, source, e.Hash[:12])
		}
		return w.Flush()
	},
}

var promptsShowCmd = &cobra.Command{
	Use:   "show <kind>",
	Short: "Show the assembled prompt for a kind (persona, turn, ...)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		kind := prompts.Kind(args[0])
		userDir := config.PromptsDir()

		entry, err := prompts.Describe(userDir, kind, cfg.Persona)
		if err != nil {
			return err
		}
		doc, err := (prompts.Resolver{UserDir: userDir}).Resolve(kind, cfg.Persona)
		if err != nil {
			return err
		}
		text, err := prompts.ResolvePlaceholders(doc.Assemble(), []string{"agent_name"}, map[string]string{"agent_name": cfg.AgentName})
		if err != nil {
			return err
		}

		if promptsShowJSON {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
				"kind":   entry.Kind,
				"name":   entry.Name,
				"source": entry.Source,
				"path":   entry.Path,
				"hash":   entry.Hash,
				"text":   text,
			})
		}

		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "kind:   %s\nname:   %s\nsource: %s\n", entry.Kind, entry.Name, entry.Source)
		if entry.Path != "" {
			fmt.Fprintf(out, "path:   %s\n", entry.Path)
		}
		fmt.Fprintf(out, "hash:   %s\n\n%s\n", entry.Hash, text)
		return nil
	},
}

func init() {
	promptsListCmd.Flags().BoolVar(&promptsListJSON, "json", false, "Output machine-readable JSON")
	promptsShowCmd.Flags().BoolVar(&promptsShowJSON, "json", false, "Output machine-readable JSON")
	promptsCmd.AddCommand(promptsListCmd)
	promptsCmd.AddCommand(promptsShowCmd)
	rootCmd.AddCommand(promptsCmd)
}
