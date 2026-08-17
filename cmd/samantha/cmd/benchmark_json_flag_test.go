//go:build !integration

package cmd

import (
	"testing"

	"github.com/spf13/pflag"
)

// --- benchmarkJSONFlag ---

func TestBenchmarkJSONFlagImplementsPflagValue(t *testing.T) {
	var _ pflag.Value = &benchmarkJSONFlag{}
}

func TestBenchmarkJSONFlagBareIsStdout(t *testing.T) {
	var f benchmarkJSONFlag
	if err := f.Set("true"); err != nil { // NoOptDefVal for a bare --json
		t.Fatalf("Set(true) error = %v", err)
	}
	if !f.stdout || f.path != "" {
		t.Fatalf("flag = %+v, want stdout=true path=\"\"", f)
	}
}

func TestBenchmarkJSONFlagExplicitBool(t *testing.T) {
	cases := []struct {
		in         string
		wantStdout bool
	}{
		{"true", true}, {"1", true},
		{"false", false}, {"0", false},
	}
	for _, tc := range cases {
		var f benchmarkJSONFlag
		if err := f.Set(tc.in); err != nil {
			t.Fatalf("Set(%q) error = %v", tc.in, err)
		}
		if f.stdout != tc.wantStdout || f.path != "" {
			t.Errorf("Set(%q) = %+v, want stdout=%v path=\"\"", tc.in, f, tc.wantStdout)
		}
	}
}

func TestBenchmarkJSONFlagDeprecatedPathForm(t *testing.T) {
	var f benchmarkJSONFlag
	if err := f.Set("/tmp/results.json"); err != nil {
		t.Fatalf("Set(path) error = %v", err)
	}
	if f.stdout {
		t.Error("stdout = true, want false for a path value")
	}
	if f.path != "/tmp/results.json" {
		t.Errorf("path = %q, want /tmp/results.json", f.path)
	}
	if got := f.String(); got != "/tmp/results.json" {
		t.Errorf("String() = %q, want the path", got)
	}
}

func TestBenchmarkJSONFlagRegisteredWithNoOptDefVal(t *testing.T) {
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	var f benchmarkJSONFlag
	fs.Var(&f, "json", "usage")
	fs.Lookup("json").NoOptDefVal = "true"

	if err := fs.Parse([]string{"--json"}); err != nil {
		t.Fatalf("Parse(--json) error = %v", err)
	}
	if !f.stdout {
		t.Fatalf("bare --json = %+v, want stdout=true", f)
	}
}

func TestBenchmarkJSONFlagParsesEqualsPath(t *testing.T) {
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	var f benchmarkJSONFlag
	fs.Var(&f, "json", "usage")
	fs.Lookup("json").NoOptDefVal = "true"

	if err := fs.Parse([]string{"--json=/tmp/out.json"}); err != nil {
		t.Fatalf("Parse(--json=path) error = %v", err)
	}
	if f.stdout || f.path != "/tmp/out.json" {
		t.Fatalf("--json=path = %+v, want the deprecated path form", f)
	}
}

// --- resolveBenchmarkOutput ---

func TestResolveBenchmarkOutputBareJSON(t *testing.T) {
	stdout, file, deprecated := resolveBenchmarkOutput(benchmarkJSONFlag{stdout: true}, "")
	if !stdout || file != "" || deprecated {
		t.Fatalf("got stdout=%v file=%q deprecated=%v, want stdout=true file=\"\" deprecated=false", stdout, file, deprecated)
	}
}

func TestResolveBenchmarkOutputJSONFile(t *testing.T) {
	stdout, file, deprecated := resolveBenchmarkOutput(benchmarkJSONFlag{}, "/tmp/out.json")
	if stdout || file != "/tmp/out.json" || deprecated {
		t.Fatalf("got stdout=%v file=%q deprecated=%v, want the file preserved and no deprecation", stdout, file, deprecated)
	}
}

func TestResolveBenchmarkOutputDeprecatedPathWarns(t *testing.T) {
	stdout, file, deprecated := resolveBenchmarkOutput(benchmarkJSONFlag{path: "/tmp/legacy.json"}, "")
	if stdout || file != "/tmp/legacy.json" || !deprecated {
		t.Fatalf("got stdout=%v file=%q deprecated=%v, want the legacy path honoured and flagged deprecated", stdout, file, deprecated)
	}
}

// --json-file must win when both are somehow set, so there is never
// ambiguity about which path is authoritative.
func TestResolveBenchmarkOutputJSONFileWinsOverDeprecatedPath(t *testing.T) {
	stdout, file, deprecated := resolveBenchmarkOutput(benchmarkJSONFlag{path: "/tmp/legacy.json"}, "/tmp/explicit.json")
	if stdout || file != "/tmp/explicit.json" || !deprecated {
		t.Fatalf("got stdout=%v file=%q deprecated=%v, want --json-file's path to win (still flagged deprecated)", stdout, file, deprecated)
	}
}

func TestBenchmarkCommandRegistersJSONFileFlag(t *testing.T) {
	if benchmarkCmd.Flags().Lookup("json") == nil {
		t.Error("benchmark command missing --json flag")
	}
	if benchmarkCmd.Flags().Lookup("json-file") == nil {
		t.Error("benchmark command missing --json-file flag")
	}
	if got := benchmarkCmd.Flags().Lookup("json").NoOptDefVal; got != "true" {
		t.Errorf("--json NoOptDefVal = %q, want \"true\" (bare --json must parse as a bool)", got)
	}
}
