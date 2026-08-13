package qwen

import "runtime"

// nativeReleaseTag is the published qwen3-tts-native GitHub release tag that
// Samantha uses when qwen_tts_native_url is empty. Bump when shipping a new pin.
const nativeReleaseTag = "v0.1.0"

const nativeReleaseBase = "https://github.com/Obedience-Corp/qwen3-tts-native/releases/download/" + nativeReleaseTag

// DefaultNativeRelease returns the platform tarball URL and archive SHA-256 for
// the pinned public release. Empty strings mean this OS/arch has no published
// asset yet (ensure will fail closed with a clear remediation).
//
// Stable asset names (no git short hash):
//
//	qwen3-tts-native-<goos>-<goarch>.tar.gz
func DefaultNativeRelease() (url, sha256 string) {
	key := runtime.GOOS + "/" + runtime.GOARCH
	switch key {
	case "darwin/arm64":
		// qwen3-tts-native v0.1.0 — Metal 0.6B F16 package
		return nativeReleaseBase + "/qwen3-tts-native-darwin-arm64.tar.gz",
			"67b6e7514168d146cf4d892d6594559796bcea4f130c9c837beb8eac1c416e2f"
	// case "linux/amd64":
	// 	return nativeReleaseBase + "/qwen3-tts-native-linux-amd64.tar.gz", "…"
	default:
		return "", ""
	}
}
