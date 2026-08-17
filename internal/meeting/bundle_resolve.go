package meeting

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

// ResolveBundle finds one meeting from whatever a user typed: a bundle id
// inside meetingsDir, a path to the bundle directory, or a path to the
// meeting.md or events.jsonl inside one — the three things a person actually
// has at hand after tab-completing or copying a path out of a listing.
//
// It reports false rather than an error because every failure means the same
// thing to a caller: there is no such meeting.
func ResolveBundle(ctx context.Context, meetingsDir, ref string) (BundleEntry, bool) {
	if ctx.Err() != nil {
		return BundleEntry{}, false
	}
	bundle := bundlePathForRef(meetingsDir, strings.TrimSpace(ref))
	if bundle == "" {
		return BundleEntry{}, false
	}
	return readBundleEntry(bundle)
}

// bundlePathForRef maps a reference onto a bundle directory, or "".
func bundlePathForRef(meetingsDir, ref string) string {
	if ref == "" {
		return ""
	}
	// A bare bundle id is resolved against the meetings dir first: it is the
	// id the index and the wire both hand out, and it never touches the
	// filesystem unless it passes the id rules.
	if ValidBundleID(ref) {
		if candidate := filepath.Join(meetingsDir, ref); isBundleDir(candidate) {
			return absolute(candidate)
		}
	}
	path := expandHome(ref)
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	if info.IsDir() {
		if hasBundleSuffix(path) {
			return absolute(path)
		}
		return ""
	}
	switch filepath.Base(path) {
	case meetinglog.BundleDocumentName:
		if parent := filepath.Dir(path); hasBundleSuffix(parent) {
			return absolute(parent)
		}
	case meetinglog.BundleEventsName:
		if bundle := bundlePathForJSONL(path); bundle != "" {
			return absolute(bundle)
		}
	}
	return ""
}

func isBundleDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir() && hasBundleSuffix(path)
}

func hasBundleSuffix(path string) bool {
	return strings.HasSuffix(strings.ToLower(filepath.Base(path)), BundleSuffix)
}

// absolute keeps reported paths copy-pasteable from any working directory.
func absolute(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}
