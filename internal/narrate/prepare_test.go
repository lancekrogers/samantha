package narrate

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lancekrogers/samantha/internal/brain"
)

type fakeBatch struct {
	n int
}

func (f *fakeBatch) Transform(ctx context.Context, req brain.BatchRequest) (brain.BatchResult, error) {
	f.n++
	return brain.BatchResult{Text: "spoken: " + req.Text, Provider: "fake", Model: "m1"}, nil
}

func TestPrepareResumeSkipsUnchanged(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.md")
	if err := os.WriteFile(src, []byte("# A\n\nHi.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(dir, "p.yaml")
	if _, err := BuildPlan(context.Background(), PlanOptions{Input: src, Out: planPath}, nil); err != nil {
		t.Fatal(err)
	}
	fb := &fakeBatch{}
	res, err := Prepare(context.Background(), PrepareOptions{
		PlanPath: planPath,
		Batch:    fb,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Prepared != 1 || fb.n != 1 {
		t.Fatalf("first prepare: %+v n=%d", res, fb.n)
	}
	res2, err := Prepare(context.Background(), PrepareOptions{
		PlanPath: planPath,
		Batch:    fb,
		Resume:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Skipped != 1 || fb.n != 1 {
		t.Fatalf("resume: %+v n=%d", res2, fb.n)
	}
}
