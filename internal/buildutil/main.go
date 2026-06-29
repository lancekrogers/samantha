// internal/buildutil/main.go
package main

import (
	"os"
	"runtime"
	"strings"

	"github.com/Obedience-Corp/obey-shared/buildutil"
)

func main() {
	args := os.Args[1:]
	if isIntegrationCommand(args) {
		addBuildTag("integration")
	}

	buildutil.Run(args, buildutil.BuildConfig{
		BinaryName:         "samantha",
		MainPath:           "./cmd/samantha",
		SectionName:        "Samantha Voice Assistant",
		IntegrationTestDir: "tests/integration",
		IntegrationBuildEnv: func() []string {
			// CGO_ENABLED=0 for CLI/config integration tests.
			// Audio-dependent tests run locally, not in containers.
			return []string{
				"CGO_ENABLED=0",
				"GOOS=linux",
				"GOARCH=" + runtime.GOARCH,
			}
		},
	})
}

func isIntegrationCommand(args []string) bool {
	for _, arg := range args {
		if arg == "integration" {
			return true
		}
	}
	return false
}

func addBuildTag(tag string) {
	current := strings.TrimSpace(os.Getenv("BUILD_TAGS"))
	if current == "" {
		_ = os.Setenv("BUILD_TAGS", tag)
		return
	}
	for _, existing := range strings.Split(current, ",") {
		if strings.TrimSpace(existing) == tag {
			return
		}
	}
	_ = os.Setenv("BUILD_TAGS", current+","+tag)
}
