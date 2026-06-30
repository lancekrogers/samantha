package render

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// RendererVersion identifies output-affecting renderer behavior (text
// segmentation, synthesis adaptation, WAV encoding). Bump it when a change
// should invalidate previously rendered outputs so resume re-renders them.
const RendererVersion = "1"

// SynthIdentity is an optional Synthesizer capability. A synthesizer that can
// describe its output-affecting identity (e.g. TTS provider and model) lets
// resume invalidate cached outputs when the engine changes underneath an
// otherwise-identical request. Synthesizers that do not implement it contribute
// an empty identity (resume then keys only on the document and explicit
// voice/speed settings).
type SynthIdentity interface {
	Identity() string
}

func synthIdentity(s Synthesizer) string {
	if id, ok := s.(SynthIdentity); ok {
		return id.Identity()
	}
	return ""
}

// resumeKey is the stable fingerprint of a single rendered output. It folds in
// every input that changes the produced audio: renderer version, source
// identity, format, voice, speed, synthesizer identity (provider/model), the
// output path, and the normalized text. A prior segment whose key matches a
// freshly computed key — and whose output file still exists — may be skipped on
// resume. Fields are domain-separated so distinct field boundaries cannot
// collide.
func resumeKey(opts Options, synthID, text, output string) string {
	h := sha256.New()
	for _, field := range []string{
		"rv=" + RendererVersion,
		"src=" + sourceLabel(opts),
		"fmt=" + string(opts.ResolveFormat()),
		"voice=" + opts.Voice,
		"speed=" + strconv.FormatFloat(opts.Speed, 'f', -1, 64),
		"synth=" + synthID,
		"out=" + output,
		"text=" + normalizeWhitespace(text),
	} {
		h.Write([]byte(field))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// resumable reports whether a prior segment lets us skip re-rendering: it must
// match the recomputed key, still have its output file on disk, and not be a
// failed segment. Failed outputs stay visible and are always retried.
func resumable(prior ManifestSegment, key, outPath string) bool {
	return prior.ResumeKey == key && prior.Status != StatusFailed && pathExists(outPath)
}
