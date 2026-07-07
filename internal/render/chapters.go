package render

import "context"

// RenderChapter is one render unit for multi-file (chaptered) output. It is a
// compatibility alias for RenderUnit kept until callers migrate; a later
// change removes it.
type RenderChapter struct {
	ID    string
	Title string
	Text  string
}

// RenderChapters renders chapters through the generic unit path. It is a thin
// compatibility wrapper over RenderUnits kept until callers migrate.
func RenderChapters(ctx context.Context, opts Options, chapters []RenderChapter, synth Synthesizer, writeWAV WAVWriter) (RenderManifest, error) {
	return RenderUnits(ctx, opts, chapterUnits(chapters), synth, writeWAV)
}

func chapterUnits(chapters []RenderChapter) []RenderUnit {
	units := make([]RenderUnit, len(chapters))
	for i, ch := range chapters {
		units[i] = RenderUnit{ID: ch.ID, Title: ch.Title, Text: ch.Text}
	}
	return units
}

// chapterFilename keeps the locked filename rules reachable through the
// chapter compatibility surface.
func chapterFilename(index int, ch RenderChapter) string {
	return unitFilename(index, RenderUnit{ID: ch.ID, Title: ch.Title})
}
