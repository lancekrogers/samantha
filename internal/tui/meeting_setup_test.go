package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/meeting"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

func bundledMeetingSummary() meetinglog.Summary {
	return meetinglog.Summary{
		Bundle:    "/tmp/a.meeting",
		File:      "/tmp/a.meeting/meeting.md",
		JSONLFile: "/tmp/a.meeting/.samantha/events.jsonl",
	}
}

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

func TestMeetingSetupModeOffSkipsRouteStep(t *testing.T) {
	m := newMeetingSetup(&config.Config{
		Meeting: config.MeetingConfig{Route: config.MeetingRouteConfig{Mode: "off"}},
	})
	m.input.SetValue("Local only")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	msg := cmd().(startMeetingMsg)
	if msg.RoutePlan != routePlanLocal {
		t.Fatalf("plan = %q, want local", msg.RoutePlan)
	}
}

func TestMeetingSetupModeAutoWithConfiguredDefault(t *testing.T) {
	m := newMeetingSetup(&config.Config{
		Meeting: config.MeetingConfig{
			Route: config.MeetingRouteConfig{
				Mode:    "auto",
				Default: "docs",
				Destinations: []config.MeetingDestinationConfig{
					{ID: "docs", Type: "file", Path: "/tmp/docs"},
				},
			},
		},
	})
	m.input.SetValue("Auto meet")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	msg := cmd().(startMeetingMsg)
	if msg.RoutePlan != routePlanDest || msg.Destination.ID != "docs" {
		t.Fatalf("msg = %+v", msg)
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

	cmd := app.beginMeetingRoute(bundledMeetingSummary())
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
	cmd := app.beginMeetingRoute(bundledMeetingSummary())
	if cmd == nil {
		t.Fatal("dest plan should return async route cmd")
	}
}

func TestOpenMeetingRoutePickerIsAsync(t *testing.T) {
	app := App{
		cfg:      &config.Config{},
		launcher: newLauncher(&config.Config{}, nil, nil),
		width:    80,
		height:   24,
	}
	cmd := app.openMeetingRoutePicker(
		bundledMeetingSummary(),
		meeting.Config{Mode: meeting.ModeAsk},
	)
	if cmd == nil {
		t.Fatal("picker must return a discovery cmd (not block Update)")
	}
	if !strings.Contains(app.launcher.banner, "Discovering") {
		t.Fatalf("banner = %q", app.launcher.banner)
	}
	// Simulate empty discovery result.
	app.applyMeetingRouteReady(meetingRouteReadyMsg{
		summary: bundledMeetingSummary(),
		dests:   nil,
	})
	if app.screen == screenMeetingRoute {
		t.Fatal("empty dests must not open picker")
	}
}

func TestApplyMeetingRouteReadyOpensPicker(t *testing.T) {
	app := App{width: 80, height: 24, launcher: newLauncher(&config.Config{}, nil, nil)}
	app.applyMeetingRouteReady(meetingRouteReadyMsg{
		summary:  bundledMeetingSummary(),
		routeCfg: meeting.Config{Mode: meeting.ModeAsk, Body: meeting.BodyNotes},
		dests: []meeting.Destination{
			{ID: "camp:My_Tools", Type: meeting.TypeCampaign, Campaign: "My_Tools"},
		},
	})
	if app.screen != screenMeetingRoute {
		t.Fatalf("screen = %v, want meeting route", app.screen)
	}
	if len(app.meetingRoute.dests) != 1 {
		t.Fatalf("dests = %+v", app.meetingRoute.dests)
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

func TestSettingsMeetingDiarizationToggle(t *testing.T) {
	cfg := &config.Config{}
	m := newSettings(cfg, nil)
	m.section = sectionMeeting
	m.cursor = meetingRowDiarization
	var saved map[string]any
	m.saveConfig = func(key string, value any) error {
		if saved == nil {
			saved = map[string]any{}
		}
		saved[key] = value
		return nil
	}
	m.selectCurrent()
	if saved["speaker.enabled"] != true || saved["speaker.meeting.enabled"] != true {
		t.Fatalf("diarization enable saved=%v", saved)
	}
	if !cfg.Speaker.Enabled || !cfg.Speaker.Meeting.Enabled {
		t.Fatalf("cfg speaker = %+v", cfg.Speaker)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "Speaker diarization") || !strings.Contains(view, "Route mode") {
		t.Fatalf("meeting settings view missing diarization/routing:\n%s", view)
	}
	// Live controls stay out of Settings (conversation /speakers).
	if strings.Contains(view, "Live conversation") || strings.Contains(view, "Live mode") {
		t.Fatalf("live speaker rows should not be under Meeting:\n%s", view)
	}
}
