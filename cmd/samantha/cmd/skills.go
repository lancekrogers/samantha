package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/pkg/voiceagent/brain"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	"github.com/lancekrogers/samantha/pkg/voiceagent/skills"
)

// skillsSchema names the skills list --json envelope version. This is a new
// command with no back-compat constraint, but the schema still lets a
// client detect a future breaking change.
const skillsSchema = "samantha.skills.v1"

// newSkillsCmd builds the `samantha skills` command group: discovery listing
// and per-skill enable/disable over the same four search roots the agent
// itself scans.
func newSkillsCmd(loadConfig configLoader) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Discover and manage Agent Skills (SKILL.md folders)",
		Long: `Samantha loads Agent Skills from four search roots, in precedence order
(first match wins when the same skill name appears in more than one root):

  1. <work-dir>/.agents/skills       — project skills
  2. nearest ancestor .agents/skills — workspace/project-root skills
  3. ~/.agents/skills                — user skills shared across tools
  4. skills_dir                      — Samantha's own skills root
                                        (default: <install_root>/skills)

.claude/skills is never scanned — Claude Code owns that path.

Skills are an Ollama-only feature today: Claude and Grok run their own
native tool and skill routing (see "provider_supported" in --json output).`,
	}
	cmd.AddCommand(newSkillsListCmd(loadConfig), newSkillsEnableCmd(loadConfig), newSkillsDisableCmd(loadConfig))
	return cmd
}

// skillsRootRow is one search root's scan result.
type skillsRootRow struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Skills int    `json:"skills"`
}

// skillsRow is one discovered skill plus the context a front end needs to
// render an honest row: where it won from, whether anything shadows it, and
// whether the current config would actually let the model see it.
type skillsRow struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Path        string   `json:"path"`
	Root        string   `json:"root"`
	Active      bool     `json:"active"`
	Disabled    bool     `json:"disabled"`
	Shadowed    []string `json:"shadowed"`
}

// skillsListEnvelope is the `skills list --json` object envelope (A1): the
// UI needs the global context (provider, thresholds, roots) to render an
// honest screen, not just the skill rows.
type skillsListEnvelope struct {
	Schema              string          `json:"schema"`
	WorkDir             string          `json:"work_dir"`
	SkillsEnabled       bool            `json:"skills_enabled"`
	BrainProvider       string          `json:"brain_provider"`
	ProviderSupported   bool            `json:"provider_supported"`
	EmbeddingModel      string          `json:"embedding_model"`
	SimilarityThreshold float64         `json:"similarity_threshold"`
	Roots               []skillsRootRow `json:"roots"`
	Skills              []skillsRow     `json:"skills"`
}

func newSkillsListCmd(loadConfig configLoader) *cobra.Command {
	var (
		jsonOut bool
		workDir string
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List discovered skills across all four search roots",
		Long: `List every Agent Skill discovered under the four search roots (project,
workspace ancestor, user, and skills_dir), first-root-wins on duplicate
names. Pass --work-dir to match what the agent itself sees: roots 1-2
resolve against the agent's working directory, which for a spawned serve
process is the samantha binary's directory, not the caller's cwd.`,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if workDir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("resolve --work-dir: %w", err)
				}
				workDir = wd
			}

			configured := cfg.SkillsDir
			if configured == "" {
				configured = config.SkillsDir()
			}
			dirs := skills.DefaultSearchPaths(workDir, configured)

			loader := skills.Loader{Dirs: dirs, Disabled: cfg.SkillsDisabled}
			discovered, err := loader.Discover(cmd.Context())
			if err != nil {
				return err
			}

			roots := make([]skillsRootRow, 0, len(dirs))
			for _, dir := range dirs {
				row := skillsRootRow{Path: dir}
				if info, statErr := os.Stat(dir); statErr == nil && info.IsDir() {
					row.Exists = true
					rootCatalog, err := skills.Loader{Dir: dir}.Catalog(cmd.Context())
					if err != nil {
						return err
					}
					row.Skills = len(rootCatalog)
				}
				roots = append(roots, row)
			}

			providerSupported := brain.SkillMenuApplies(cfg.BrainProvider)
			rows := make([]skillsRow, 0, len(discovered))
			for _, d := range discovered {
				shadowed := d.Shadowed
				if shadowed == nil {
					shadowed = []string{}
				}
				rows = append(rows, skillsRow{
					Name:        d.Name,
					Description: d.Description,
					Path:        d.Dir,
					Root:        d.Root,
					Active:      cfg.SkillsEnabled && providerSupported && !d.Disabled,
					Disabled:    d.Disabled,
					Shadowed:    shadowed,
				})
			}

			env := skillsListEnvelope{
				Schema:              skillsSchema,
				WorkDir:             workDir,
				SkillsEnabled:       cfg.SkillsEnabled,
				BrainProvider:       cfg.BrainProvider,
				ProviderSupported:   providerSupported,
				EmbeddingModel:      cfg.OllamaEmbeddingModel,
				SimilarityThreshold: cfg.SkillsSimilarityThreshold,
				Roots:               roots,
				Skills:              rows,
			}

			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(env)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "work dir: %s\n", env.WorkDir)
			fmt.Fprintf(cmd.OutOrStdout(), "provider: %s (skills supported: %t)\n\n", env.BrainProvider, env.ProviderSupported)
			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No skills discovered.")
			} else {
				w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "NAME\tROOT\tACTIVE\tDISABLED")
				for _, r := range rows {
					fmt.Fprintf(w, "%s\t%s\t%t\t%t\n", r.Name, r.Root, r.Active, r.Disabled)
				}
				if err := w.Flush(); err != nil {
					return err
				}
			}
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), "ROOTS (precedence order):")
			for _, r := range roots {
				status := "folder not present"
				if r.Exists {
					status = fmt.Sprintf("%d skill(s)", r.Skills)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "  %s: %s\n", r.Path, status)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print the discovery envelope as JSON")
	cmd.Flags().StringVar(&workDir, "work-dir", "", "Working directory to resolve roots 1-2 against (default: current directory)")
	return cmd
}

