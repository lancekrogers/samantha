package tui

import (
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/events"
)

// demoVoiceAnimEnv enables a scripted listening→hearing→speaking sequence for
// VHS/termcast recordings. Real mic/TTS are not required; events drive the
// same meter path as production turns.
const demoVoiceAnimEnv = "SAMANTHA_DEMO_VOICE_ANIM"

type demoVoiceAnimStartedMsg struct{}

func demoVoiceAnimEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(demoVoiceAnimEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// startDemoVoiceAnim launches the scripted bus sequence in the background.
// The returned Cmd only signals that the script has been armed; animation
// continues via the normal bridge + voice tick loop.
func startDemoVoiceAnim(bus *events.Bus) tea.Cmd {
	if bus == nil {
		return nil
	}
	return func() tea.Msg {
		go runDemoVoiceAnim(bus)
		return demoVoiceAnimStartedMsg{}
	}
}

func runDemoVoiceAnim(bus *events.Bus) {
	sleep := func(d time.Duration) {
		time.Sleep(d)
	}
	emit := func(e events.Event) {
		bus.Emit(e)
	}

	// Ambient listen
	sleep(400 * time.Millisecond)
	emit(events.STTPhase{Phase: "listening"})
	sleep(1200 * time.Millisecond)

	// Rising mic energy → hearing
	for i := 0; i < 18; i++ {
		level := 0.15 + float64(i)*0.045
		if level > 1 {
			level = 1
		}
		emit(events.AudioLevel{Source: "input", Level: level})
		if i == 4 {
			emit(events.STTPhase{Phase: "hearing"})
		}
		if i == 10 {
			emit(events.TranscriptPartial{Text: "show me the voice meter"})
		}
		sleep(90 * time.Millisecond)
	}

	emit(events.STTPhase{Phase: "transcribing"})
	sleep(700 * time.Millisecond)

	emit(events.UserInput{Text: "show me the voice meter"})
	emit(events.ThinkingStarted{})
	sleep(1100 * time.Millisecond)

	emit(events.GeneratingVoice{Sentence: "Here is the live voice meter."})
	sleep(900 * time.Millisecond)

	emit(events.SpeakingStarted{Text: "Here is the live voice meter."})
	// Synthetic playback energy so the speaking art breathes.
	for i := 0; i < 22; i++ {
		// Triangle envelope 0.3..0.95..0.35
		var level float64
		if i < 11 {
			level = 0.3 + float64(i)*0.06
		} else {
			level = 0.95 - float64(i-11)*0.05
		}
		if level < 0.25 {
			level = 0.25
		}
		emit(events.AudioLevel{Source: "output", Level: level})
		sleep(100 * time.Millisecond)
	}

	emit(events.SpeakingComplete{Elapsed: 2200 * time.Millisecond})
	emit(events.ResponseReady{Response: "Here is the live voice meter — listening, hearing your level, and speaking."})
	sleep(1800 * time.Millisecond)

	// Second cycle: quieter listen → short speak
	emit(events.STTPhase{Phase: "listening"})
	sleep(900 * time.Millisecond)
	for i := 0; i < 10; i++ {
		emit(events.AudioLevel{Source: "input", Level: 0.25 + float64(i)*0.05})
		if i == 2 {
			emit(events.STTPhase{Phase: "hearing"})
		}
		sleep(80 * time.Millisecond)
	}
	emit(events.SpeakingStarted{Text: "Ready when you are."})
	sleep(1600 * time.Millisecond)
	emit(events.SpeakingComplete{})
	emit(events.STTPhase{Phase: "listening"})
	// Hold listening for the end of the GIF.
	sleep(2 * time.Second)
}
