package brain

import (
	"strings"
	"unicode"
)

// sentencesPerChunk controls how many sentences are batched before sending to TTS.
// Larger = smoother playback, but longer wait before first audio.
const sentencesPerChunk = 3

// ChunkSentences reads text chunks from input and emits batches of sentences for TTS.
// Batches sentencesPerChunk sentences together for smoother playback.
func ChunkSentences(input <-chan string) <-chan string {
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
					batch := strings.Join(sentences, " ")
					out <- cleanForVoice(batch)
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
			batch := strings.Join(sentences, " ")
			out <- cleanForVoice(batch)
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
				if isAbbreviation(prevWord) {
					continue
				}
			}

			// Must be followed by space, end, or quote
			if i == len(runes)-1 {
				return i
			}
			next := runes[i+1]
			if unicode.IsSpace(next) || next == '"' || next == '\'' {
				return i
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

func isAbbreviation(word string) bool {
	abbrevs := map[string]bool{
		"Mr": true, "Mrs": true, "Ms": true, "Dr": true,
		"Prof": true, "Sr": true, "Jr": true, "St": true,
		"vs": true, "etc": true, "Inc": true, "Ltd": true,
		"i": true, "e": true, // i.e., e.g.
	}
	return abbrevs[word]
}
