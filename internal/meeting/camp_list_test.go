package meeting

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestParseCampaignListJSON(t *testing.T) {
	raw := `[
	  {"id":"abc","name":"My_Tools","org":"devtools","path":"/tmp/My_Tools","status":"active","type":"product"},
	  {"id":"def","name":"  ","org":"x","path":"/tmp/x","status":"active","type":"product"},
	  {"id":"ghi","name":"obey-campaign","org":"obc","path":"/tmp/obey","status":"active","type":"product"}
	]`
	camps, err := ParseCampaignListJSON([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(camps) != 2 {
		t.Fatalf("len = %d, want 2 (blank name skipped)", len(camps))
	}
	if camps[0].Name != "My_Tools" || camps[1].Name != "obey-campaign" {
		t.Fatalf("names = %q, %q", camps[0].Name, camps[1].Name)
	}
}

func TestListCampaignsNotOnPath(t *testing.T) {
	camps, err := ListCampaigns(context.Background(),
		func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("Run should not be called when camp is missing")
			return nil, nil
		},
		func(string) (string, error) { return "", os.ErrNotExist },
	)
	if err != nil {
		t.Fatalf("err = %v, want nil soft miss", err)
	}
	if len(camps) != 0 {
		t.Fatalf("camps = %v, want empty", camps)
	}
}

func TestListCampaignsParsesRunnerOutput(t *testing.T) {
	var gotArgs []string
	camps, err := ListCampaigns(context.Background(),
		func(_ context.Context, name string, args ...string) ([]byte, error) {
			gotArgs = append([]string{name}, args...)
			return []byte(`[{"name":"My_Tools","org":"devtools"}]`), nil
		},
		func(string) (string, error) { return "/usr/local/bin/camp", nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotArgs) < 3 || gotArgs[0] != "/usr/local/bin/camp" || gotArgs[1] != "list" || gotArgs[2] != "--json" {
		t.Fatalf("args = %v", gotArgs)
	}
	if len(camps) != 1 || camps[0].Name != "My_Tools" {
		t.Fatalf("camps = %+v", camps)
	}
}

func TestListCampaignsRunnerError(t *testing.T) {
	_, err := ListCampaigns(context.Background(),
		func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("boom")
		},
		func(string) (string, error) { return "camp", nil },
	)
	if err == nil || !strings.Contains(err.Error(), "camp list") {
		t.Fatalf("err = %v", err)
	}
}

func TestMergeDestinationsPrefersConfigured(t *testing.T) {
	configured := []Destination{
		{ID: "my", Type: TypeCampaign, Campaign: "My_Tools", Capture: "note", Tags: []string{"hand"}},
		{ID: "docs", Type: TypeFile, Path: "/tmp/docs"},
	}
	discovered := []Destination{
		{ID: "camp:My_Tools", Type: TypeCampaign, Campaign: "My_Tools", Capture: "intent"},
		{ID: "camp:Other", Type: TypeCampaign, Campaign: "Other", Capture: "intent"},
	}
	got := MergeDestinations(configured, discovered)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3: %+v", len(got), got)
	}
	if got[0].Capture != "note" || got[0].ID != "my" {
		t.Fatalf("configured campaign overwritten: %+v", got[0])
	}
	if got[2].Campaign != "Other" {
		t.Fatalf("expected discovered Other, got %+v", got)
	}
}

func TestDestinationFromCampaign(t *testing.T) {
	d := DestinationFromCampaign(Campaign{Name: "My_Tools", Org: "devtools"})
	if d.ID != "camp:My_Tools" || d.Type != TypeCampaign || d.Campaign != "My_Tools" || d.Capture != "intent" {
		t.Fatalf("dest = %+v", d)
	}
	if !strings.Contains(DestinationLabel(d), "My_Tools") {
		t.Fatalf("label = %q", DestinationLabel(d))
	}
}

func TestDiscoverDestinationsMergesCampList(t *testing.T) {
	r := &Router{
		Cfg: Config{
			Destinations: []Destination{
				{ID: "docs", Type: TypeFile, Path: "/tmp/out"},
			},
		},
		LookPath: func(name string) (string, error) {
			if name == "camp" {
				return "/bin/camp", nil
			}
			return "", os.ErrNotExist
		},
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`[{"name":"My_Tools"},{"name":"obey-campaign"}]`), nil
		},
	}
	got := r.DiscoverDestinations(context.Background())
	if len(got) != 3 {
		t.Fatalf("dests = %+v, want file + 2 campaigns", got)
	}
	ids := map[string]bool{}
	for _, d := range got {
		ids[d.ID] = true
	}
	if !ids["docs"] || !ids["camp:My_Tools"] || !ids["camp:obey-campaign"] {
		t.Fatalf("ids = %v", ids)
	}
}

func TestDiscoverDestinationsSoftFailsCampList(t *testing.T) {
	r := &Router{
		Cfg: Config{
			Destinations: []Destination{
				{ID: "docs", Type: TypeFile, Path: "/tmp/out"},
			},
		},
		LookPath: func(string) (string, error) { return "camp", nil },
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("registry locked")
		},
	}
	got := r.DiscoverDestinations(context.Background())
	if len(got) != 1 || got[0].ID != "docs" {
		t.Fatalf("got = %+v, want only configured file dest", got)
	}
}

func TestWithDestination(t *testing.T) {
	cfg := Config{Destinations: []Destination{{ID: "a", Type: TypeFile, Path: "/a"}}}
	dest := Destination{ID: "camp:X", Type: TypeCampaign, Campaign: "X"}
	got := WithDestination(cfg, dest)
	if _, ok := got.DestinationByID("camp:X"); !ok {
		t.Fatalf("missing dest: %+v", got.Destinations)
	}
	// Idempotent.
	got2 := WithDestination(got, dest)
	if len(got2.Destinations) != 2 {
		t.Fatalf("duplicated: %+v", got2.Destinations)
	}
}
