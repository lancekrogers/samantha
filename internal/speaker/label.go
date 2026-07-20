package speaker

import (
	"fmt"
	"strings"
)

// AnonymousLabel returns speaker-N for 1-based index n (n >= 1).
func AnonymousLabel(n int) string {
	if n < 1 {
		n = 1
	}
	return fmt.Sprintf("%s%d", LabelSpeakerPrefix, n)
}

// NormalizeLabel trims and lowercases enrolled names; empty becomes unknown.
func NormalizeLabel(label string) string {
	s := strings.TrimSpace(strings.ToLower(label))
	if s == "" {
		return LabelUnknown
	}
	return s
}

// IsUnknown reports whether label is empty or the unknown sentinel.
func IsUnknown(label string) bool {
	s := strings.TrimSpace(strings.ToLower(label))
	return s == "" || s == LabelUnknown
}

// ApplyThreshold returns LabelUnknown when conf < threshold, otherwise label.
// Empty label always becomes unknown.
func ApplyThreshold(label string, conf, threshold float32) string {
	if IsUnknown(label) || conf < threshold {
		return LabelUnknown
	}
	return NormalizeLabel(label)
}

// MapDiarizationID maps a raw engine speaker index (often 0-based) to speaker-N.
func MapDiarizationID(id int) string {
	// Engine ids are typically 0-based; product labels are 1-based.
	if id < 0 {
		return LabelUnknown
	}
	return AnonymousLabel(id + 1)
}
