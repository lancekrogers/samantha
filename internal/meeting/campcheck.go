package meeting

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrImportMeetingUnsupported marks a camp binary without the CI0009
// `idea notes import-meeting` importer. Callers that would default to the
// meetings sink must fail closed on this rather than fall back to `idea add`
// — filing a meeting as a lifecycle intent is the misroute CI0009 exists to
// prevent.
var ErrImportMeetingUnsupported = errors.New("camp does not support `idea notes import-meeting` (CI0009); update camp")

// SupportsImportMeeting verifies the installed camp offers the CI0009
// meetings importer. Probing `--help` costs one fast process spawn and reads
// the truth from the binary actually on PATH, which matters because serve
// runs for days while camp gets upgraded underneath it — so callers should
// re-check per route rather than cache.
func SupportsImportMeeting(ctx context.Context, run Runner, look LookPath) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if run == nil {
		run = DefaultRunner
	}
	campBin := "camp"
	if look != nil {
		p, err := look("camp")
		if err != nil || strings.TrimSpace(p) == "" {
			return fmt.Errorf("%w: camp is not on PATH", ErrImportMeetingUnsupported)
		}
		campBin = p
	}
	out, err := run(ctx, campBin, "idea", "notes", "import-meeting", "--help")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		detail := strings.TrimSpace(string(out))
		if detail != "" {
			return fmt.Errorf("%w: %s", ErrImportMeetingUnsupported, firstLine(detail))
		}
		return fmt.Errorf("%w: %v", ErrImportMeetingUnsupported, err)
	}
	return nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
