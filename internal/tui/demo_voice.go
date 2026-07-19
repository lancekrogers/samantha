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
	sleep := func(d time.Duration) { time.Sleep(d) }
	emit := func(e events.Event) { bus.Emit(e) }

	// Ambient listen — cyan radar
	sleep(350 * time.Millisecond)
	emit(events.STTPhase{Phase: "listening"})
	sleep(1600 * time.Millisecond)

	// Rising mic energy → amber hearing plume
	for i := 0; i < 22; i++ {
		level := 0.12 + float64(i)*0.04
		if level > 1 {
			level = 1
		}
		// Add a little jitter so the spectrum feels alive.
		jitter := 0.05 * float64((i*3)%5) / 5
		emit(events.AudioLevel{Source: "input", Level: clampDemo(level + jitter)})
		if i == 3 {
			emit(events.STTPhase{Phase: "hearing"})
		}
		if i == 8 {
			emit(events.TranscriptPartial{Text: "show me that voice stage"})
		}
		if i == 16 {
			emit(events.TranscriptPartial{Text: "show me that voice stage at full power"})
		}
		sleep(85 * time.Millisecond)
	}

	emit(events.STTPhase{Phase: "transcribing"})
	sleep(650 * time.Millisecond)

	emit(events.UserInput{Text: "show me that voice stage at full power"})
	emit(events.ThinkingStarted{})
	sleep(1200 * time.Millisecond)

	emit(events.GeneratingVoice{Sentence: "Watch the meter ride your voice."})
	sleep(900 * time.Millisecond)

	emit(events.SpeakingStarted{Text: "Watch the meter ride your voice."})
	// Dramatic triangle + pulse envelope for speaking art.
	for i := 0; i < 28; i++ {
		var level float64
		switch {
		case i < 8:
			level = 0.35 + float64(i)*0.08
		case i < 18:
			level = 0.95 - 0.15*float64((i%4))/4
		default:
			level = 0.85 - float64(i-18)*0.05
		}
		emit(events.AudioLevel{Source: "output", Level: clampDemo(level)})
		sleep(95 * time.Millisecond)
	}

	emit(events.SpeakingComplete{Elapsed: 2600 * time.Millisecond})
	emit(events.ResponseReady{Response: "Watch the meter ride your voice — listening, hearing, and speaking, all live."})
	sleep(1500 * time.Millisecond)

	// Second cycle: quick listen → hot hearing → short speak
	emit(events.STTPhase{Phase: "listening"})
	sleep(800 * time.Millisecond)
	for i := 0; i < 12; i++ {
		emit(events.AudioLevel{Source: "input", Level: clampDemo(0.3 + float64(i)*0.06)})
		if i == 1 {
			emit(events.STTPhase{Phase: "hearing"})
		}
		sleep(75 * time.Millisecond)
	}
	emit(events.SpeakingStarted{Text: "Ready when you are."})
	for i := 0; i < 14; i++ {
		emit(events.AudioLevel{Source: "output", Level: clampDemo(0.4 + 0.4*float64(i%5)/5)})
		sleep(90 * time.Millisecond)
	}
	emit(events.SpeakingComplete{})
	emit(events.STTPhase{Phase: "listening"})
	// Hold listening for the end of the GIF.
	sleep(2200 * time.Millisecond)
}

func clampDemo(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
