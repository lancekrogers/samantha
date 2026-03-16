package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/samantha/internal/config"
)

var configCmd = &cobra.Command{
	Use:   "config [key] [value]",
	Short: "View or set configuration values",
	Args:  cobra.MaximumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		switch len(args) {
		case 0:
			// Show all config
			fmt.Printf("\n  Samantha Configuration\n")
			fmt.Printf("  Config file: %s\n\n", config.ConfigFile())
			for _, key := range config.AllKeys() {
				val := config.Get(key)
				display := maskSecret(key, fmt.Sprint(val))
				fmt.Printf("  %s = %s\n", key, display)
			}
			fmt.Println()
		case 1:
			// Show one value
			val := config.Get(args[0])
			if val == nil {
				return fmt.Errorf("unknown key: %s", args[0])
			}
			display := maskSecret(args[0], fmt.Sprint(val))
			fmt.Printf("  %s = %s\n", args[0], display)
		case 2:
			// Set value
			if err := config.SetAndSave(args[0], args[1]); err != nil {
				return err
			}
			fmt.Printf("  Set %s = %s\n", args[0], maskSecret(args[0], args[1]))
		}
		return nil
	},
}

func maskSecret(key, value string) string {
	if strings.Contains(strings.ToLower(key), "key") && len(value) > 8 {
		return value[:4] + "..." + value[len(value)-4:]
	}
	return value
}

func init() {
	rootCmd.AddCommand(configCmd)
}
