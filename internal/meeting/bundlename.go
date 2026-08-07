package meeting

import (
	"fmt"
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
