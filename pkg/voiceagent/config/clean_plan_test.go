package config

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testPlanID = "736e7bbb35fd62946b65c8f4b9bd4dd1201c309eade46eb9cd1dc7602f58e489"

func TestParseCleanPlanRejectsWhatItCannotTrust(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr string
	}{
		{name: "empty", input: "   \n", wantErr: "empty"},
		{name: "not a plan at all", input: "delete everything", wantErr: "is not a dry-run document"},
		{name: "truncated plan id", input: "736e7bbb", wantErr: "is not a dry-run document"},
		{name: "uppercase plan id", input: strings.ToUpper(testPlanID), wantErr: "is not a dry-run document"},
		{name: "bare plan id names no install", input: "  " + testPlanID + "\n", wantErr: "does not name the models dir"},
		{
			name:    "document without a models dir",
			input:   `{"schema_version":2,"candidates":[],"plan_id":"` + CleanPlanID(nil) + `"}`,
			wantErr: "missing models_dir",
		},
		{name: "malformed json", input: `{"plan_id":`, wantErr: "clean plan"},
		{name: "document without a plan id", input: `{"schema_version":2,"candidates":[]}`, wantErr: "missing plan_id"},
		{
			name:    "document whose id does not describe its own list",
			input:   `{"schema_version":2,"models_dir":"/m","candidates":[{"path":"/m/stale.bin","rel":"stale.bin"}],"plan_id":"` + testPlanID + `"}`,
			wantErr: "does not match its candidate list",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := ParseCleanPlan([]byte(tc.input))
			if err == nil {
				t.Fatalf("ParseCleanPlan() error = nil, want %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.wantErr)
			}
			if plan.PlanID != "" {
				t.Errorf("a rejected plan must carry nothing, got %+v", plan)
			}
		})
	}
}

func TestParseCleanPlanAcceptsDocumentOrID(t *testing.T) {
	candidates := []CleanCandidate{{Path: "/m/stale.bin", Rel: "stale.bin", Size: 4, Category: CleanCategoryAsset, Kind: CleanKindFile}}
	document := `{"schema_version":2,"models_dir":"/m","candidates":[{"path":"/m/stale.bin","rel":"stale.bin","size_bytes":4,"category":"asset","kind":"file"}],"protected":[],"total_bytes":4,"plan_id":"` + CleanPlanID(candidates) + `"}`

	cases := []struct {
		name           string
		input          string
		wantCandidates int
	}{
		{name: "dry-run document", input: document, wantCandidates: 1},
		{
			name: "dry-run document with nothing to remove",
			input: `{"schema_version":2,"models_dir":"/m","candidates":[],"protected":[],"total_bytes":0,"plan_id":"` +
				CleanPlanID(nil) + `"}`,
			wantCandidates: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := ParseCleanPlan([]byte(tc.input))
			if err != nil {
				t.Fatalf("ParseCleanPlan() error = %v", err)
			}
			if plan.PlanID == "" {
				t.Error("an accepted plan must carry its id")
			}
			if len(plan.Candidates) != tc.wantCandidates {
				t.Errorf("candidates = %d, want %d", len(plan.Candidates), tc.wantCandidates)
			}
		})
	}
}

func TestDeleteCleanPlanSkipsWhatIsNoLongerRemovable(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	cases := []struct {
		name string
		ctx  context.Context
		// present is created on disk before the delete.
		present []string
		// planned and current are models-dir-relative paths.
		planned    []string
		current    []string
		wantErr    bool
		wantGone   []string
		wantKept   []string
		wantSkip   map[string]string
		wantFreed  int64
		wantDelete int
	}{
		{
			name:     "cancelled context deletes nothing",
			ctx:      cancelled,
			present:  []string{"stale.bin"},
			planned:  []string{"stale.bin"},
			current:  []string{"stale.bin"},
			wantErr:  true,
			wantKept: []string{"stale.bin"},
		},
		{
			name:     "a path that is required again is skipped, not deleted",
			ctx:      context.Background(),
			present:  []string{"stale.bin", "now-required.onnx"},
			planned:  []string{"stale.bin", "now-required.onnx"},
			current:  []string{"stale.bin"},
			wantGone: []string{"stale.bin"},
			wantKept: []string{"now-required.onnx"},
			wantSkip: map[string]string{"now-required.onnx": "no longer a removable candidate"},

			wantFreed:  4,
			wantDelete: 1,
		},
		{
			name:       "a path someone else already removed is skipped",
			ctx:        context.Background(),
			present:    []string{"stale.bin"},
			planned:    []string{"stale.bin", "vanished.bin"},
			current:    []string{"stale.bin", "vanished.bin"},
			wantGone:   []string{"stale.bin"},
			wantSkip:   map[string]string{"vanished.bin": "already gone"},
			wantFreed:  4,
			wantDelete: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, rel := range tc.present {
				if err := os.WriteFile(filepath.Join(dir, rel), []byte("data"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			toCandidates := func(rels []string) []CleanCandidate {
				out := make([]CleanCandidate, 0, len(rels))
				for _, rel := range rels {
					out = append(out, CleanCandidate{Path: filepath.Join(dir, rel), Rel: rel, Size: 4, Kind: CleanKindFile})
				}
				return out
			}

			result, err := DeleteCleanPlan(tc.ctx, dir, toCandidates(tc.planned), toCandidates(tc.current))
			if tc.wantErr {
				if err == nil {
					t.Fatal("DeleteCleanPlan() error = nil, want one")
				}
			} else if err != nil {
				t.Fatalf("DeleteCleanPlan() error = %v", err)
			}
			for _, rel := range tc.wantGone {
				if _, statErr := os.Stat(filepath.Join(dir, rel)); !os.IsNotExist(statErr) {
					t.Errorf("%s should have been deleted", rel)
				}
			}
			for _, rel := range tc.wantKept {
				if _, statErr := os.Stat(filepath.Join(dir, rel)); statErr != nil {
					t.Errorf("%s must not be deleted: %v", rel, statErr)
				}
			}
			if tc.wantErr {
				return
			}
			if len(result.Deleted) != tc.wantDelete || result.BytesFreed != tc.wantFreed {
				t.Errorf("result = %+v, want %d deleted and %d bytes freed", result, tc.wantDelete, tc.wantFreed)
			}
			if len(result.Skipped) != len(tc.wantSkip) {
				t.Fatalf("skipped = %+v, want %d", result.Skipped, len(tc.wantSkip))
			}
			for _, s := range result.Skipped {
				if want, ok := tc.wantSkip[s.Rel]; !ok || want != s.Reason {
					t.Errorf("skipped %q reason = %q, want %q", s.Rel, s.Reason, want)
				}
			}
			if result.SchemaVersion != CleanPlanSchemaVersion {
				t.Errorf("schema_version = %d, want %d", result.SchemaVersion, CleanPlanSchemaVersion)
			}
		})
	}
}

func TestPlanChangedErrorIsMachineReadable(t *testing.T) {
	err := NewPlanChangedError("aaa", "bbb")
	encoded, marshalErr := json.Marshal(err)
	if marshalErr != nil {
		t.Fatalf("marshalling: %v", marshalErr)
	}
	want := `{"error":"plan_changed","plan_id":"aaa","current_plan_id":"bbb"}`
	if string(encoded) != want {
		t.Errorf("payload = %s, want %s", encoded, want)
	}
	if !strings.Contains(err.Error(), "re-run the dry run") {
		t.Errorf("message = %q, want it to say what to do next", err.Error())
	}
}
