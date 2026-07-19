// Package anim renders terminal voice-activity visuals for Samantha.
//
// Design intent: a calm voice assistant meter — equalizer bars and a soft
// pulse — not festival fire art. Listening breathes, hearing rides mic
// level, speaking rides playback energy.
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
	Tip     lipgloss.Style // peak bars
	Mid     lipgloss.Style // mid bars
	Core    lipgloss.Style // base bars
	Muted   lipgloss.Style
	Label   lipgloss.Style
	Error   lipgloss.Style
	Accent  lipgloss.Style
	Hearing lipgloss.Style
	Speak   lipgloss.Style
	Think   lipgloss.Style
	Border  lipgloss.Style
	Badge   lipgloss.Style
}

var barGlyphs = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

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

// Waveform is a single-line equalizer of the given width.
func Waveform(frame int, level float64, width int, s Styles) string {
	if width < 8 {
		width = 8
	}
	if width > 64 {
		width = 64
	}
	level = clamp01(level)
	var b strings.Builder
	b.Grow(width * 12)
	for i := 0; i < width; i++ {
		pos := float64(i) / float64(max(width-1, 1))
		// Gentle center bias — reads as stereo energy, not a flame plume.
		center := 0.55 + 0.45*(1-math.Abs(pos-0.5)*2)
		phase := float64((frame*2+i*3)%24) / 24
		wiggle := 0.18 * math.Sin(phase*2*math.Pi)
		h := clamp01(level*center + wiggle*level*0.85)
		if level < 0.08 {
			// Idle listening floor.
			h = 0.12 + 0.08*math.Abs(math.Sin(float64(frame)*0.35+float64(i)*0.2))
		}
		idx := int(h * float64(len(barGlyphs)-1))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(barGlyphs) {
			idx = len(barGlyphs) - 1
		}
		rel := float64(idx) / float64(len(barGlyphs)-1)
		b.WriteString(colorByHeat(rel, s).Render(string(barGlyphs[idx])))
	}
	return b.String()
}

// Spectrum is a multi-row equalizer (rows tall).
func Spectrum(frame int, level float64, cols, rows int, s Styles) string {
	if cols < 12 {
		cols = 12
	}
	if cols > 56 {
		cols = 56
	}
	if rows < 1 {
		rows = 1
	}
	if rows > 4 {
		rows = 4
	}
	level = clamp01(level)
	heights := make([]int, cols)
	for i := 0; i < cols; i++ {
		pos := float64(i) / float64(max(cols-1, 1))
		lobe := math.Sin(pos * math.Pi)
		lobe *= lobe
		phase := float64((frame*2+i*3)%20) / 20
		wiggle := 0.2 * math.Sin(phase*2*math.Pi+float64(i)*0.3)
		h := clamp01(level*(0.4+0.6*lobe) + wiggle*level)
		if level < 0.08 {
			h = 0.15 + 0.1*math.Abs(math.Sin(float64(frame)*0.3+float64(i)*0.25))
		}
		heights[i] = int(math.Round(h * float64(rows)))
		if heights[i] > rows {
			heights[i] = rows
		}
		if heights[i] < 1 && level > 0.05 {
			heights[i] = 1
		}
	}
	var lines []string
	for r := rows; r >= 1; r-- {
		var b strings.Builder
		for i := 0; i < cols; i++ {
			if heights[i] >= r {
				rel := float64(r) / float64(rows)
				g := "█"
				if heights[i] == r {
					g = "▄"
				}
				b.WriteString(colorByHeat(rel, s).Render(g))
			} else {
				b.WriteString(" ")
			}
		}
		lines = append(lines, b.String())
	}
	return strings.Join(lines, "\n")
}

// Pulse is a one-line soft activity indicator (listening/thinking).
func Pulse(mode Mode, frame int, s Styles) string {
	dots := []string{"·  ·  ·", "  ·  · ", "·   ·  ", " ·  ·  "}
	d := dots[frame%len(dots)]
	switch mode {
	case ModeListening:
		return s.Accent.Render("◎  ") + s.Muted.Render(d) + s.Accent.Render("  listening")
	case ModeThinking:
		spin := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		return s.Think.Render(spin[frame%len(spin)]+"  ") + s.Muted.Render("thinking")
	case ModeTranscribing:
		spin := []string{"◐", "◓", "◑", "◒"}
		return s.Think.Render(spin[frame%len(spin)]+"  ") + s.Muted.Render("transcribing")
	case ModeError:
		return s.Error.Render("✗  error")
	default:
		return s.Muted.Render(d)
	}
}

// Stage is the compact voice strip under the header: EQ + status line.
// Intentionally not multi-line ASCII art — a voice meter, not a logo.
func Stage(mode Mode, frame int, heightScale float64, width int, label string, s Styles, reduced bool) string {
	if mode == ModeIdle {
		return ""
	}
	level := effectiveLevel(mode, heightScale, frame)
	if label == "" {
		label = modeLabel(mode)
	}
	palette := modePalette(mode, s)

	if reduced {
		wave := Waveform(0, level, meterWidth(width), palette)
		return stripCard(wave+"\n"+statusLine(mode, label, level, palette, true), width, palette)
	}

	var body string
	switch mode {
	case ModeListening, ModeThinking, ModeTranscribing:
		// Calm pulse + thin waveform floor.
		body = Pulse(mode, frame, palette) + "\n" + Waveform(frame, level, meterWidth(width), palette)
	case ModeHearing, ModeSpeaking, ModeSynthesizing:
		rows := 2
		if width >= 80 {
			rows = 3
		}
		body = Spectrum(frame, level, spectrumCols(width), rows, palette) + "\n" +
			statusLine(mode, label, level, palette, false)
	case ModeError:
		body = Pulse(mode, frame, palette)
	default:
		body = Waveform(frame, level, meterWidth(width), palette)
	}
	return stripCard(body, width, palette)
}

