package meeting

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestSupportsImportMeeting(t *testing.T) {
	okLook := func(string) (string, error) { return "/usr/local/bin/camp", nil }

	tests := []struct {
		name    string
		run     Runner
		look    LookPath
		wantErr error
	}{
		{
			name: "camp with CI0009 passes",
			run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return []byte("Import a .meeting bundle…"), nil
			},
			look: okLook,
		},
		{
			name: "old camp without the importer is refused with remediation",
			run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return []byte("Error: unknown command \"import-meeting\" for \"camp idea notes\"\nRun 'camp --help'"),
					fmt.Errorf("exit status 1")
			},
			look:    okLook,
			wantErr: ErrImportMeetingUnsupported,
		},
		{
			name: "camp missing from PATH is refused",
			run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				t.Fatal("runner must not be called when camp is absent")
				return nil, nil
			},
			look:    func(string) (string, error) { return "", fmt.Errorf("not found") },
			wantErr: ErrImportMeetingUnsupported,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := SupportsImportMeeting(context.Background(), tt.run, tt.look)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("SupportsImportMeeting() error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("SupportsImportMeeting() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestSupportsImportMeetingHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := SupportsImportMeeting(ctx, nil, func(string) (string, error) { return "/bin/camp", nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SupportsImportMeeting() with cancelled ctx error = %v, want context.Canceled", err)
	}
}

func TestSupportsImportMeetingProbesTheRightSubcommand(t *testing.T) {
	var gotArgs []string
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = append([]string{name}, args...)
		return nil, nil
	}
	if err := SupportsImportMeeting(context.Background(), run, nil); err != nil {
		t.Fatalf("SupportsImportMeeting() error = %v", err)
	}
	want := []string{"camp", "idea", "notes", "import-meeting", "--help"}
	if len(gotArgs) != len(want) {
		t.Fatalf("probe argv = %v, want %v", gotArgs, want)
	}
	for i := range want {
		if gotArgs[i] != want[i] {
			t.Fatalf("probe argv = %v, want %v", gotArgs, want)
		}
	}
}
