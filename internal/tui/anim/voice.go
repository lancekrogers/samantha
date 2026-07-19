// Package anim renders terminal animations for Samantha's voice TUI.
// Festival-installer DNA: multi-frame ASCII, state-colored gradients, and a
// level scale that grows with mic/playback energy — turned up for a live
// conversation stage rather than a boot splash.
package anim

import (
	"fmt"
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
	// Optional mode accents — when zero-value, Tip/Mid/Core are used.
	Hearing lipgloss.Style
	Speak   lipgloss.Style
	Think   lipgloss.Style
	Fire    lipgloss.Style
	Border  lipgloss.Style
	Badge   lipgloss.Style
}

// barGlyphs are height steps for the level-reactive waveform (0..8).
var barGlyphs = []rune{' ', '▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// listeningRings — soft radar breath while waiting for speech.
var listeningRings = [][]string{
	{
		"      ·  ·  ·      ",
		"   ·         ·   ",
		"  ·    (·)    ·  ",
		"   ·         ·   ",
		"      ·  ·  ·      ",
	},
	{
		"     ·  °  ·     ",
		"   °    ·    °   ",
		"  ·   ( · )   ·  ",
		"   °    ·    °   ",
		"     ·  °  ·     ",
	},
	{
		"    °  ·  ·  °    ",
		"  ·    ° °    ·  ",
		" ·   (( · ))   · ",
		"  ·    ° °    ·  ",
		"    °  ·  ·  °    ",
	},
	{
		"   ·  °  *  °  ·  ",
		"  °   ·   ·   °  ",
		" ·   (  ·  )   · ",
		"  °   ·   ·   °  ",
		"   ·  °  *  °  ·  ",
	},
	{
		"    °  ·  ·  °    ",
		"  ·    ° °    ·  ",
		" ·   (( · ))   · ",
		"  ·    ° °    ·  ",
		"    °  ·  ·  °    ",
	},
	{
		"     ·  °  ·     ",
		"   °    ·    °   ",
		"  ·   ( · )   ·  ",
		"   °    ·    °   ",
		"     ·  °  ·     ",
	},
}

// hearingBurst — mic energy plume (grows with level, flame-style).
var hearingBurst = [][]string{
	{
		"       ·       ",
		"      )|(      ",
		"     )||| (    ",
		"    )|||||(    ",
		"   )||||||| (  ",
		"    `|||||´    ",
	},
	{
		"      · ·      ",
		"     )|||(     ",
		"    )|||||(    ",
		"   )|||||||(   ",
		"  )|||||||||(  ",
		"   `|||||||´   ",
	},
	{
		"     * · *     ",
		"    )|||||(    ",
		"   )|||||||(   ",
		"  )|||||||||(  ",
		" )||||||||||| (",
		"  `|||||||||´  ",
	},
	{
		"    · * · *    ",
		"   )|||||||(   ",
		"  )|||||||||(  ",
		" )|||||||||||( ",
		")|||||||||||||( ",
		" `|||||||||||´ ",
	},
}

// speakingWaves — outward speech ripples.
var speakingWaves = [][]string{
	{
		"      ·  ·      ",
		"    )  ~~  (    ",
		"   )  ~~~~  (   ",
		"  )  ~~~~~~  (  ",
		"   `  ~~~~  ´   ",
	},
	{
		"     ·  *  ·    ",
		"   )  ~~~~  (   ",
		"  )  ~~~~~~  (  ",
		" )  ~~~~~~~~  ( ",
		"  `  ~~~~~~  ´  ",
	},
	{
		"    *  ·  ·  *  ",
		"  )  ~~~~~~  (  ",
		" )  ~~~~~~~~  ( ",
		")  ~~~~~~~~~~  (",
		" `  ~~~~~~~~  ´ ",
	},
	{
		"   ·  *  *  ·   ",
		"  )  ~~~~~~  (  ",
		" )  ~~~~~~~~  ( ",
		")  ~~~~~~~~~~  (",
		" `  ~~~~~~~~  ´ ",
	},
	{
		"    *  ·  ·  *  ",
		"  )  ~~~~~~  (  ",
		" )  ~~~~~~~~  ( ",
		"  )  ~~~~~~  (  ",
		"   `  ~~~~  ´   ",
	},
	{
		"     ·  *  ·    ",
		"   )  ~~~~  (   ",
		"  )  ~~~~~~  (  ",
		"   )  ~~~~  (   ",
		"    `  ~~  ´    ",
	},
}

// thinkingOrbit — slow star orbit while the model works.
var thinkingOrbit = [][]string{
	{"  ✦  ·  ·  ·  · ", "  ·    (·)    · ", "  ·  ·  ·  ·  ° "},
	{"  ·  ✦  ·  ·  · ", "  ·    (·)    · ", "  °  ·  ·  ·  · "},
	{"  ·  ·  ✦  ·  · ", "  ·    (·)    · ", "  ·  ·  ·  ·  · "},
	{"  ·  ·  ·  ✦  · ", "  ·    (·)    · ", "  ·  ·  ·  ·  · "},
	{"  ·  ·  ·  ·  ✦ ", "  ·    (·)    · ", "  ·  ·  ·  ·  · "},
	{"  ·  ·  ·  ·  · ", "  ·    (✦)    · ", "  ·  ·  ·  ·  · "},
}

// ReducedMotion reports whether ambient animation should be disabled.
func ReducedMotion() bool {
	for _, key := range []string{"SAMANTHA_REDUCED_MOTION", "NO_MOTION", "FESTIVAL_REDUCED_MOTION"} {
		switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}

// Spectrum renders a multi-row EQ bar (rows tall, cols wide) driven by level.
func Spectrum(frame int, level float64, cols, rows int, s Styles) string {
	if cols < 8 {
		cols = 8
	}
	if cols > 56 {
		cols = 56
	}
	if rows < 1 {
		rows = 1
	}
	if rows > 6 {
		rows = 6
	}
	level = clamp01(level)
	// Precompute column heights 0..rows.
	heights := make([]int, cols)
	for i := 0; i < cols; i++ {
		pos := float64(i) / float64(max(cols-1, 1))
		// Twin-lobe envelope with a hot center — reads as a stereo meter.
		lobe := math.Sin(pos * math.Pi)
		lobe = lobe * lobe
		phase := float64((frame*2+i*3)%20) / 20
		wiggle := 0.18 * math.Sin(phase*2*math.Pi+float64(i)*0.35)
		h := clamp01(level*(0.35+0.65*lobe) + wiggle*level)
		// Low floor so idle listening still shows a thin line.
		if h < 0.08 && level > 0.02 {
			h = 0.08
		}
		heights[i] = int(math.Round(h * float64(rows)))
		if heights[i] > rows {
			heights[i] = rows
		}
	}

	var lines []string
	for r := rows; r >= 1; r-- {
		var b strings.Builder
		for i := 0; i < cols; i++ {
			if heights[i] >= r {
				// Color by relative height of this cell.
				rel := float64(r) / float64(rows)
				g := "█"
				if heights[i] == r && r < rows {
					g = "▄"
				}
				b.WriteString(colorByHeat(rel, s).Render(g))
			} else {
				b.WriteString(s.Muted.Render("·"))
			}
		}
		lines = append(lines, b.String())
	}
	return strings.Join(lines, "\n")
}

// Waveform renders a single-line level meter of the given width (min 8).
func Waveform(frame int, level float64, width int, s Styles) string {
	if width < 8 {
		width = 8
	}
	if width > 56 {
		width = 56
	}
	level = clamp01(level)
	var b strings.Builder
	b.Grow(width * 12)
	for i := 0; i < width; i++ {
		pos := float64(i) / float64(max(width-1, 1))
		center := 1 - math.Abs(pos-0.5)*1.7
		if center < 0.12 {
			center = 0.12
		}
		phase := float64((frame*3+i*2)%16) / 16
		wiggle := 0.14 * math.Sin(phase*2*math.Pi)
		h := clamp01(level*center + wiggle*(0.2+0.8*level))
		idx := int(h * float64(len(barGlyphs)-1))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(barGlyphs) {
			idx = len(barGlyphs) - 1
		}
		g := string(barGlyphs[idx])
		rel := float64(idx) / float64(len(barGlyphs)-1)
		b.WriteString(colorByHeat(rel, s).Render(g))
	}
	return b.String()
}

// Stage is the full voice panel: art + spectrum + badge, wrapped in a card.
func Stage(mode Mode, frame int, heightScale float64, width int, label string, s Styles, reduced bool) string {
	if mode == ModeIdle {
		return ""
	}
	heightScale = clamp01(heightScale)
	level := effectiveLevel(mode, heightScale, frame)
	if label == "" {
		label = modeLabel(mode)
	}

	palette := modePalette(mode, s)
	if reduced {
		wave := Waveform(0, level, meterWidth(width), palette)
		badge := renderBadge(mode, label, level, palette, true)
		body := lipgloss.JoinVertical(lipgloss.Center, wave, badge)
		return stageCard(body, width, palette, mode)
	}

	art := renderArt(mode, frame, level, palette)
	specRows := 3
	if width < 60 {
		specRows = 2
	}
	spec := Spectrum(frame, level, spectrumCols(width), specRows, palette)
	wave := Waveform(frame, level, meterWidth(width), palette)
	badge := renderBadge(mode, label, level, palette, false)

	body := lipgloss.JoinVertical(lipgloss.Center, art, "", spec, wave, "", badge)
	return stageCard(body, width, palette, mode)
}

// Panel is an alias for Stage kept for call-site compatibility.
func Panel(mode Mode, frame int, heightScale float64, width int, label string, s Styles, reduced bool) string {
	return Stage(mode, frame, heightScale, width, label, s, reduced)
}

// CompactMeter is a single-line header chip: glyph + waveform + short label.
func CompactMeter(mode Mode, frame int, level float64, label string, s Styles, reduced bool) string {
	if mode == ModeIdle {
		return ""
	}
	level = clamp01(level)
	palette := modePalette(mode, s)
	glyph := modeGlyph(mode, frame, reduced)
	waveW := 14
	if reduced {
		waveW = 8
	}
	wave := Waveform(frame, effectiveLevel(mode, level, frame), waveW, palette)
	if label == "" {
		label = modeLabel(mode)
	}
	pct := int(effectiveLevel(mode, level, frame) * 100)
	lab := palette.Label.Bold(true).Render(fmt.Sprintf("%s %d%%", label, pct))
	return glyph + " " + wave + " " + lab
}

func stageCard(body string, width int, s Styles, mode Mode) string {
	if width < 24 {
		return body
	}
	inner := max(width-4, 20)
	// Cap card width so it doesn't dominate ultra-wide terminals.
	if inner > 72 {
		inner = 72
	}
	border := s.Border
	if border.GetForeground() == (lipgloss.Color("")) {
		border = s.Mid
	}
	card := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border.GetForeground()).
		Padding(0, 2).
		Width(inner).
		Align(lipgloss.Center).
		Render(body)
	return lipgloss.PlaceHorizontal(width, lipgloss.Center, card)
}

func renderArt(mode Mode, frame int, level float64, s Styles) string {
	switch mode {
	case ModeListening:
		return colorizeFrame(listeningRings[frame%len(listeningRings)], s, 0.35+0.2*math.Abs(math.Sin(float64(frame)*0.4)))
	case ModeHearing:
		f := hearingBurst[frame%len(hearingBurst)]
		return renderScaledArt(f, maxFloat(level, 0.35), s)
	case ModeTranscribing:
		spin := []string{"◐", "◓", "◑", "◒"}[frame%4]
		return s.Accent.Render("  "+spin+" ") + Waveform(frame, 0.4+0.15*math.Sin(float64(frame)/2), 18, s) + s.Accent.Render(" "+spin)
	case ModeThinking:
		return colorizeFrame(thinkingOrbit[frame%len(thinkingOrbit)], s, 0.4)
	case ModeSynthesizing:
		return renderScaledArt(speakingWaves[frame%len(speakingWaves)], 0.45+0.3*level, s)
	case ModeSpeaking:
		env := level
		if env < 0.25 {
			env = 0.5 + 0.35*math.Abs(math.Sin(float64(frame)*0.55))
		}
		return renderScaledArt(speakingWaves[frame%len(speakingWaves)], env, s)
	case ModeError:
		return s.Error.Bold(true).Render("  ⚠  ·  ⚠  ·  ⚠  ")
	default:
		return ""
	}
}

func renderBadge(mode Mode, label string, level float64, s Styles, reduced bool) string {
	pct := int(clamp01(level) * 100)
	icon := modeGlyph(mode, 0, reduced)
	text := fmt.Sprintf(" %s  %s  ·  %d%% ", icon, strings.ToUpper(label), pct)
	style := s.Badge
	if style.GetBackground() == (lipgloss.Color("")) {
		style = s.Label.Bold(true).Padding(0, 1)
	}
	return style.Render(text)
}

func colorizeFrame(lines []string, s Styles, heat float64) string {
	var out []string
	for i, line := range lines {
		rel := float64(i) / float64(max(len(lines)-1, 1))
		// Outer rings cooler, core hotter.
		h := clamp01(heat * (1 - rel*0.45))
		out = append(out, colorByHeat(h, s).Render(line))
	}
	return strings.Join(out, "\n")
}

func renderScaledArt(lines []string, heightScale float64, s Styles) string {
	if len(lines) == 0 {
		return ""
	}
	n := 2 + int(float64(len(lines)-2)*clamp01(heightScale))
	if n > len(lines) {
		n = len(lines)
	}
	if n < 2 {
		n = 2
	}
	start := len(lines) - n
	var b strings.Builder
	for i := start; i < len(lines); i++ {
		line := lines[i]
		rel := float64(i-start) / float64(max(n-1, 1))
		// Top of plume = tip (hot), base = core.
		heat := 1 - rel*0.55
		b.WriteString(colorByHeat(heat, s).Render(line))
		if i < len(lines)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func colorByHeat(rel float64, s Styles) lipgloss.Style {
	switch {
	case rel >= 0.75:
		return s.Tip
	case rel >= 0.4:
		return s.Mid
	default:
		return s.Core
	}
}

// modePalette returns a heat gradient tuned to the active voice state.
func modePalette(mode Mode, base Styles) Styles {
	s := base
	switch mode {
	case ModeListening:
		// Cool cyan radar.
		if !isZeroStyle(base.Accent) {
			s.Tip = base.Accent.Bold(true)
			s.Mid = base.Accent
			s.Core = base.Muted
			s.Label = base.Accent
			s.Border = base.Accent
			s.Badge = base.Accent.Bold(true).Reverse(true).Padding(0, 1)
		}
	case ModeHearing:
		// Amber / fire recording plume.
		if !isZeroStyle(base.Hearing) {
			s.Tip = base.Hearing.Bold(true)
			s.Mid = base.Hearing
			if !isZeroStyle(base.Fire) {
				s.Core = base.Fire
			}
			s.Label = base.Hearing
			s.Border = base.Hearing
			s.Badge = base.Hearing.Bold(true).Reverse(true).Padding(0, 1)
		}
	case ModeSpeaking, ModeSynthesizing:
		// Purple / magenta speech.
		if !isZeroStyle(base.Speak) {
			s.Tip = base.Speak.Bold(true)
			s.Mid = base.Speak
			s.Core = base.Muted
			s.Label = base.Speak
			s.Border = base.Speak
			s.Badge = base.Speak.Bold(true).Reverse(true).Padding(0, 1)
		}
	case ModeThinking, ModeTranscribing:
		if !isZeroStyle(base.Think) {
			s.Tip = base.Think.Bold(true)
			s.Mid = base.Think
			s.Core = base.Muted
			s.Label = base.Think
			s.Border = base.Think
			s.Badge = base.Think.Bold(true).Reverse(true).Padding(0, 1)
		}
	case ModeError:
		s.Tip = base.Error.Bold(true)
		s.Mid = base.Error
		s.Core = base.Error
		s.Label = base.Error
		s.Border = base.Error
		s.Badge = base.Error.Bold(true).Reverse(true).Padding(0, 1)
	}
	return s
}

func isZeroStyle(s lipgloss.Style) bool {
	// A style with neither fg nor bg set is treated as unset optional accent.
	return s.GetForeground() == (lipgloss.Color("")) && s.GetBackground() == (lipgloss.Color(""))
}

func effectiveLevel(mode Mode, level float64, frame int) float64 {
	level = clamp01(level)
	switch mode {
	case ModeListening:
		return 0.18 + 0.12*math.Abs(math.Sin(float64(frame)*0.35))
	case ModeHearing:
		if level < 0.08 {
			return 0.28 + 0.12*math.Abs(math.Sin(float64(frame)*0.7))
		}
		return level
	case ModeTranscribing:
		return 0.35 + 0.12*math.Abs(math.Sin(float64(frame)*0.5))
	case ModeThinking:
		return 0.22 + 0.1*math.Abs(math.Sin(float64(frame)*0.28))
	case ModeSynthesizing:
		return 0.4 + 0.25*math.Abs(math.Sin(float64(frame)*0.55))
	case ModeSpeaking:
		if level < 0.15 {
			return 0.5 + 0.4*math.Abs(math.Sin(float64(frame)*0.55))
		}
		return level
	case ModeError:
		return 0.55
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
			return "◉"
		case ModeError:
			return "✗"
		default:
			return "·"
		}
	}
	switch mode {
	case ModeListening:
		return []string{"◎", "◉", "●", "◉"}[frame%4]
	case ModeHearing:
		return []string{"🎙", "◉", "🎙", "◎"}[frame%4]
	case ModeTranscribing:
		return []string{"◐", "◓", "◑", "◒"}[frame%4]
	case ModeThinking:
		return []string{"✦", "✧", "✦", "·"}[frame%4]
	case ModeSynthesizing:
		return []string{"♪", "♫", "♪", "✦"}[frame%4]
	case ModeSpeaking:
		return []string{"◉", "◎", "●", "◎"}[frame%4]
	case ModeError:
		return "⚠"
	default:
		return "·"
	}
}

func meterWidth(panelWidth int) int {
	if panelWidth <= 0 {
		return 24
	}
	w := panelWidth / 2
	if w < 16 {
		w = 16
	}
	if w > 40 {
		w = 40
	}
	return w
}

func spectrumCols(panelWidth int) int {
	w := panelWidth - 10
	if w < 20 {
		w = 20
	}
	if w > 48 {
		w = 48
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

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