// Panel is an alias for Stage (call-site compatibility).
func Panel(mode Mode, frame int, heightScale float64, width int, label string, s Styles, reduced bool) string {
	return Stage(mode, frame, heightScale, width, label, s, reduced)
}

// CompactMeter is a single-line header chip: glyph + short EQ + label.
func CompactMeter(mode Mode, frame int, level float64, label string, s Styles, reduced bool) string {
	if mode == ModeIdle {
		return ""
	}
	palette := modePalette(mode, s)
	level = effectiveLevel(mode, level, frame)
	waveW := 12
	if reduced {
		waveW = 8
	}
	wave := Waveform(frame, level, waveW, palette)
	if label == "" {
		label = modeLabel(mode)
	}
	pct := int(level * 100)
	return modeGlyph(mode, frame, reduced) + " " + wave + " " +
		palette.Label.Render(fmt.Sprintf("%s %d%%", label, pct))
}

func stripCard(body string, width int, s Styles) string {
	if width < 24 {
		return body
	}
	inner := width - 4
	if inner > 72 {
		inner = 72
	}
	if inner < 20 {
		inner = 20
	}
	border := s.Border
	if isZeroStyle(border) {
		border = s.Mid
	}
	card := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border.GetForeground()).
		Padding(0, 1).
		Width(inner).
		Render(body)
	return lipgloss.PlaceHorizontal(width, lipgloss.Center, card)
}

func statusLine(mode Mode, label string, level float64, s Styles, reduced bool) string {
	pct := int(clamp01(level) * 100)
	g := modeGlyph(mode, 0, reduced)
	return s.Label.Render(fmt.Sprintf("%s  %s  ·  %d%%", g, label, pct))
}

func colorByHeat(rel float64, s Styles) lipgloss.Style {
	switch {
	case rel >= 0.7:
		return s.Tip
	case rel >= 0.35:
		return s.Mid
	default:
		return s.Core
	}
}

func modePalette(mode Mode, base Styles) Styles {
	s := base
	switch mode {
	case ModeListening:
		if !isZeroStyle(base.Accent) {
			s.Tip, s.Mid, s.Core = base.Accent.Bold(true), base.Accent, base.Muted
			s.Label, s.Border = base.Accent, base.Accent
		}
	case ModeHearing:
		if !isZeroStyle(base.Hearing) {
			s.Tip, s.Mid, s.Core = base.Hearing.Bold(true), base.Hearing, base.Muted
			s.Label, s.Border = base.Hearing, base.Hearing
		}
	case ModeSpeaking, ModeSynthesizing:
		if !isZeroStyle(base.Speak) {
			s.Tip, s.Mid, s.Core = base.Speak.Bold(true), base.Speak, base.Muted
			s.Label, s.Border = base.Speak, base.Speak
		}
	case ModeThinking, ModeTranscribing:
		if !isZeroStyle(base.Think) {
			s.Tip, s.Mid, s.Core = base.Think.Bold(true), base.Think, base.Muted
			s.Label, s.Border = base.Think, base.Think
		}
	case ModeError:
		s.Tip, s.Mid, s.Core = base.Error.Bold(true), base.Error, base.Error
		s.Label, s.Border = base.Error, base.Error
	}
	return s
}

func isZeroStyle(s lipgloss.Style) bool {
	return s.GetForeground() == (lipgloss.Color("")) && s.GetBackground() == (lipgloss.Color(""))
}

func effectiveLevel(mode Mode, level float64, frame int) float64 {
	level = clamp01(level)
	switch mode {
	case ModeListening:
		return 0.2 + 0.1*math.Abs(math.Sin(float64(frame)*0.35))
	case ModeHearing:
		if level < 0.08 {
			return 0.3 + 0.12*math.Abs(math.Sin(float64(frame)*0.65))
		}
		return level
	case ModeTranscribing:
		return 0.3 + 0.1*math.Abs(math.Sin(float64(frame)*0.5))
	case ModeThinking:
		return 0.18 + 0.08*math.Abs(math.Sin(float64(frame)*0.28))
	case ModeSynthesizing:
		return 0.4 + 0.2*math.Abs(math.Sin(float64(frame)*0.5))
	case ModeSpeaking:
		if level < 0.15 {
			return 0.45 + 0.35*math.Abs(math.Sin(float64(frame)*0.55))
		}
		return level
	case ModeError:
		return 0.4
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
			return "◎"
		case ModeSpeaking, ModeSynthesizing:
			return "●"
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
		return []string{"◉", "◎", "◉", "●"}[frame%4]
	case ModeTranscribing, ModeThinking:
		return []string{"⠋", "⠙", "⠹", "⠸"}[frame%4]
	case ModeSynthesizing, ModeSpeaking:
		return []string{"●", "◉", "◎", "◉"}[frame%4]
	case ModeError:
		return "✗"
	default:
		return "·"
	}
}

func meterWidth(panelWidth int) int {
	if panelWidth <= 0 {
		return 28
	}
	w := panelWidth - 8
	if w < 16 {
		w = 16
	}
	if w > 48 {
		w = 48
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
