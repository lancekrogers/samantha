package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/calibre"
	"github.com/lancekrogers/samantha/internal/config"
)

// newLibraryCmd builds the `samantha library` command group (Calibre catalog).
func newLibraryCmd(loadConfig configLoader) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "library",
		Short: "Search and inspect the Calibre ebook library",
		Long: `Query the user's Calibre library via calibredb.

Requires calibre_enabled=true in config (and Calibre installed). The integration
is optional: when disabled, library commands print a short enablement hint.

Examples:
  samantha library search "cryptography"
  samantha library search "tag:AI" --limit 10 --json`,
	}
	cmd.AddCommand(newLibrarySearchCmd(loadConfig))
	return cmd
}

func newLibrarySearchCmd(loadConfig configLoader) *cobra.Command {
	var (
		asJSON bool
		limit  int
	)
	cmd := &cobra.Command{
		Use:   "search QUERY",
		Short: "Search the Calibre library",
		Long: `Search the Calibre library with Calibre's search grammar and print matching
books (title, authors, formats). Pass --json for machine-readable output.

Examples:
  samantha library search "cryptography"
  samantha library search 'author:"Norvig"' --limit 5
  samantha library search "tag:AI" --json`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if !cfg.CalibreEnabled {
				return fmt.Errorf("library search: calibre is disabled; enable with: samantha config calibre_enabled true")
			}
			client := calibreClientFromConfig(cfg)
			books, err := client.Search(cmd.Context(), args[0], limit)
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(books)
			}
			if len(books) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "  No books matched.")
				return nil
			}
			for _, b := range books {
				authors := strings.Join(b.Authors, ", ")
				if authors == "" {
					authors = "(unknown author)"
				}
				fmts := formatExts(b.Formats)
				fmt.Fprintf(cmd.OutOrStdout(), "  [%d] %s — %s [%s]\n", b.ID, b.Title, authors, fmts)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print machine-readable JSON")
	cmd.Flags().IntVar(&limit, "limit", 20, "Maximum number of results")
	return cmd
}

func calibreClientFromConfig(cfg *config.Config) calibre.Client {
	return calibre.NewClientFromConfig(
		cfg.CalibreEnabled,
		cfg.CalibreLibraryPath,
		cfg.CalibredbBinary,
		cfg.CalibreConvertBinary,
		cfg.CalibrePreferFormat,
	)
}

func formatExts(paths []string) string {
	if len(paths) == 0 {
		return "no formats"
	}
	seen := make([]string, 0, len(paths))
	for _, p := range paths {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))
		if ext == "" {
			ext = filepath.Base(p)
		}
		seen = append(seen, ext)
	}
	return strings.Join(seen, ", ")
}
