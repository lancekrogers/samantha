package ui

import (
	"fmt"

	"github.com/lancekrogers/samantha/internal/events"
)

// UI handles terminal output for the voice assistant.
type UI struct {
	bus  *events.Bus
	name string
}

// New creates a UI and subscribes it to the event bus.
func New(bus *events.Bus, agentName string) *UI {
	u := &UI{bus: bus, name: agentName}

	events.Subscribe(bus, u.onSTTPhase)
	events.Subscribe(bus, u.onTranscriptPartial)
	events.Subscribe(bus, u.onUserInput)
	events.Subscribe(bus, u.onThinkingStarted)
	events.Subscribe(bus, u.onThinkingComplete)
	events.Subscribe(bus, u.onResponseStreamingStarted)
	events.Subscribe(bus, u.onSpeechSegmentReady)
	events.Subscribe(bus, u.onGeneratingVoice)
	events.Subscribe(bus, u.onVoiceGenerated)
	events.Subscribe(bus, u.onSpeakingStarted)
	events.Subscribe(bus, u.onSpeakingComplete)
	events.Subscribe(bus, u.onSpeakingInterrupted)
	events.Subscribe(bus, u.onResponseReady)
	events.Subscribe(bus, u.onCleared)
	events.Subscribe(bus, u.onError)
	events.Subscribe(bus, u.onInfo)
	events.Subscribe(bus, u.onToolCallStarted)
	events.Subscribe(bus, u.onToolCallFinished)

	return u
}

// status prints a dim metric/phase line, clearing any transient text first.
func (u *UI) status(text string) {
	fmt.Printf("\033[2K%s\n", dimStyle.Render(text))
}

// ShowWelcome displays the startup banner.
func (u *UI) ShowWelcome() {
	banner := bannerStyle.Render(
		titleStyle.Render(fmt.Sprintf("%s (Go)", u.name)) + "\n" +
			dimStyle.Render("Ultra-low-latency voice assistant"),
	)
	fmt.Println()
	fmt.Println(banner)
	fmt.Println()
	fmt.Println(dimStyle.Render("  Say something, and I'll respond."))
	fmt.Println(dimStyle.Render("  Press Ctrl+C to exit."))
	fmt.Println()
}

// ShowGoodbye displays the exit message.
func (u *UI) ShowGoodbye() {
	fmt.Println()
	fmt.Println(dimStyle.Render("  See you later."))
	fmt.Println()
}

// ShowProviders shows active providers.
func (u *UI) ShowProviders(ttsProvider, sttProvider string) {
	fmt.Printf("  %s\n", dimStyle.Render(fmt.Sprintf("TTS: %s | STT: %s", ttsProvider, sttProvider)))
}

func (u *UI) onSTTPhase(e events.STTPhase) {
	if e.Elapsed > 0 {
		u.status(fmt.Sprintf("    %s (%.1fs)", e.Phase, e.Elapsed.Seconds()))
		return
	}
	switch e.Phase {
	case "listening":
		fmt.Printf("  %s\r", statusStyle.Render("🎙 Listening..."))
	case "hearing":
		fmt.Printf("  %s\r", statusStyle.Render("🎙 Hearing you..."))
	case "transcribing":
		fmt.Printf("  %s\r", dimStyle.Render("● Transcribing..."))
	}
}

func (u *UI) onTranscriptPartial(e events.TranscriptPartial) {
	fmt.Printf("\033[2K  %s %s\r", dimStyle.Render("You (partial):"), dimStyle.Render(e.Text))
}

func (u *UI) onUserInput(e events.UserInput) {
	fmt.Printf("\033[2K  %s %s\n", userStyle.Render("You:"), e.Text)
}

func (u *UI) onThinkingStarted(_ events.ThinkingStarted) {
	fmt.Printf("  %s\r", dimStyle.Render(fmt.Sprintf("● %s thinking...", u.name)))
}

func (u *UI) onThinkingComplete(e events.ThinkingComplete) {
	u.status(fmt.Sprintf("    model complete (%.1fs)", e.Elapsed.Seconds()))
}

func (u *UI) onResponseStreamingStarted(e events.ResponseStreamingStarted) {
	u.status(fmt.Sprintf("    first response chunk (%.1fs)", e.Elapsed.Seconds()))
}

func (u *UI) onSpeechSegmentReady(_ events.SpeechSegmentReady) {
	fmt.Printf("  %s\r", dimStyle.Render("● Queuing speech..."))
}

func (u *UI) onGeneratingVoice(_ events.GeneratingVoice) {
	fmt.Printf("  %s\r", dimStyle.Render("● Synthesizing voice..."))
}

func (u *UI) onVoiceGenerated(e events.VoiceGenerated) {
	u.status(fmt.Sprintf("    voice ready (%.1fs)", e.Elapsed.Seconds()))
}

func (u *UI) onSpeakingStarted(_ events.SpeakingStarted) {
	fmt.Printf("  %s\r", statusStyle.Render("● Speaking..."))
}

func (u *UI) onSpeakingComplete(e events.SpeakingComplete) {
	label := "playback"
	if e.Interrupted {
		label = "playback interrupted"
	}
	u.status(fmt.Sprintf("    %s (%.1fs)", label, e.Elapsed.Seconds()))
}

func (u *UI) onSpeakingInterrupted(e events.SpeakingInterrupted) {
	u.status(fmt.Sprintf("    speech interrupted (%s)", e.Reason))
}

func (u *UI) onResponseReady(e events.ResponseReady) {
	fmt.Printf("  %s %s\n\n", agentStyle.Render(u.name+":"), e.Response)
}

func (u *UI) onCleared(_ events.ConversationCleared) {
	fmt.Println(dimStyle.Render("  Conversation cleared."))
}

func (u *UI) onError(e events.Error) {
	msg := e.Message
	if e.Stage != "" {
		msg = fmt.Sprintf("[%s] %s", e.Stage, e.Message)
	}
	fmt.Printf("  %s\n\n", errorStyle.Render("Error: "+msg))
}

func (u *UI) onInfo(e events.Info) {
	fmt.Printf("  %s\n", dimStyle.Render(e.Message))
}

func (u *UI) onToolCallStarted(e events.ToolCallStarted) {
	msg := "🔧 " + e.Name
	if e.Summary != "" {
		msg += " (" + e.Summary + ")"
	}
	fmt.Printf("  %s\n", dimStyle.Render(msg))
}

func (u *UI) onToolCallFinished(e events.ToolCallFinished) {
	msg := "✓ " + e.Name
	if e.Err != "" {
		msg = "✗ " + e.Name + ": " + e.Err
	} else if e.Preview != "" {
		msg += " → " + e.Preview
	}
	fmt.Printf("  %s\n", dimStyle.Render(msg))
}
