package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/config"
)

var configCmd = &cobra.Command{
	Use:   "config [key] [value]",
	Short: "View or set configuration values",
	Args:  cobra.MaximumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		switch len(args) {
		case 0:
			// Show all config
			fmt.Printf("\n  %s\n", titleStyle.Render("Samantha Configuration"))
			fmt.Printf("  %s\n\n", dimStyle.Render("Config file: "+config.ConfigFile()))
			for _, key := range config.AllKeys() {
				val := config.Get(key)
				display := maskSecret(key, fmt.Sprint(val))
				fmt.Printf("  %s %s %s\n", keyStyle.Render(key), dimStyle.Render("="), valueStyle.Render(display))
			}
			fmt.Println()
		case 1:
			// Show one value
			val := config.Get(args[0])
			if val == nil {
				return fmt.Errorf("unknown key: %s", args[0])
			}
			display := maskSecret(args[0], fmt.Sprint(val))
			fmt.Printf("  %s %s %s\n", keyStyle.Render(args[0]), dimStyle.Render("="), valueStyle.Render(display))
		case 2:
			// Set value
			if err := config.ValidateAndSet(args[0], args[1]); err != nil {
				return err
			}
			fmt.Printf("  %s %s %s %s\n", dimStyle.Render("Set"), keyStyle.Render(args[0]), dimStyle.Render("="), valueStyle.Render(maskSecret(args[0], args[1])))
		}
		return nil
	},
}

func newConfigMigrateCmd(load configLoader, configPath func() string) *cobra.Command {
	var dryRun bool
	var write bool
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Preview safe config migrations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dryRun == write {
				return errors.New("config migrate requires exactly one of --dry-run or --write")
			}
			cfg, err := load()
			if err != nil {
				return err
			}
			if dryRun {
				proposal, err := config.ProposeSTTConfigMigration(cfg, configPath())
				if err != nil {
					return err
				}
				printConfigMigrationDryRun(cmd, proposal)
				return nil
			}

			result, err := config.WriteSTTConfigMigration(cfg, configPath())
			if err != nil {
				return err
			}
			printConfigMigrationWrite(cmd, result)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview migration without writing files")
	cmd.Flags().BoolVar(&write, "write", false, "Apply migration with a backup")
	return cmd
}

func printConfigMigrationDryRun(cmd *cobra.Command, proposal config.STTConfigMigrationProposal) {
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "Config migration dry run")
	fmt.Fprintf(out, "config_path: %s\n", proposal.ConfigPath)
	fmt.Fprintf(out, "current_alias: %s\n", proposal.CurrentAlias)
	fmt.Fprintf(out, "proposed_stt_provider: %s\n", proposal.ProposedProvider)
	fmt.Fprintf(out, "proposed_stt_mode: %s\n", proposal.ProposedMode)
	fmt.Fprintf(out, "no_op: %t\n", proposal.Noop)
	fmt.Fprintln(out, "would_write: false")
}

func printConfigMigrationWrite(cmd *cobra.Command, result config.STTConfigMigrationResult) {
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "Config migration write")
	fmt.Fprintf(out, "config_path: %s\n", result.ConfigPath)
	fmt.Fprintf(out, "current_alias: %s\n", result.CurrentAlias)
	fmt.Fprintf(out, "proposed_stt_provider: %s\n", result.ProposedProvider)
	fmt.Fprintf(out, "proposed_stt_mode: %s\n", result.ProposedMode)
	fmt.Fprintf(out, "no_op: %t\n", result.Noop)
	fmt.Fprintf(out, "wrote: %t\n", result.Wrote)
	if result.BackupPath != "" {
		fmt.Fprintf(out, "backup_path: %s\n", result.BackupPath)
	}
}

func maskSecret(key, value string) string {
	if strings.Contains(strings.ToLower(key), "key") && len(value) > 8 {
		return value[:4] + "..." + value[len(value)-4:]
	}
	return value
}

func init() {
	configCmd.AddCommand(newConfigMigrateCmd(config.Load, config.ConfigFile))
	rootCmd.AddCommand(configCmd)
}
