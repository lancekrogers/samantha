package remote

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

func TestStartNormalizesSource(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		want    string
		wantErr bool
	}{
		{name: "unknown source rejected", source: "quest", wantErr: true},
		{name: "empty defaults to ios", source: "", want: "ios"},
		{name: "watch accepted", source: "watch", want: "watch"},
		{name: "mac accepted", source: "mac", want: "mac"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := janitorManager(t, newRecordingPipeline(nil))
			session, err := m.Start(context.Background(), StartRequest{Title: "Standup", Source: tt.source})
			if tt.wantErr {
				if !errors.Is(err, ErrBadStart) {
					t.Fatalf("Start() error = %v, want ErrBadStart", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Start() error = %v", err)
			}
			doc, err := os.ReadFile(filepath.Join(session.BundlePath(), meetinglog.BundleDocumentName))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(doc), "# Source: "+tt.want) {
				t.Fatalf("bundle document missing %q:\n%s", "# Source: "+tt.want, doc)
			}
		})
	}
}

func TestStartWithCanceledContext(t *testing.T) {
	m, _ := janitorManager(t, newRecordingPipeline(nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Start(ctx, StartRequest{Source: "watch"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() with canceled context error = %v", err)
	}
}
