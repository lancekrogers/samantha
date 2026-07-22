package tui

import (
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	ansi "github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/samantha/internal/meeting"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

// meetingResultsModel keeps the completed recording visible long enough to
// review speaker attribution before the existing routing flow continues.
type meetingResultsModel struct {
	summary    meetinglog.Summary
	events     []meetinglog.Event
	err        error
	width      int
	height     int
	ready      bool
	standalone bool
	view       viewport.Model
}

type meetingResultsDoneMsg struct{ summary meetinglog.Summary }

func newMeetingResults(summary meetinglog.Summary) meetingResultsModel {
	events, err := meeting.ReadEvents(summary.JSONLFile)
	return meetingResultsModel{summary: summary, events: events, err: err}
}

func (m meetingResultsModel) Init() tea.Cmd { return nil }

// standaloneMeetingResults adapts the App submodel's concrete Update return
// type to Bubble Tea's top-level tea.Model interface.
type standaloneMeetingResults struct{ meetingResultsModel }

func (m standaloneMeetingResults) Init() tea.Cmd { return nil }

func (m standaloneMeetingResults) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.meetingResultsModel.Update(msg)
	m.meetingResultsModel = next
	return m, cmd
}

func (m standaloneMeetingResults) View() string { return m.meetingResultsModel.View() }

func (m *meetingResultsModel) resize() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	chrome := 9
	if m.summary.SpeakerError != "" {
		chrome++
	}
	height := max(m.height-chrome, 3)
	if !m.ready {
		m.view = viewport.New(max(m.width-4, 1), height)
		m.ready = true
	} else {
		m.view.Width = max(m.width-4, 1)
		m.view.Height = height
	}
	m.view.SetContent(m.content())
}

func (m meetingResultsModel) Update(msg tea.Msg) (meetingResultsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "enter", "esc":
			if m.standalone {
				return m, tea.Quit
			}
			summary := m.summary
			return m, func() tea.Msg { return meetingResultsDoneMsg{summary: summary} }
		}
	}
	var cmd tea.Cmd
	m.view, cmd = m.view.Update(msg)
	return m, cmd
}

func (m meetingResultsModel) content() string {
	if m.err != nil {
		return errorStyle.Render("Could not load meeting results: " + m.err.Error())
	}
	var notes, bookmarks, transcript, attributed []meetinglog.Event
	for _, event := range m.events {
		switch event.Type {
		case meetinglog.TypeNote:
			notes = append(notes, event)
		case meetinglog.TypeBookmark:
			bookmarks = append(bookmarks, event)
		case meetinglog.TypeUtterance:
			transcript = append(transcript, event)
		case meetinglog.TypeSpeakerUtterance:
			attributed = append(attributed, event)
		}
	}
	if len(attributed) > 0 {
		transcript = attributed
	}

	var b strings.Builder
	if len(notes) > 0 {
		b.WriteString(headerStyle.Render("Notes"))
		b.WriteString("\n")
		for _, event := range notes {
			fmt.Fprintf(&b, "%s📝 %s\n", resultOffset(event.OffsetMs), event.Text)
		}
		b.WriteString("\n")
	}
	if len(bookmarks) > 0 {
		b.WriteString(headerStyle.Render("Important moments"))
		b.WriteString("\n")
		for _, event := range bookmarks {
			fmt.Fprintf(&b, "%s★ %s\n", resultOffset(event.OffsetMs), event.Text)
		}
		b.WriteString("\n")
	}

	heading := "Transcript"
	if len(attributed) > 0 {
		heading = "Speaker-attributed transcript"
	}
	b.WriteString(headerStyle.Render(heading))
	b.WriteString("\n")
	if len(transcript) == 0 {
		b.WriteString(dimStyle.Render("No utterances recorded."))
		b.WriteString("\n")
	} else {
		for _, event := range transcript {
			b.WriteString(resultOffset(event.OffsetMs))
			if event.Type == meetinglog.TypeSpeakerUtterance && event.Label != "" {
				b.WriteString(speakerLabelStyle(event.Label).Render(event.Label + ":"))
				b.WriteString(" ")
			}
			b.WriteString(event.Text)
			b.WriteString("\n")
		}
	}
	return lipgloss.NewStyle().Width(max(m.view.Width, 1)).Render(b.String())
}

func speakerLabelStyle(label string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(speakerColor(label)).Bold(true)
}

func speakerColor(label string) lipgloss.Color {
	normalized := strings.ToLower(strings.TrimSpace(label))
	if number, err := strconv.Atoi(strings.TrimPrefix(normalized, "speaker-")); err == nil && number > 0 {
		return speakerColors[(number-1)%len(speakerColors)]
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(normalized))
	return speakerColors[int(hash.Sum32()%uint32(len(speakerColors)))]
}

func resultOffset(ms int64) string {
	if ms <= 0 {
		return ""
	}
	d := (time.Duration(ms) * time.Millisecond).Round(time.Second)
	if d >= time.Hour {
		return fmt.Sprintf("[%d:%02d:%02d] ", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60)
	}
	return fmt.Sprintf("[%02d:%02d] ", int(d.Minutes()), int(d.Seconds())%60)
}

func (m meetingResultsModel) View() string {
	if !m.ready {
		return "\n  " + headerStyle.Render("Loading meeting results…") + "\n"
	}
	w := max(m.width, 1)
	desc := m.summary.Description
	if strings.TrimSpace(desc) == "" {
		desc = "meeting"
	}
	header := ansi.Truncate(titleStyle.Render("  Meeting complete")+"  "+normalStyle.Render(desc), w, "…")
	stats := fmt.Sprintf("  %s · %d spoken · %d notes · %d ★",
		m.summary.Duration().Round(time.Second), m.summary.Utterances, m.summary.Notes, m.summary.Bookmarks)
	if m.summary.SpeakerStatus != "" {
		stats += fmt.Sprintf(" · speakers %s", m.summary.SpeakerStatus)
		if m.summary.SpeakerCount > 0 {
			stats += fmt.Sprintf(" (%d)", m.summary.SpeakerCount)
		}
	}
	pathLine := ansi.Truncate("  Saved: "+m.summary.Bundle, w, "…")
	rule := lipgloss.NewStyle().Foreground(colorAccent).Render(strings.Repeat("─", w))
	footer := ansi.Truncate("  ↑/↓/pgup/pgdown review  •  enter/esc continue to routing", w, "…")
	analysisError := ""
	if m.summary.SpeakerError != "" {
		analysisError = errorStyle.Render(ansi.Truncate("  Speaker analysis: "+m.summary.SpeakerError, w, "…")) + "\n"
	}
	return header + "\n" + dimStyle.Render(stats) + "\n" + analysisError + dimStyle.Render(pathLine) + "\n" + rule + "\n" +
		m.view.View() + "\n" + rule + "\n" + dimStyle.Render(footer) + "\n"
}
