package platforminfo

import (
	"strings"
	"testing"
)

func TestInstallRemediationsArePlatformSpecific(t *testing.T) {
	tests := []struct {
		goos        string
		popplerWant string
		calibreWant string
		binaryWant  string
	}{
		{goos: "darwin", popplerWant: "brew install poppler", calibreWant: "brew install --cask calibre", binaryWant: "/Applications/calibre.app"},
		{goos: "linux", popplerWant: "pacman -S poppler", calibreWant: "pacman -S calibre", binaryWant: "/opt/calibre/calibredb"},
		{goos: "windows", popplerWant: "Poppler for Windows", calibreWant: "command-line tools to PATH", binaryWant: "calibredb.exe"},
		{goos: "freebsd", popplerWant: "ensure pdftotext is on PATH", calibreWant: "ensure calibredb is on PATH", binaryWant: "Ensure calibredb is on PATH"},
	}

	for _, tt := range tests {
		t.Run(tt.goos, func(t *testing.T) {
			if got := PopplerInstallRemediation(tt.goos); !strings.Contains(got, tt.popplerWant) {
				t.Errorf("PopplerInstallRemediation(%q) = %q, want %q", tt.goos, got, tt.popplerWant)
			}
			if got := CalibreInstallRemediation(tt.goos); !strings.Contains(got, tt.calibreWant) {
				t.Errorf("CalibreInstallRemediation(%q) = %q, want %q", tt.goos, got, tt.calibreWant)
			}
			if got := CalibreBinaryHint(tt.goos); !strings.Contains(got, tt.binaryWant) {
				t.Errorf("CalibreBinaryHint(%q) = %q, want %q", tt.goos, got, tt.binaryWant)
			}
		})
	}
}

func TestMissingCalibreDetailIncludesBinaryName(t *testing.T) {
	got := MissingCalibreDetail("linux", "custom-calibredb")
	for _, want := range []string{"custom-calibredb", "pacman -S calibre", "/opt/calibre/calibredb"} {
		if !strings.Contains(got, want) {
			t.Errorf("MissingCalibreDetail() = %q, want %q", got, want)
		}
	}
}
