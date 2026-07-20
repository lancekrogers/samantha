package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/calibre"
	"github.com/lancekrogers/samantha/internal/config"
)

// newLibraryCmd builds the `samantha library` command group (Calibre catalog).
func newLibraryCmd(loadConfig configLoader) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "library",
		Short: "Browse and search the Calibre ebook library",
		Long: `Browse and query the user's Calibre library via calibredb.

Requires calibre_enabled=true in config (and Calibre installed). The integration
is optional: when disabled, library commands print a short enablement hint.

Examples:
  samantha library list
  samantha library search "cryptography"
  samantha library show 42
  samantha library search "tag:AI" --limit 10 --json`,
	}
	cmd.AddCommand(newLibraryListCmd(loadConfig))
	cmd.AddCommand(newLibrarySearchCmd(loadConfig))
	cmd.AddCommand(newLibraryShowCmd(loadConfig))
	return cmd
}

func newLibraryListCmd(loadConfig configLoader) *cobra.Command {
	var (
		asJSON bool
		limit  int
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Browse books in the Calibre library",
		Long: `List books from the Calibre library without a search filter (title order).

Examples:
  samantha library list
  samantha library list --limit 50 --json`,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, client, err := requireCalibreClient(loadConfig, "library list")
			if err != nil {
				return err
			}
			_ = cfg
			books, err := client.List(cmd.Context(), limit)
			if err != nil {
				return err
			}
			return printBooks(cmd, books, asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print machine-readable JSON")
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum number of books")
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
			_, client, err := requireCalibreClient(loadConfig, "library search")
			if err != nil {
				return err
			}
			books, err := client.Search(cmd.Context(), args[0], limit)
			if err != nil {
				return err
			}
			return printBooks(cmd, books, asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print machine-readable JSON")
	cmd.Flags().IntVar(&limit, "limit", 20, "Maximum number of results")
	return cmd
}

func newLibraryShowCmd(loadConfig configLoader) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show ID",
		Short: "Show metadata for one Calibre book",
		Long: `Show full metadata for a book by Calibre id (title, authors, tags, series,
formats, description).

Examples:
  samantha library show 42
  samantha library show 5 --json`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.Atoi(strings.TrimSpace(args[0]))
			if err != nil || id <= 0 {
				return fmt.Errorf("library show: invalid book id %q", args[0])
			}
			_, client, err := requireCalibreClient(loadConfig, "library show")
			if err != nil {
				return err
			}
			book, err := client.Metadata(cmd.Context(), id)
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(book)
			}
			printBookDetail(cmd, book)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print machine-readable JSON")
	return cmd
}

func requireCalibreClient(loadConfig configLoader, op string) (*config.Config, calibre.Client, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, calibre.Client{}, err
	}
	if !cfg.CalibreEnabled {
		return nil, calibre.Client{}, fmt.Errorf("%s: calibre is disabled; enable with: samantha config calibre_enabled true", op)
	}
	return cfg, calibreClientFromConfig(cfg), nil
}

func printBooks(cmd *cobra.Command, books []calibre.Book, asJSON bool) error {
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
}

func printBookDetail(cmd *cobra.Command, b calibre.Book) {
	out := cmd.OutOrStdout()
	authors := strings.Join(b.Authors, ", ")
	if authors == "" {
		authors = "(unknown author)"
	}
	fmt.Fprintf(out, "  ID:       %d\n", b.ID)
	fmt.Fprintf(out, "  Title:    %s\n", b.Title)
	fmt.Fprintf(out, "  Authors:  %s\n", authors)
	if b.Series != "" {
		fmt.Fprintf(out, "  Series:   %s\n", b.Series)
	}
	if len(b.Tags) > 0 {
		fmt.Fprintf(out, "  Tags:     %s\n", strings.Join(b.Tags, ", "))
	}
	if b.PubDate != "" {
		fmt.Fprintf(out, "  Pubdate:  %s\n", b.PubDate)
	}
	fmt.Fprintf(out, "  Formats:  %s\n", formatExts(b.Formats))
	for _, p := range b.Formats {
		fmt.Fprintf(out, "    %s\n", p)
	}
	if blurb := calibre.PlainComments(b.Comments); blurb != "" {
		fmt.Fprintf(out, "  Description:\n    %s\n", blurb)
	}
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
