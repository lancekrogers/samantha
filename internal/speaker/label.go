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

// LabelsEqual compares labels case-insensitively for identity checks.
// Observation.Label should keep enrollment/stable id casing; do not use this
// to rewrite stored labels.
func LabelsEqual(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// IsUnknown reports whether label is empty or the unknown sentinel.
func IsUnknown(label string) bool {
	s := strings.TrimSpace(strings.ToLower(label))
	return s == "" || s == LabelUnknown
}

// ApplyThreshold returns LabelUnknown when conf < threshold or label is empty.
// On pass-through it preserves the candidate label casing (stable id).
func ApplyThreshold(label string, conf, threshold float32) string {
	if IsUnknown(label) || conf < threshold {
		return LabelUnknown
	}
	return strings.TrimSpace(label)
}

// MapDiarizationID maps a raw engine speaker index (often 0-based) to speaker-N.
func MapDiarizationID(id int) string {
	// Engine ids are typically 0-based; product labels are 1-based.
	if id < 0 {
		return LabelUnknown
	}
	return AnonymousLabel(id + 1)
}
