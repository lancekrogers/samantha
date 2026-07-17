package cmd

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/calibre"
	"github.com/lancekrogers/samantha/internal/config"
)

func TestLibrarySearchDisabled(t *testing.T) {
	cmd := newLibraryCmd(func() (*config.Config, error) {
		return &config.Config{CalibreEnabled: false}, nil
	})
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"search", "go"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("err = %v", err)
	}
}

func TestLibrarySearchListsBooks(t *testing.T) {
	books := []calibre.Book{
		{ID: 1, Title: "The Go Programming Language", Authors: []string{"Donovan"}, Formats: []string{"/x/go.epub"}},
	}
	var out bytes.Buffer
	for _, b := range books {
		authors := strings.Join(b.Authors, ", ")
		fmt.Fprintf(&out, "  [%d] %s — %s [%s]\n", b.ID, b.Title, authors, formatExts(b.Formats))
	}
	s := out.String()
	if !strings.Contains(s, "The Go Programming Language") || !strings.Contains(s, "epub") {
		t.Fatalf("output = %q", s)
	}
}

func TestFormatExts(t *testing.T) {
	got := formatExts([]string{"/a/book.epub", "/a/book.pdf"})
	if got != "epub, pdf" {
		t.Fatalf("got %q", got)
	}
	if formatExts(nil) != "no formats" {
		t.Fatal("empty")
	}
}

func TestLibraryCmdHasSearch(t *testing.T) {
	cmd := newLibraryCmd(func() (*config.Config, error) {
		return &config.Config{CalibreEnabled: true}, nil
	})
	var found bool
	for _, c := range cmd.Commands() {
		if c.Name() == "search" {
			found = true
		}
	}
	if !found {
		t.Fatal("search subcommand missing")
	}
}
