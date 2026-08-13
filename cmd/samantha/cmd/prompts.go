package cmd

import (
	"encoding/json"
	"fmt"
	"github.com/lancekrogers/samantha/internal/persona"
	"github.com/lancekrogers/samantha/pkg/voiceagent/brain"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	"github.com/lancekrogers/samantha/pkg/voiceagent/prompts"
)

var (
	promptsListJSON     bool
	promptsShowJSON     bool
	promptsShowProvider string
	promptsShowPersona  string
)

var promptsCmd = &cobra.Command{
	Use:   "prompts",
	Short: "Inspect prompt documents (read-only)",
}

// promptNameForKind is the catalog name the resolver uses for a kind, matching
// what the brain resolves. Only kind=persona is named by cfg.Persona; passing
// that name for every kind made `prompts show turn` fail for any custom persona
// once the resolver stopped falling back to the embedded default by name.
func promptNameForKind(cfg *config.Config, kind prompts.Kind) string {
	switch kind {
	case prompts.KindPersona:
		return cfg.Persona
	case prompts.KindTurn:
		return cfg.TurnPrompt
	default:
		// Other kinds have no per-persona ref; empty selects the embedded
		// default for that kind when one exists.
		return ""
	}
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

		// --persona previews a profile other than the active one. Resolve the
		// binding rather than writing cfg.Persona: since the multi-agent work,
		// cfg.Persona is the prompt *document* name, not the persona id.
		if id := strings.TrimSpace(promptsShowPersona); id != "" {
			binding, err := persona.ResolveBinding(cfg, id)
			if err != nil {
				return err
			}
			cfg = binding.Config()
		}
		name := promptNameForKind(cfg, kind)

		entry, err := prompts.Describe(userDir, kind, name)
		if err != nil {
			return err
		}
		doc, err := (prompts.Resolver{UserDir: userDir}).Resolve(kind, name)
		if err != nil {
			return err
		}
		text, err := prompts.ResolvePlaceholders(doc.Assemble(), []string{"agent_name"}, map[string]string{"agent_name": cfg.AgentName})
		if err != nil {
			return err
		}

		// --effective renders what a provider actually receives, assembled by the
		// same policy the brain uses. Anything that rebuilt the prompt here
		// independently would drift from reality and quietly reassure the user
		// that a wrong prompt is right.
		if provider := strings.TrimSpace(promptsShowProvider); provider != "" {
			if kind != prompts.KindPersona {
				return fmt.Errorf("--provider applies to the persona prompt (got kind %q)", kind)
			}
			workDir, _ := os.Getwd()
			catalog, _ := brain.SkillsCatalogFor(cmd.Context(), cfg, workDir)
			turn, turnErr := brain.TurnInstructionFor(cfg)
			if turnErr != nil {
				return turnErr
			}
			text = brain.AssembleSystemPrompt(brain.SystemPromptInput{
				Provider: provider,
				Persona:  text,
				WorkDir:  workDir,
				Skills:   catalog,
				Turn:     turn,
				Cfg:      cfg,
			})
			if !brain.TurnGoesInSystemPrompt(provider) {
				text += "\n\n[turn instruction is appended to the user message on this provider, not the system prompt]\n" + turn
			}
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

// promptsNewCmd scaffolds the prompt kinds nothing else can create.
//
// Personas are deliberately excluded: `persona create` already writes
// prompts/persona/<id>.yaml and activates the profile, and two ways to make a
// persona is how the two paths drift.
var promptsNewCmd = &cobra.Command{
	Use:   "new <kind> <name>",
	Short: "Scaffold a turn or compact prompt document",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		kind := prompts.Kind(args[0])
		name := strings.TrimSpace(args[1])

		switch kind {
		case prompts.KindTurn, prompts.KindCompact:
		case prompts.KindPersona:
			return fmt.Errorf("use `samantha persona create <display-name>` for personas — it scaffolds the prompt and activates the profile in one step")
		default:
			return fmt.Errorf("unknown kind %q (want turn or compact)", kind)
		}
		if name == "" {
			return fmt.Errorf("name is required")
		}

		def, err := prompts.Default(kind)
		if err != nil {
			return err
		}
		dir := filepath.Join(config.PromptsDir(), string(kind))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
		path := filepath.Join(dir, name+".yaml")
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s already exists — edit it, or pick another name", path)
		} else if !os.IsNotExist(err) {
			return err
		}

		def.Prompt.Name = name
		def.Metadata.ID = name + "-user"
		def.Metadata.Description = "User-authored " + string(kind) + " prompt"
		data, err := yaml.Marshal(def)
		if err != nil {
			return fmt.Errorf("encoding %s document: %w", kind, err)
		}
		header := "# yaml-language-server: $schema=" + prompts.Schema + "\n"
		if err := os.WriteFile(path, append([]byte(header), data...), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "  Created %s\n", path)
		switch kind {
		case prompts.KindTurn:
			fmt.Fprintf(cmd.OutOrStdout(), "  Point a persona profile at it with prompts.turn: %s\n", name)
		case prompts.KindCompact:
			fmt.Fprintf(cmd.OutOrStdout(), "  Select it with: samantha config compact_prompt %s\n", name)
		}
		return nil
	},
}

func init() {
	promptsListCmd.Flags().BoolVar(&promptsListJSON, "json", false, "Output machine-readable JSON")
	promptsShowCmd.Flags().BoolVar(&promptsShowJSON, "json", false, "Output machine-readable JSON")
	promptsShowCmd.Flags().StringVar(&promptsShowProvider, "provider", "", "Render the effective prompt this provider receives (ollama|claude|grok)")
	promptsShowCmd.Flags().StringVar(&promptsShowPersona, "persona", "", "Preview a persona other than the active one")
	promptsCmd.AddCommand(promptsListCmd)
	promptsCmd.AddCommand(promptsShowCmd)
	promptsCmd.AddCommand(promptsNewCmd)
	rootCmd.AddCommand(promptsCmd)
}
