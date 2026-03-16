// internal/buildutil/main.go
package main

import (
	"os"
	"runtime"

	"github.com/Obedience-Corp/obey-shared/buildutil"
)

func main() {
	buildutil.Run(os.Args[1:], buildutil.BuildConfig{
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
