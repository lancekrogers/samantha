package meeting

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// BundleSuffix is the directory suffix every meeting bundle carries.
const BundleSuffix = ".meeting"

// Slug reduces a meeting description to a filesystem-safe stem: lowercase
// alphanumerics with single dashes between runs, capped at 60 bytes.
func Slug(description string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(description) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 60 {
		s = strings.Trim(s[:60], "-")
	}
	if s == "" {
		return "meeting"
	}
	return s
}

// BundleName joins the slug with the codebase's sortable timestamp layout and
// the recognizable directory suffix. Desktop recordings and serve-driven
// remote recordings share this so bundles are indistinguishable on disk.
func BundleName(description string, now time.Time) string {
	return fmt.Sprintf("%s-%s%s", Slug(description), now.Format("20060102-150405"), BundleSuffix)
}

// safeIDPattern is the shape an id may have before it is allowed anywhere near
// a filesystem path: no separators, no leading dot, so neither "../.." nor
// "/etc/passwd" nor a hidden name can survive it. Live meeting ids (16 hex)
// and bundle ids both match; a bundle id additionally ends in .meeting.
var safeIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,120}$`)

// SafeMeetingID reports whether an id is safe to compare, log, and join onto a
// path. It says nothing about whether the meeting exists.
func SafeMeetingID(id string) bool { return safeIDPattern.MatchString(id) }

// ValidBundleID reports whether an id could name a bundle directory: safe, and
// carrying the .meeting suffix every bundle has.
func ValidBundleID(id string) bool {
	return SafeMeetingID(id) && strings.HasSuffix(id, BundleSuffix) && len(id) > len(BundleSuffix)
}
