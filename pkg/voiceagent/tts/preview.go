package tts

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SpokenPreviewName returns a short, speakable name for a voice id.
//
// Examples:
//
//	af_heart  → "Heart"
//	bm_george → "George"
//	Uncle_Fu  → "Uncle Fu"
//	Vivian    → "Vivian"
func SpokenPreviewName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "this voice"
	}
	// Prefer human-readable segments over raw ids.
	if strings.Contains(name, "_") || strings.Contains(name, "-") {
		parts := strings.FieldsFunc(name, func(r rune) bool {
			return r == '_' || r == '-'
		})
		if len(parts) == 0 {
			return name
		}
		// Kokoro style: locale+gender prefix (af, am, bf, bm, …) then the name.
		if len(parts) >= 2 && isKokoroPrefix(parts[0]) {
			parts = parts[1:]
		}
		for i, p := range parts {
			parts[i] = titleWord(p)
		}
		return strings.Join(parts, " ")
	}
	return titleWord(name)
}

// SpokenPreviewLine is the sample sentence used when auditioning a voice in
// Settings. It names the voice — not a fixed persona — so multi-provider,
// multi-voice, multi-persona setups stay honest.
func SpokenPreviewLine(voiceName string) string {
	return fmt.Sprintf("Hi, I'm %s. This is how I sound.", SpokenPreviewName(voiceName))
}

func isKokoroPrefix(s string) bool {
	if len(s) != 2 {
		return false
	}
	a, b := s[0], s[1]
	return (a == 'a' || a == 'b' || a == 'e' || a == 'f' || a == 'h' || a == 'i' || a == 'j' || a == 'p' || a == 'z') &&
		(b == 'f' || b == 'm')
}

func titleWord(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	// Preserve already-capitalized names like Vivian / Ryan.
	if r, _ := utf8.DecodeRuneInString(s); unicode.IsUpper(r) {
		return s
	}
	runes := []rune(strings.ToLower(s))
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}
