//go:build !integration

package cmd

import (
	"testing"

	"github.com/lancekrogers/samantha/internal/speaker"
)

func TestOwnerVerifyGate(t *testing.T) {
	cases := []struct {
		name       string
		st         speaker.LiveStats
		wantReject bool
	}{
		{"disabled fails open", speaker.LiveStats{Status: speaker.LiveDisabled, LastLabel: "speaker-2"}, false},
		{"unavailable fails open", speaker.LiveStats{Status: speaker.LiveUnavailable, LastLabel: "speaker-2"}, false},
		{"degraded fails open", speaker.LiveStats{Status: speaker.LiveDegraded, LastLabel: "speaker-2"}, false},
		{"no label yet fails open", speaker.LiveStats{Status: speaker.LiveHealthy}, false},
		{"enrolled owner allowed", speaker.LiveStats{Status: speaker.LiveHealthy, LastLabel: "lance", LastEnrollRev: "titanet"}, false},
		{"anonymous speaker rejected", speaker.LiveStats{Status: speaker.LiveHealthy, LastLabel: "speaker-2"}, true},
		{"running anonymous rejected", speaker.LiveStats{Status: speaker.LiveRunning, LastLabel: "speaker-2"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gate := ownerVerifyGate(func() speaker.LiveStats { return tc.st })
			reason := gate(tc.st.LastLabel)
			if got := reason != ""; got != tc.wantReject {
				t.Fatalf("gate(%+v) = %q, wantReject=%v", tc.st, reason, tc.wantReject)
			}
		})
	}
}
