package brain

import (
	"strings"
	"unicode"
)

// sentencesPerChunk is 1: this package segments, it does not batch.
//
// Batching for prosody — one sentence for the opening, two thereafter — lives in
// the pipeline, and it has to. The property worth protecting is "the first
// AUDIBLE chunk is short", and this package cannot tell which chunks become
// audio: the pipeline's voice gate suppresses tool output and code fences, and
// cleaning can empty a segment outright.
//
// Batching here counted emitted chunks instead, so a reply that opens with a
// suppressed tool line spent its one-sentence budget on silence, and the first
// thing the listener actually heard was a two-sentence synthesis — doubling
// time-to-first-audio in exactly the case the policy exists to protect.
//
// See internal/pipeline/pipeline.go, where speakability is known.
const sentencesPerChunk = 1

// CleanForVoice strips markdown and vocal fillers from text bound for TTS.
// Exported for consumers that must inspect the model's original structure
// before it is cleaned — the pipeline's voice gate needs the markdown fences
// this removes, so it gates first and cleans after.
func CleanForVoice(s string) string { return cleanForVoice(s) }

// HasSpeakableContent reports whether cleaned voice text contains more than a
// standalone voiced filler. Streaming consumers use the same predicate as the
// provider finalizer so a filler-only reply cannot be spoken on one path while
// being replaced by the recovery line on another.
func HasSpeakableContent(s string) bool { return hasSpeakableContent(s) }

// ChunkSentences reads text chunks from input and emits one voice-cleaned
// sentence at a time for TTS.
func ChunkSentences(input <-chan string) <-chan string {
	out := make(chan string, 4)
	go func() {
		defer close(out)
		for batch := range ChunkSentencesRaw(input) {
			out <- cleanForVoice(batch)
		}
	}()
	return out
}

// ChunkSentencesRaw segments the stream exactly like ChunkSentences but emits
// the model's original text, fences and all. Callers that route a batch to
// both the transcript and TTS clean it themselves, after any structural
// inspection.
func ChunkSentencesRaw(input <-chan string) <-chan string {
	out := make(chan string, 4)

	go func() {
		defer close(out)
		var buf strings.Builder
		var sentences []string

		for chunk := range input {
			buf.WriteString(chunk)

			// Extract complete sentences from buffer.
			for {
				text := buf.String()
				idx := findSentenceEnd(text)
				if idx < 0 {
					break
				}
				sentence := strings.TrimSpace(text[:idx+1])
				if sentence != "" {
					sentences = append(sentences, sentence)
				}
				buf.Reset()
				buf.WriteString(text[idx+1:])

				// Emit batch when we have enough sentences.
				if len(sentences) >= sentencesPerChunk {
					out <- strings.Join(sentences, " ")
					sentences = sentences[:0]
				}
			}
		}

		// Flush remaining sentences + any trailing text.
		remaining := strings.TrimSpace(buf.String())
		if remaining != "" {
			sentences = append(sentences, remaining)
		}
		if len(sentences) > 0 {
			out <- strings.Join(sentences, " ")
		}
	}()

	return out
}

// findSentenceEnd returns the index of the first sentence-ending punctuation
// followed by a space or end of string. Returns -1 if no boundary found.
func findSentenceEnd(text string) int {
	runes := []rune(text)
	for i, r := range runes {
		if r == '.' || r == '?' || r == '!' {
			// Check for abbreviations (e.g., "Mr.", "Dr.", "etc.")
			if r == '.' && i > 0 && i < len(runes)-1 {
				prevWord := extractPrevWord(runes[:i])
				if isAbbreviation(prevWord) || isInitialism(runes[:i]) {
					continue
				}
			}

			// Must be followed by space or quote — NOT end of buffer.
			// End-of-buffer splits cause premature cuts on streaming partial text.
			if i < len(runes)-1 {
				next := runes[i+1]
				if unicode.IsSpace(next) || next == '"' || next == '\'' {
					return i
				}
			}
		}
	}
	return -1
}

func extractPrevWord(runes []rune) string {
	end := len(runes)
	start := end
	for start > 0 && unicode.IsLetter(runes[start-1]) {
		start--
	}
	return string(runes[start:end])
}

// isInitialism reports whether text ends in a letter-dot-letter run like
// "e.g" or "i.e", so the trailing dot is not a sentence end.
func isInitialism(runes []rune) bool {
	end := len(runes)
	start := end
	for start > 0 && unicode.IsLetter(runes[start-1]) {
		start--
	}
	return end-start == 1 && start > 0 && runes[start-1] == '.'
}

func isAbbreviation(word string) bool {
	abbrevs := map[string]bool{
		"Mr": true, "Mrs": true, "Ms": true, "Dr": true,
		"Prof": true, "Sr": true, "Jr": true, "St": true,
		"vs": true, "etc": true, "Inc": true, "Ltd": true,
		"i": true, "e": true, // i.e., e.g.
	}
	return abbrevs[word]
}
