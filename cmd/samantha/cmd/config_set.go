package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

// configSetPayload is `config set --json`. schema_version rides along so a
// front end can branch on the shape without sniffing fields.
type configSetPayload struct {
	SchemaVersion int `json:"schema_version"`
	config.SetResult
}

// newConfigSetCmd builds the set subcommand.
func newConfigSetCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Change one configuration value",
		Long: "Change one setting. Only that key's line is rewritten: comments, blank\n" +
			"lines and key order in config.yaml survive, and a timestamped .bak is\n" +
			"kept.\n\n" +
			"The value is read according to the key's type (`config schema` lists\n" +
			"them):\n" +
			"  bool          samantha config set barge_in_enabled true\n" +
			"  int / float   samantha config set vad_silence_duration 0.8\n" +
			"  enum          samantha config set tts_provider qwen3-tts\n" +
			"  list<string>  the value is a JSON array — quote it for the shell:\n" +
			"                samantha config set skills_disabled '[\"pdf-fill\",\"calibre\"]'\n" +
			"                samantha config set skills_disabled '[]'\n\n" +
			"Writing a value the file already holds changes nothing and reports\n" +
			"changed: false. Keys owned by another command (`persona use`, `meeting\n" +
			"destinations`) are refused, naming the verb that owns them.",
		// The arg count is checked in RunE so a miscall exits 2 (usage) rather
		// than 1 (the operation failed) — a front end must tell them apart.
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 2 {
				return usageError("config set needs a key and a value, got %d argument(s)", len(args))
			}
			result, err := config.SetKeyFile(args[0], args[1])
			if err != nil {
				return failConfig(cmd, asJSON, err)
			}
			if asJSON {
				return writeJSON(cmd, configSetPayload{
					SchemaVersion: config.SchemaVersion,
					SetResult:     result,
				})
			}
			printSetResult(cmd, result)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output machine-readable JSON")
	return cmd
}

func printSetResult(cmd *cobra.Command, result config.SetResult) {
	out := cmd.OutOrStdout()
	if !result.Changed {
		fmt.Fprintf(out, "  %s %s %s\n",
			keyStyle.Render(result.Key),
			dimStyle.Render("is already"),
			valueStyle.Render(maskSecret(result.Key, fmt.Sprint(result.Value))))
		return
	}
	fmt.Fprintf(out, "  %s %s %s %s\n",
		dimStyle.Render("Set"),
		keyStyle.Render(result.Key),
		dimStyle.Render("="),
		valueStyle.Render(maskSecret(result.Key, fmt.Sprint(result.Value))))
	if result.RestartRequired {
		fmt.Fprintf(out, "  %s\n", dimStyle.Render("Restart the agent to apply this."))
	}
}
