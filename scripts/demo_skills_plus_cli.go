// Demo: skills + CLI tools (allowed-tools is a soft hint, not a sandbox).
//
//	go run ./scripts/demo_skills_plus_cli.go
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lancekrogers/samantha/internal/skills"
)

func main() {
	fmt.Println("=== Samantha demo: skills + CLI tools ===")
	fmt.Println()

	// 1) Load fixture skill (has allowed-tools: Read list_files)
	fixture := filepath.Join("internal", "skills", "testdata", "skills")
	catalog, err := skills.Loader{Dir: fixture}.Catalog(context.Background())
	if err != nil {
		fail("catalog: %v", err)
	}
	if len(catalog) != 1 || catalog[0].Name != "hello" {
		fail("want hello skill, got %#v", catalog)
	}
	sk := catalog[0]
	fmt.Printf("1) Loaded skill %q from fixture catalog\n", sk.Name)
	fmt.Printf("   description: %s\n", sk.Description)
	fmt.Printf("   allowed-tools (soft hint): %v\n", sk.AllowedTools)
	fmt.Println()

	// 2) Alias mapping for hints / future policy helpers
	fmt.Println("2) Alias mapping (helpers only — not a sandbox gate)")
	for _, pair := range []struct{ tool, note string }{
		{"read_file", "Read alias matches"},
		{"write_file", "NOT listed — still a first-class CLI tool"},
		{"run_command", "Bash(...) maps here"},
	} {
		ok := skills.ToolAllowed(pair.tool, sk.AllowedTools)
		fmt.Printf("   ToolAllowed(%q) = %-5v  // %s\n", pair.tool, ok, pair.note)
	}
	fmt.Println()

	// 3) CLI capability after skill load (write_file analogue — not blocked)
	dir, err := os.MkdirTemp("", "samantha-skills-cli-demo-*")
	if err != nil {
		fail("tempdir: %v", err)
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "proof.txt")
	content := "skills playbooks + CLI tools still work after load\n"
	if err := os.WriteFile(out, []byte(content), 0o644); err != nil {
		fail("write: %v", err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		fail("read: %v", err)
	}
	fmt.Println("3) CLI write after skill load (write_file analogue)")
	// Do not print the absolute temp path (machine-local; not useful in demos).
	fmt.Printf("   wrote %s in a temp dir\n", filepath.Base(out))
	fmt.Printf("   content: %q\n", strings.TrimSpace(string(raw)))
	fmt.Println()

	// 4) Contract
	fmt.Println("4) Contract")
	fmt.Println("   Skills  = progressive disclosure playbooks (.agents/skills)")
	fmt.Println("   Tools   = list_files / read_file / write_file / run_command (+ read_skill)")
	fmt.Println("   allowed-tools = soft author hint, NOT a hard sandbox")
	fmt.Println("   Safety  = voice_tools_enabled / remote_tools_enabled")
	fmt.Println()
	fmt.Println("OK — skills + CLI tools demo passed")
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FAIL: "+format+"\n", args...)
	os.Exit(1)
}
