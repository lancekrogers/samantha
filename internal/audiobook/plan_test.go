package audiobook

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/render"
)

func TestClassifyAudiobookSections(t *testing.T) {
	tests := []struct {
		title, kind, suggestion string
	}{
		{"Title Page", "front_matter", DecisionExclude},
		{"Contents", "navigation", DecisionExclude},
		{"Index", "index", DecisionExclude},
		{"Introduction", "main_content", DecisionInclude},
		{"Thanks!", "back_matter", DecisionReview},
		{"Cheatsheet", "reference", DecisionReview},
	}
	for _, tt := range tests {
		kind, suggestion, _, _ := classify(tt.title, "A paragraph of substantive prose.")
		if kind != tt.kind || suggestion != tt.suggestion {
			t.Errorf("classify(%q) = %s/%s, want %s/%s", tt.title, kind, suggestion, tt.kind, tt.suggestion)
		}
	}
}

func TestBuildPlanWritesReviewArtifacts(t *testing.T) {
	input := filepath.Join("..", "..", "tests", "fixtures", "documents", "tiny-book.epub")
	outDir := t.TempDir()
	result, err := BuildPlan(context.Background(), PlanOptions{Input: input, OutDir: outDir, Format: render.FormatEPUB})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if result.PlanPath != PlanPath(outDir) || result.MDPath != filepath.Join(outDir, "production-plan.md") {
		t.Fatalf("result paths = %q/%q", result.PlanPath, result.MDPath)
	}
	if result.Plan.Schema != SchemaVersion || len(result.Plan.Sections) != 2 {
		t.Fatalf("plan = %#v", result.Plan)
	}
	for _, path := range []string{result.PlanPath, result.MDPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing %s: %v", path, err)
		}
	}
	for _, section := range result.Plan.Sections {
		if section.ExtractedPath == "" || section.EstimatedDurationMS <= 0 || section.Status != "pending" {
			t.Errorf("section missing review metadata: %+v", section)
		}
		if _, err := os.Stat(filepath.Join(outDir, section.ExtractedPath)); err != nil {
			t.Errorf("missing extracted section %s: %v", section.ExtractedPath, err)
		}
	}
	data, err := os.ReadFile(result.MDPath)
	if err != nil || !strings.Contains(string(data), "Audiobook production plan") || !strings.Contains(string(data), "Decision") {
		t.Fatalf("markdown preview = %q, err=%v", string(data), err)
	}
	if _, err := os.Stat(filepath.Join(outDir, ".narration-plan.yaml")); !os.IsNotExist(err) {
		t.Fatalf("bridge plan should be removed, stat err=%v", err)
	}
}

func TestApplyDecisionsAndRejectsConflicts(t *testing.T) {
	p := &Plan{
		Schema: SchemaVersion, Source: Source{Path: "book.epub", Format: "epub"},
		Defaults: Defaults{Decision: DecisionReview},
		Sections: []Section{
			{ID: "one", Order: 1, Kind: "main_content", Suggestion: DecisionInclude, Decision: DecisionInclude, ExtractedPath: "extracted/one.txt"},
			{ID: "two", Order: 2, Kind: "navigation", Suggestion: DecisionExclude, Decision: DecisionExclude, ExtractedPath: "extracted/two.txt"},
		},
	}
	if err := p.ApplyDecisions([]string{"one"}, []string{"two"}, "human reviewed"); err != nil {
		t.Fatalf("ApplyDecisions() error = %v", err)
	}
	if p.Sections[0].DecisionReason != "human reviewed" || len(p.Unresolved()) != 0 {
		t.Fatalf("updated plan = %+v", p)
	}
	if err := p.ApplyDecisions([]string{"one"}, []string{"one"}, "conflict"); err == nil {
		t.Fatal("conflicting decisions should fail")
	}
	if err := p.ApplyDecisions([]string{"missing"}, nil, "unknown"); err == nil {
		t.Fatal("unknown section should fail")
	}
}

func TestLooksLikeIndex(t *testing.T) {
	if !looksLikeIndex("Alpha ........ 1\nBeta ........ 2\nGamma ........ 3\nDelta ........ 4") {
		t.Fatal("expected index-shaped text")
	}
	if looksLikeIndex("This is ordinary prose.\nIt has several lines.\nBut it is not an index.") {
		t.Fatal("ordinary prose should not look like an index")
	}
}
