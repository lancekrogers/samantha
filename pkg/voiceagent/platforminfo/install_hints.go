// Package platforminfo centralizes user-facing platform capability and setup
// guidance. Callers pass GOOS explicitly so every branch stays unit-testable.
package platforminfo

import "fmt"

// PopplerInstallRemediation returns platform-appropriate setup guidance for
// the optional pdftotext dependency.
func PopplerInstallRemediation(goos string) string {
	switch goos {
	case "darwin":
		return "install Poppler with 'brew install poppler' to enable PDF extraction"
	case "linux":
		return "install Poppler to enable PDF extraction (Arch: sudo pacman -S poppler; Debian/Ubuntu: sudo apt install poppler-utils)"
	case "windows":
		return "install Poppler for Windows and add its bin directory to PATH to enable PDF extraction"
	default:
		return "install Poppler and ensure pdftotext is on PATH to enable PDF extraction"
	}
}

// CalibreInstallRemediation returns platform-appropriate setup guidance for
// the optional Calibre library integration.
func CalibreInstallRemediation(goos string) string {
	const finish = "open Calibre once to create a library, then run: samantha config calibre_enabled true"
	switch goos {
	case "darwin":
		return "Install Calibre with 'brew install --cask calibre' or from https://calibre-ebook.com, " + finish
	case "linux":
		return "Install Calibre from your distribution or https://calibre-ebook.com (Arch: sudo pacman -S calibre; Debian/Ubuntu: sudo apt install calibre), " + finish
	case "windows":
		return "Install Calibre from https://calibre-ebook.com, add its command-line tools to PATH, " + finish
	default:
		return "Install Calibre from https://calibre-ebook.com, ensure calibredb is on PATH, " + finish
	}
}

// CalibreBinaryHint explains where Samantha looks when Calibre is installed
// but calibredb is still unresolved.
func CalibreBinaryHint(goos string) string {
	switch goos {
	case "darwin":
		return "Set calibredb_binary to the full path if needed (macOS app: /Applications/calibre.app/Contents/MacOS/calibredb)"
	case "linux":
		return "Ensure calibredb is on PATH or set calibredb_binary to its full path (the upstream installer commonly uses /opt/calibre/calibredb)"
	case "windows":
		return "Add Calibre's install directory to PATH or set calibredb_binary to calibredb.exe's full path"
	default:
		return "Ensure calibredb is on PATH or set calibredb_binary to its full path"
	}
}

// MissingCalibreDetail is the common error detail used by CLI and TUI paths.
func MissingCalibreDetail(goos, name string) string {
	if name == "" {
		name = "calibredb"
	}
	return fmt.Sprintf(
		"%s not found; %s; %s",
		name,
		CalibreInstallRemediation(goos),
		CalibreBinaryHint(goos),
	)
}