// skillsToggleResult is the `skills enable|disable --json` payload.
type skillsToggleResult struct {
	Name            string   `json:"name"`
	Disabled        bool     `json:"disabled"`
	Active          bool     `json:"active"`
	SkillsDisabled  []string `json:"skills_disabled"`
	RestartRequired bool     `json:"restart_required"`
}

func newSkillsEnableCmd(loadConfig configLoader) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:           "enable <name>",
		Short:         "Re-enable a skill previously disabled with `skills disable`",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSkillsToggle(cmd, loadConfig, args[0], false, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print the result as JSON")
	return cmd
}

func newSkillsDisableCmd(loadConfig configLoader) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:           "disable <name>",
		Short:         "Disable a discovered skill without deleting its folder",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSkillsToggle(cmd, loadConfig, args[0], true, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print the result as JSON")
	return cmd
}

// runSkillsToggle implements both enable and disable: disable refuses a
// name discovery never found (idempotent when already disabled); enable is
// a no-op when the name is not currently disabled, even if its folder is
// gone. Both persist through config.SetAndSave — never ValidateAndSet/
// `config set`, whose coerceValue only handles bool/int/float64/string and
// would reject a list.
func runSkillsToggle(cmd *cobra.Command, loadConfig configLoader, name string, disable, jsonOut bool) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	configured := cfg.SkillsDir
	if configured == "" {
		configured = config.SkillsDir()
	}
	dirs := skills.DefaultSearchPaths(workDir, configured)

	// Discover (not Catalog) so an already-disabled name is still found —
	// Catalog omits disabled skills, which would break idempotent re-disable.
	discovered, err := (skills.Loader{Dirs: dirs}).Discover(cmd.Context())
	if err != nil {
		return err
	}
	found := false
	for _, d := range discovered {
		if strings.EqualFold(strings.TrimSpace(d.Name), strings.TrimSpace(name)) {
			found = true
			break
		}
	}
	if disable && !found {
		return fmt.Errorf("no skill named %q was discovered (roots: %s)", name, strings.Join(dirs, ", "))
	}

	updated := updateSkillsDisabledList(cfg.SkillsDisabled, name, disable)
	if err := config.SetAndSave("skills_disabled", updated); err != nil {
		return err
	}

	nameKey := strings.ToLower(strings.TrimSpace(name))
	nowDisabled := slices.Contains(updated, nameKey)
	providerSupported := brain.SkillMenuApplies(cfg.BrainProvider)
	active := cfg.SkillsEnabled && providerSupported && !nowDisabled

	if jsonOut {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(skillsToggleResult{
			Name:            name,
			Disabled:        nowDisabled,
			Active:          active,
			SkillsDisabled:  updated,
			RestartRequired: true,
		})
	}

	verb := "Enabled"
	if disable {
		verb = "Disabled"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s %q — restart the agent (or start a new conversation) to apply.\n", verb, name)
	return nil
}

// updateSkillsDisabledList adds or removes name from current and returns the
// result as sorted-unique-lowercased-trimmed, so repeated writes are stable
// no matter what casing or order the stored list started with.
func updateSkillsDisabledList(current []string, name string, disable bool) []string {
	set := map[string]struct{}{}
	for _, c := range current {
		c = strings.ToLower(strings.TrimSpace(c))
		if c == "" {
			continue
		}
		set[c] = struct{}{}
	}
	key := strings.ToLower(strings.TrimSpace(name))
	if disable {
		set[key] = struct{}{}
	} else {
		delete(set, key)
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
