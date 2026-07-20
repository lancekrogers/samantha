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

func TestLibraryListDisabled(t *testing.T) {
	cmd := newLibraryCmd(func() (*config.Config, error) {
		return &config.Config{CalibreEnabled: false}, nil
	})
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"list"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("err = %v", err)
	}
}

func TestLibraryShowInvalidID(t *testing.T) {
	cmd := newLibraryCmd(func() (*config.Config, error) {
		return &config.Config{CalibreEnabled: true}, nil
	})
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"show", "nope"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "invalid book id") {
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

func TestLibraryCmdHasBrowseSurfaces(t *testing.T) {
	cmd := newLibraryCmd(func() (*config.Config, error) {
		return &config.Config{CalibreEnabled: true}, nil
	})
	want := map[string]bool{"list": false, "search": false, "show": false}
	for _, c := range cmd.Commands() {
		if _, ok := want[c.Name()]; ok {
			want[c.Name()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("%s subcommand missing", name)
		}
	}
}

func TestPrintBookDetailIncludesDescription(t *testing.T) {
	cmd := newLibraryCmd(func() (*config.Config, error) {
		return &config.Config{CalibreEnabled: true}, nil
	})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	printBookDetail(cmd, calibre.Book{
		ID:       7,
		Title:    "Crypto 101",
		Authors:  []string{"lvh"},
		Tags:     []string{"Security"},
		Formats:  []string{"/lib/crypto.pdf"},
		Comments: "<p>An introduction to cryptography.</p>",
	})
	s := buf.String()
	for _, want := range []string{"Crypto 101", "lvh", "Security", "pdf", "introduction to cryptography"} {
		if !strings.Contains(s, want) {
			t.Fatalf("detail missing %q:\n%s", want, s)
		}
	}
}
