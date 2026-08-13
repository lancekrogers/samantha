//go:build integration && windows

package tts

import "fmt"

func withCapturedStderr(fn func()) (string, error) {
	// Windows live Kokoro tests still run sample-count checks; fd redirect for
	// native sherpa logs is unix-only here.
	fn()
	return "", fmt.Errorf("stderr capture unsupported on windows")
}
