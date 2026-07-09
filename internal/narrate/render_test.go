package narrate

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lancekrogers/samantha/internal/render"
)

func TestRecordRenderOutputsMatchesManifestFilenames(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.md")
	if err := os.WriteFile(src, []byte("# One\n\nFirst.\n\n# Two\n\nSecond.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(dir, "p.yaml")
	if _, err := BuildPlan(context.Background(), PlanOptions{Input: src, Out: planPath}, nil); err != nil {
		t.Fatal(err)
	}
	plan, err := Load(planPath)
	if err != nil {
		t.Fatal(err)
	}

	outDir := filepath.Join(dir, "audio")
	manifest := render.RenderManifest{Segments: []render.ManifestSegment{
		{ID: plan.Sections[0].ID, Output: "001-one.wav", Status: render.StatusComplete},
		{ID: plan.Sections[1].ID, Output: "002-two.wav", Status: render.StatusFailed},
	}}
	if err := RecordRenderOutputs(plan, planPath, outDir, manifest); err != nil {
		t.Fatal(err)
	}

	reloaded, err := Load(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := reloaded.Sections[0].AudioPath, filepath.Join("audio", "001-one.wav"); got != want {
		t.Fatalf("completed section audio_path = %q, want %q", got, want)
	}
	if got := reloaded.Sections[1].AudioPath; got != plan.Sections[1].AudioPath || got == filepath.Join("audio", "002-two.wav") {
		t.Fatalf("failed section audio_path must keep planned value, got %q", got)
	}
}
