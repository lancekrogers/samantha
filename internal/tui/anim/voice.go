// Package anim renders compact terminal animations for Samantha's voice TUI.
// The style mirrors the festival installer's flame: multi-frame ASCII art,
// state-colored gradients, and a level scale that grows with energy.
package anim

import (
	"math"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Mode is the voice activity state driving the meter.
type Mode int

const (
	ModeIdle Mode = iota
	ModeListening
	ModeHearing
	ModeTranscribing
	ModeThinking
	ModeSynthesizing
	ModeSpeaking
	ModeError
)

// Styles colors the meter. Callers pass the conversation palette so anim stays
// free of package cycles with internal/tui.
type Styles struct {
	Tip    lipgloss.Style // bright peak
	Mid    lipgloss.Style // mid body
	Core   lipgloss.Style // base
	Muted  lipgloss.Style
	Label  lipgloss.Style
	Error  lipgloss.Style
	Accent lipgloss.Style
}

// barGlyphs are height steps for the level-reactive waveform (0..7).
var barGlyphs = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// listeningBreath is a soft ambient pulse while waiting for speech.
var listeningBreath = []string{
	"  ·  ·  ·  ·  ·  ·  ",
	"  ·  °  ·  °  ·  °  ",
	"  °  ·  °  ·  °  ·  ",
	"  ·  ·  ·  ·  ·  ·  ",
}

// speakingFrames are outward speech ripples (festival-style multi-line art).
var speakingFrames = [][]string{
	{
		"    ·  ",
		"   )(  ",
		"  )##( ",
		" )####(",
		"  `##' ",
	},
	{
		"   · · ",
		"   )#( ",
		"  )###(",
		" )#####(",
		"  `###' ",
	},
	{
		"  ·  · ",
		"  )##( ",
		" )####(",
		")######(",
		" `####' ",
	},
	{
		" ·  · ·",
		"  )##( ",
		" )####(",
		")######(",
		" `####' ",
	},
	{
		"  ·  · ",
		"  )##( ",
		" )####(",
		")#####( ",
		" `###'  ",
	},
	{
		"   · · ",
		"   )#( ",
		"  )###(",
		" )####( ",
		"  `##'  ",
	},
}

// hearingFrames grow with input level (bottom-up scale, like the flame).
var hearingFrames = [][]string{
	{
		"   ·   ",
		"  )|(  ",
		" )|||(",
		")|||||(",
		" `|||' ",
	},
	{
		"  · ·  ",
		"  )||( ",
		" )||||(",
		")||||||(",
		" `||||' ",
	},
	{
		" · · · ",
		" )|||( ",
		")|||||( ",
		")|||||||(",
		" `|||||' ",
	},
}

// ReducedMotion reports whether ambient animation should be disabled.
// Honors SAMANTHA_REDUCED_MOTION and the common NO_MOTION / reduced-motion envs.
func ReducedMotion() bool {
	for _, key := range []string{"SAMANTHA_REDUCED_MOTION", "NO_MOTION", "FESTIVAL_REDUCED_MOTION"} {
		switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}

// Waveform renders a single-line level meter of the given width (min 8).
// level is 0..1; frame advances the phase of the synthetic envelope when level
// is low so idle states still breathe.
func Waveform(frame int, level float64, width int, s Styles) string {
	if width < 8 {
		width = 8
	}
	if width > 48 {
		width = 48
	}
	level = clamp01(level)
	var b strings.Builder
	b.Grow(width * 8)
	for i := 0; i < width; i++ {
		// Spatial envelope: higher near center, dips at edges.
		pos := float64(i) / float64(width-1)
		center := 1 - math.Abs(pos-0.5)*1.6
		if center < 0.15 {
			center = 0.15
		}
		// Phase drift so the bar feels alive under steady speech.
		phase := float64((frame*3+i*2)%16) / 16
		wiggle := 0.12 * math.Sin(phase*2*math.Pi)
		h := clamp01(level*center + wiggle*(0.25+0.75*level))
		idx := int(h * float64(len(barGlyphs)-1))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(barGlyphs) {
			idx = len(barGlyphs) - 1
		}
		g := string(barGlyphs[idx])
		switch {
		case idx >= 6:
			b.WriteString(s.Tip.Render(g))
		case idx >= 3:
			b.WriteString(s.Mid.Render(g))
		default:
			b.WriteString(s.Core.Render(g))
		}
	}
	return b.String()
}

// Panel renders a multi-line voice indicator for the active mode.
// heightScale is 0..1 (typically the live mic/playback level).
func Panel(mode Mode, frame int, heightScale float64, width int, label string, s Styles, reduced bool) string {
	if mode == ModeIdle {
		return ""
	}
	heightScale = clamp01(heightScale)
	if reduced {
		return staticPanel(mode, heightScale, width, label, s)
	}

	var art string
	switch mode {
	case ModeListening:
		art = s.Muted.Render(listeningBreath[frame%len(listeningBreath)])
	case ModeHearing:
		art = renderScaledArt(hearingFrames[frame%len(hearingFrames)], heightScale, s)
	case ModeTranscribing:
		spin := []string{"·", "°", "*", "✦"}[frame%4]
		art = s.Accent.Render("  " + spin + " " + Waveform(frame, 0.35+0.1*math.Sin(float64(frame)/2), 14, s) + " " + spin)
	case ModeThinking:
		spin := []string{"·", "°", "·", "✦"}[frame%4]
		art = s.Muted.Render("  " + spin + " thinking " + spin)
	case ModeSynthesizing:
		art = renderScaledArt(speakingFrames[frame%len(speakingFrames)], 0.4+0.35*heightScale, s)
	case ModeSpeaking:
		// Speaking uses a synthetic envelope when no output level is available.
		env := heightScale
		if env < 0.2 {
			env = 0.45 + 0.35*math.Abs(math.Sin(float64(frame)*0.55))
		}
		art = renderScaledArt(speakingFrames[frame%len(speakingFrames)], env, s)
	case ModeError:
		art = s.Error.Render("  !  ·  !  ·  !  ")
	default:
		return ""
	}

	wave := Waveform(frame, effectiveLevel(mode, heightScale, frame), meterWidth(width), s)
	lab := label
	if lab == "" {
		lab = modeLabel(mode)
	}
	labelLine := s.Label.Render(lab)

	block := lipgloss.JoinVertical(lipgloss.Center, art, wave, labelLine)
	if width < 20 {
		return block
	}
	return lipgloss.PlaceHorizontal(width, lipgloss.Center, block)
}

// CompactMeter is a single-line header chip: glyph + waveform + short label.
func CompactMeter(mode Mode, frame int, level float64, label string, s Styles, reduced bool) string {
	if mode == ModeIdle {
		return ""
	}
	level = clamp01(level)
	glyph := modeGlyph(mode, frame, reduced)
	waveW := 10
	if reduced {
		waveW = 6
	}
	wave := Waveform(frame, effectiveLevel(mode, level, frame), waveW, s)
	if label == "" {
		label = modeLabel(mode)
	}
	style := s.Label
	if mode == ModeError {
		style = s.Error
	}
	return glyph + " " + wave + " " + style.Render(label)
}

func staticPanel(mode Mode, level float64, width int, label string, s Styles) string {
	wave := Waveform(0, clamp01(level), meterWidth(width), s)
	if label == "" {
		label = modeLabel(mode)
	}
	block := lipgloss.JoinVertical(lipgloss.Center, wave, s.Label.Render(label))
	if width < 20 {
		return block
	}
	return lipgloss.PlaceHorizontal(width, lipgloss.Center, block)
}

func renderScaledArt(lines []string, heightScale float64, s Styles) string {
	if len(lines) == 0 {
		return ""
	}
	// Show bottom N lines proportional to heightScale (min 2), flame-style.
	n := 2 + int(float64(len(lines)-2)*clamp01(heightScale))
	if n > len(lines) {
		n = len(lines)
	}
	start := len(lines) - n
	var b strings.Builder
	for i := start; i < len(lines); i++ {
		line := lines[i]
		rel := float64(i-start) / float64(max(n-1, 1))
		var styled string
		switch {
		case rel < 0.35:
			styled = s.Tip.Render(line)
		case rel < 0.7:
			styled = s.Mid.Render(line)
		default:
			styled = s.Core.Render(line)
		}
		b.WriteString(styled)
		if i < len(lines)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func effectiveLevel(mode Mode, level float64, frame int) float64 {
	level = clamp01(level)
	switch mode {
	case ModeListening:
		return 0.12 + 0.08*math.Abs(math.Sin(float64(frame)*0.4))
	case ModeHearing:
		if level < 0.05 {
			return 0.2 + 0.1*math.Abs(math.Sin(float64(frame)*0.7))
		}
		return level
	case ModeTranscribing:
		return 0.3 + 0.1*math.Abs(math.Sin(float64(frame)*0.5))
	case ModeThinking:
		return 0.15 + 0.05*math.Abs(math.Sin(float64(frame)*0.3))
	case ModeSynthesizing:
		return 0.35 + 0.2*math.Abs(math.Sin(float64(frame)*0.6))
	case ModeSpeaking:
		if level < 0.15 {
			return 0.45 + 0.35*math.Abs(math.Sin(float64(frame)*0.55))
		}
		return level
	case ModeError:
		return 0.5
	default:
		return level
	}
}

func modeLabel(mode Mode) string {
	switch mode {
	case ModeListening:
		return "Listening"
	case ModeHearing:
		return "Hearing you"
	case ModeTranscribing:
		return "Transcribing"
	case ModeThinking:
		return "Thinking"
	case ModeSynthesizing:
		return "Synthesizing"
	case ModeSpeaking:
		return "Speaking"
	case ModeError:
		return "Error"
	default:
		return ""
	}
}

func modeGlyph(mode Mode, frame int, reduced bool) string {
	if reduced {
		switch mode {
		case ModeListening, ModeHearing:
			return "🎙"
		case ModeSpeaking, ModeSynthesizing:
			return "●"
		case ModeError:
			return "✗"
		default:
			return "·"
		}
	}
	spin := []string{"·", "°", "*", "✦"}
	switch mode {
	case ModeListening:
		return "🎙"
	case ModeHearing:
		return []string{"🎙", "◉", "🎙", "◎"}[frame%4]
	case ModeTranscribing:
		return spin[frame%4]
	case ModeThinking:
		return spin[frame%4]
	case ModeSynthesizing:
		return []string{"♪", "♫", "♪", "✦"}[frame%4]
	case ModeSpeaking:
		return []string{"◉", "◎", "●", "◎"}[frame%4]
	case ModeError:
		return "✗"
	default:
		return "·"
	}
}

func meterWidth(panelWidth int) int {
	if panelWidth <= 0 {
		return 16
	}
	w := panelWidth / 3
	if w < 12 {
		w = 12
	}
	if w > 28 {
		w = 28
	}
	return w
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
