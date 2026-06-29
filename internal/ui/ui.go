package ui

import (
	"fmt"
	"strings"

	"github.com/Obedience-Corp/samantha/internal/events"
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

	return u
}

// ShowWelcome displays the startup banner.
func (u *UI) ShowWelcome() {
	label := fmt.Sprintf("%s (Go)", u.name)
	pad := 40 - len(label)
	if pad < 0 {
		pad = 2
	}
	fmt.Println()
	fmt.Println("  ╭──────────────────────────────────────────╮")
	fmt.Printf("  │  %s%s│\n", label, strings.Repeat(" ", pad))
	fmt.Println("  │  Ultra-low-latency voice assistant       │")
	fmt.Println("  ╰──────────────────────────────────────────╯")
	fmt.Println()
	fmt.Println("  Say something, and I'll respond.")
	fmt.Println("  Press Ctrl+C to exit.")
	fmt.Println()
}

// ShowGoodbye displays the exit message.
func (u *UI) ShowGoodbye() {
	fmt.Println()
	fmt.Println("  See you later.")
	fmt.Println()
}

// ShowProviders shows active providers.
func (u *UI) ShowProviders(ttsProvider, sttProvider string) {
	fmt.Printf("  TTS: %s | STT: %s\n", ttsProvider, sttProvider)
}

func (u *UI) onSTTPhase(e events.STTPhase) {
	if e.Elapsed > 0 {
		fmt.Printf("    %s (%.1fs)\n", e.Phase, e.Elapsed.Seconds())
		return
	}
	switch e.Phase {
	case "listening":
		fmt.Print("  🎙 Listening...\r")
	case "hearing":
		fmt.Print("  🎙 Hearing you...\r")
	case "transcribing":
		fmt.Print("  ● Transcribing...\r")
	}
}

func (u *UI) onTranscriptPartial(e events.TranscriptPartial) {
	fmt.Printf("\033[2K  You (partial): %s\r", e.Text)
}

func (u *UI) onUserInput(e events.UserInput) {
	fmt.Printf("\033[2K  You: %s\n", e.Text)
}

func (u *UI) onThinkingStarted(_ events.ThinkingStarted) {
	fmt.Printf("  ● %s thinking...\r", u.name)
}

func (u *UI) onThinkingComplete(e events.ThinkingComplete) {
	fmt.Printf("\033[2K    model complete (%.1fs)\n", e.Elapsed.Seconds())
}

func (u *UI) onResponseStreamingStarted(e events.ResponseStreamingStarted) {
	fmt.Printf("\033[2K    first response chunk (%.1fs)\n", e.Elapsed.Seconds())
}

func (u *UI) onSpeechSegmentReady(_ events.SpeechSegmentReady) {
	fmt.Print("  ● Queuing speech...\r")
}

func (u *UI) onGeneratingVoice(_ events.GeneratingVoice) {
	fmt.Print("  ● Synthesizing voice...\r")
}

func (u *UI) onVoiceGenerated(e events.VoiceGenerated) {
	fmt.Printf("\033[2K    voice ready (%.1fs)\n", e.Elapsed.Seconds())
}

func (u *UI) onSpeakingStarted(_ events.SpeakingStarted) {
	fmt.Print("  ● Speaking...\r")
}

func (u *UI) onSpeakingComplete(e events.SpeakingComplete) {
	label := "playback"
	if e.Interrupted {
		label = "playback interrupted"
	}
	fmt.Printf("\033[2K    %s (%.1fs)\n", label, e.Elapsed.Seconds())
}

func (u *UI) onSpeakingInterrupted(e events.SpeakingInterrupted) {
	fmt.Printf("\033[2K    speech interrupted (%s)\n", e.Reason)
}

func (u *UI) onResponseReady(e events.ResponseReady) {
	fmt.Printf("  %s: %s\n\n", u.name, e.Response)
}

func (u *UI) onCleared(_ events.ConversationCleared) {
	fmt.Println("  Conversation cleared.")
}

func (u *UI) onError(e events.Error) {
	if e.Stage != "" {
		fmt.Printf("  Error [%s]: %s\n\n", e.Stage, e.Message)
		return
	}
	fmt.Printf("  Error: %s\n\n", e.Message)
}

func (u *UI) onInfo(e events.Info) {
	fmt.Printf("  %s\n", e.Message)
}
