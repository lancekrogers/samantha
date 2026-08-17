package cmd

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

// configValuesPayload is `config get --json` with no key: every setting's
// effective value and where it came from.
//
// The values are the file's and the defaults' — not the persona overlay's.
// That is deliberate: these are the values `config set` will change. The
// overlay is reported separately in the persona block so a front end can say
// "the persona wins here" without lying about what the file holds.
type configValuesPayload struct {
	SchemaVersion int                        `json:"schema_version"`
	ConfigFile    string                     `json:"config_file"`
	Values        map[string]config.KeyValue `json:"values"`
	Persona       *configPersonaPayload      `json:"persona,omitempty"`
}

// configValuePayload is `config get <key> --json`.
type configValuePayload struct {
	SchemaVersion       int              `json:"schema_version"`
	Key                 string           `json:"key"`
	Value               any              `json:"value"`
	Type                config.ValueType `json:"type"`
	Source              string           `json:"source"`
	RestartRequired     bool             `json:"restart_required"`
	OverriddenByPersona bool             `json:"overridden_by_persona"`
}

// newConfigGetCmd builds the get subcommand.
func newConfigGetCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "get [key]",
		Short: "Show effective configuration values (read-only)",
		Long: "Show what the config file and defaults hold, with the source of each\n" +
			"value (default, file, or env). With no key, every setting is reported.\n\n" +
			"Values are the app-level ones — the ones `config set` writes. Where the\n" +
			"active persona overrides a key, the persona block names it rather than\n" +
			"the value being silently replaced.\n\n" +
			"Reads only: this command never writes to the install root.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				return runConfigGetKey(cmd, args[0], asJSON)
			}
			return runConfigGetAll(cmd, asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output machine-readable JSON")
	return cmd
}

func runConfigGetAll(cmd *cobra.Command, asJSON bool) error {
	payload := configValuesPayload{
		SchemaVersion: config.SchemaVersion,
		ConfigFile:    config.ConfigFile(),
		Values:        config.Values(),
		Persona:       personaOverlay(),
	}
	if asJSON {
		return writeJSON(cmd, payload)
	}
	printValues(cmd, payload)
	return nil
}

func runConfigGetKey(cmd *cobra.Command, key string, asJSON bool) error {
	value, valueType, ok := config.ValueFor(key)
	if !ok {
		return failConfig(cmd, asJSON, &config.SetError{
			Code:       config.CodeUnknownKey,
			Key:        key,
			Message:    fmt.Sprintf("unknown config key %q", key),
			DidYouMean: config.SuggestKeys(key),
		})
	}
	spec, _ := config.SpecFor(key)
	payload := configValuePayload{
		SchemaVersion:       config.SchemaVersion,
		Key:                 spec.Key,
		Value:               value.Value,
		Type:                valueType,
		Source:              value.Source,
		RestartRequired:     spec.RestartRequired,
		OverriddenByPersona: personaOverridesKey(spec.Key),
	}
	if asJSON {
		return writeJSON(cmd, payload)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  %s %s %s %s\n",
		keyStyle.Render(payload.Key),
		dimStyle.Render("="),
		valueStyle.Render(maskSecret(payload.Key, fmt.Sprint(payload.Value))),
		dimStyle.Render("("+payload.Source+")"))
	return nil
}

func printValues(cmd *cobra.Command, payload configValuesPayload) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "\n  %s\n", titleStyle.Render("Samantha Configuration"))
	fmt.Fprintf(out, "  %s\n\n", dimStyle.Render("Config file: "+payload.ConfigFile))
	keys := make([]string, 0, len(payload.Values))
	for key := range payload.Values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		entry := payload.Values[key]
		fmt.Fprintf(out, "  %s %s %s %s\n",
			keyStyle.Render(key),
			dimStyle.Render("="),
			valueStyle.Render(maskSecret(key, fmt.Sprint(entry.Value))),
			dimStyle.Render("("+entry.Source+")"))
	}
	if payload.Persona != nil {
		fmt.Fprintf(out, "\n  %s %s\n",
			dimStyle.Render("overridden by persona"),
			valueStyle.Render(payload.Persona.DisplayName))
		for _, key := range payload.Persona.Overrides {
			fmt.Fprintf(out, "    %s\n", keyStyle.Render(key))
		}
	}
	fmt.Fprintln(out)
}
