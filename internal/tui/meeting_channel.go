package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/listen"
	"github.com/lancekrogers/samantha/internal/meeting"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

// waitMeetingCh drains one message from the listen-loop bridge channel.
func waitMeetingCh(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return meetingLoopDoneMsg{}
		}
		return meetingChMsg{msg: msg, ch: ch}
	}
}

type meetingChMsg struct {
	msg tea.Msg
	ch  <-chan tea.Msg
}

// trySendMeeting never blocks. Used for droppable phase/level/partial samples.
func trySendMeeting(ch chan<- tea.Msg, msg tea.Msg) {
	select {
	case ch <- msg:
	default:
	}
}

// sendMeeting delivers durable UI messages (utterances, errors, loop done).
// Blocks until the TUI drains a slot so backpressure never silently drops
// transcript lines or the terminal stop signal (capacity is 256; the consumer
// is continuous).
func sendMeeting(ch chan<- tea.Msg, msg tea.Msg) {
	ch <- msg
}

// meetingUISink bridges listen.Sink → TUI messages and dual-writes via Writer.
// Stop phrases end the session and are intentionally omitted from the log.
type meetingUISink struct {
	ch      chan<- tea.Msg
	phrases map[string]bool
	stop    context.CancelFunc
	writer  *meetinglog.Writer
}

func (s *meetingUISink) OnUtterance(u listen.Utterance) error {
	if s.phrases != nil && s.phrases[meeting.NormalizeStopPhrase(u.Text)] {
		if s.stop != nil {
			s.stop()
		}
		return nil
	}
	if s.writer != nil {
		if err := s.writer.OnUtterance(u); err != nil {
			return err
		}
	}
	sendMeeting(s.ch, meetingUtteranceMsg(u))
	return nil
}

func (s *meetingUISink) OnTimeout() error {
	if s.writer != nil {
		return s.writer.OnTimeout()
	}
	return nil
}

func (s *meetingUISink) OnError(err error) error {
	if s.writer != nil {
		if werr := s.writer.OnError(err); werr != nil {
			return werr
		}
	}
	sendMeeting(s.ch, meetingErrorMsg{err: err})
	return nil
}
