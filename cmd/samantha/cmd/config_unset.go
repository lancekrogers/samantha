package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

// configUnsetPayload is `config unset --json`. It is `config set`'s payload:
// a front end that already decodes one write result decodes both, and `value`
// carries the default the key falls back to.
type configUnsetPayload struct {
	SchemaVersion int `json:"schema_version"`
	config.SetResult
}

// newConfigUnsetCmd builds the unset subcommand.
func newConfigUnsetCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "unset <key>",
		Short: "Remove one setting so its default applies again",
		Long: "Remove one setting from config.yaml. The key's built-in default — or its\n" +
			"environment variable, if it has one — applies again, and `config get`\n" +
			"reports the value as coming from `default` rather than `file`.\n\n" +
			"This is not the same as writing an empty value. `config set stt_mode \"\"`\n" +
			"stores an empty string, which pins the key; `config unset stt_mode`\n" +
			"removes it, so a future default or an exported STT_MODE is free to take\n" +
			"effect. Keys where \"\" is itself a real value are marked `allows_empty`\n" +
			"in `config schema`.\n\n" +
			"Only that key's lines are removed: comments, blank lines and key order in\n" +
			"config.yaml survive, and a timestamped .bak is kept. Removing a key the\n" +
			"file does not hold changes nothing and reports changed: false. Keys owned\n" +
			"by another command (`persona use`, `meeting destinations`) are refused,\n" +
			"naming the verb that owns them.",
		// The arg count is checked in RunE so a miscall exits 2 (usage) rather
		// than 1 (the operation failed) — a front end must tell them apart.
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return usageError("config unset needs exactly one key, got %d argument(s)", len(args))
			}
			result, err := config.UnsetKeyFile(args[0])
			if err != nil {
				return failConfig(cmd, asJSON, err)
			}
			if asJSON {
				return writeJSON(cmd, configUnsetPayload{
					SchemaVersion: config.SchemaVersion,
					SetResult:     result,
				})
			}
			printUnsetResult(cmd, result)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output machine-readable JSON")
	return cmd
}

func printUnsetResult(cmd *cobra.Command, result config.SetResult) {
	out := cmd.OutOrStdout()
	if !result.Changed {
		fmt.Fprintf(out, "  %s %s\n",
			keyStyle.Render(result.Key),
			dimStyle.Render("is not set in config.yaml"))
		return
	}
	fmt.Fprintf(out, "  %s %s %s %s\n",
		dimStyle.Render("Unset"),
		keyStyle.Render(result.Key),
		dimStyle.Render("— now"),
		valueStyle.Render(maskSecret(result.Key, fmt.Sprint(result.Value))))
	if result.RestartRequired {
		fmt.Fprintf(out, "  %s\n", dimStyle.Render("Restart the agent to apply this."))
	}
}
