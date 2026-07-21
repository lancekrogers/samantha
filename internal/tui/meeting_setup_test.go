package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/meeting"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

func TestMeetingSetupAdvancesToRouteStep(t *testing.T) {
	m := newMeetingSetup(&config.Config{})
	m.width, m.height = 80, 24
	m.input.SetValue("Standup")

	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.step != meetingSetupRoute {
		t.Fatalf("step = %d, want route", m.step)
	}
	if !m.loading {
		t.Fatal("expected destination discovery loading")
	}
	if cmd == nil {
		t.Fatal("expected loadDestinations command")
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "2/2") || !strings.Contains(view, "Standup") {
		t.Fatalf("route step view:\n%s", view)
	}
}

func TestMeetingSetupRoutePickLocal(t *testing.T) {
	m := newMeetingSetup(&config.Config{})
	m.step = meetingSetupRoute
	m.loading = false
	m.input.SetValue("Notes")
	m.cursor = 0

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected startMeetingMsg command")
	}
	msg, ok := cmd().(startMeetingMsg)
	if !ok {
		t.Fatalf("msg type = %T", cmd())
	}
	if msg.Description != "Notes" || msg.RoutePlan != routePlanLocal {
		t.Fatalf("start msg = %+v", msg)
	}
}

func TestMeetingSetupRoutePickDestination(t *testing.T) {
	m := newMeetingSetup(&config.Config{})
	m.step = meetingSetupRoute
	m.loading = false
	m.dests = []meeting.Destination{
		{ID: "camp:My_Tools", Type: meeting.TypeCampaign, Campaign: "My_Tools"},
	}
	m.cursor = 2 // first destination after local + ask
	m.input.SetValue("Sync")

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	msg := cmd().(startMeetingMsg)
	if msg.RoutePlan != routePlanDest || msg.Destination.Campaign != "My_Tools" {
		t.Fatalf("start msg = %+v", msg)
	}
}

func TestMeetingSetupAppliesDiscoveredDests(t *testing.T) {
	m := newMeetingSetup(&config.Config{
		Meeting: config.MeetingConfig{
			Route: config.MeetingRouteConfig{Default: "camp:My_Tools"},
		},
	})
	m.loadSeq = 3
	m.step = meetingSetupRoute
	m.loading = true

	m, _ = m.Update(meetingDestsMsg{
		seq: 3,
		dests: []meeting.Destination{
			{ID: "camp:My_Tools", Type: meeting.TypeCampaign, Campaign: "My_Tools"},
			{ID: "camp:Other", Type: meeting.TypeCampaign, Campaign: "Other"},
		},
	})
	if m.loading {
		t.Fatal("still loading")
	}
	if m.cursor != 2 { // preselect default dest (index 0 + 2)
		t.Fatalf("cursor = %d, want 2 for default dest", m.cursor)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "My_Tools") || !strings.Contains(view, "campaign") {
		t.Fatalf("view missing campaign dests:\n%s", view)
	}
}

func TestBeginMeetingRouteHonorsLocalPlan(t *testing.T) {
	app := App{cfg: &config.Config{}}
	app.meetingRoutePlan = meetingRoutePlan{Kind: routePlanLocal}
	app.launcher = newLauncher(&config.Config{}, nil, nil)

	cmd := app.beginMeetingRoute(meetinglog.Summary{File: "/tmp/a.log", JSONLFile: "/tmp/a.jsonl"})
	if cmd != nil {
		t.Fatal("local plan should not return a route cmd")
	}
	if app.screen == screenMeetingRoute {
		t.Fatal("local plan must not open route picker")
	}
	if app.launcher.banner == "" {
		t.Fatal("expected skip banner")
	}
	if !strings.Contains(strings.ToLower(app.launcher.banner), "local") {
		t.Fatalf("banner = %q", app.launcher.banner)
	}
}

func TestBeginMeetingRouteDestPlanReturnsCmd(t *testing.T) {
	app := App{cfg: &config.Config{}}
	app.meetingRoutePlan = meetingRoutePlan{
		Kind: routePlanDest,
		Dest: meeting.Destination{ID: "docs", Type: meeting.TypeFile, Path: t.TempDir()},
	}
	cmd := app.beginMeetingRoute(meetinglog.Summary{File: "/tmp/a.log", JSONLFile: "/tmp/a.jsonl"})
	if cmd == nil {
		t.Fatal("dest plan should return async route cmd")
	}
}

func TestStartMeetingMsgStoresRoutePlan(t *testing.T) {
	app := App{cfg: &config.Config{}, runCtx: t.Context()}
	model, _ := app.Update(startMeetingMsg{
		Description: "X",
		RoutePlan:   routePlanLocal,
	})
	got := model.(App)
	if got.meetingRoutePlan.Kind != routePlanLocal {
		t.Fatalf("plan = %+v", got.meetingRoutePlan)
	}
	if got.screen != screenMeeting {
		t.Fatalf("screen = %v, want meeting", got.screen)
	}
}

func TestSettingsSpeakerToggle(t *testing.T) {
	cfg := &config.Config{}
	m := newSettings(cfg, nil)
	m.section = sectionSpeaker
	var saved map[string]any
	m.saveConfig = func(key string, value any) error {
		if saved == nil {
			saved = map[string]any{}
		}
		saved[key] = value
		return nil
	}
	m.selectCurrent()
	if saved["speaker.enabled"] != true || !cfg.Speaker.Enabled {
		t.Fatalf("master enable failed: saved=%v cfg=%+v", saved, cfg.Speaker)
	}
	m.cursor = 1
	m.selectCurrent()
	if saved["speaker.meeting.enabled"] != true || !cfg.Speaker.Meeting.Enabled {
		t.Fatalf("meeting enable failed: saved=%v", saved)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "Speaker analysis") || !strings.Contains(view, "Meeting diarization") {
		t.Fatalf("speaker settings view:\n%s", view)
	}
}
