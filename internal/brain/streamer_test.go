package brain

import "testing"

func TestChunkSentencesKeepsInitialismsIntact(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			"e.g. mid-sentence",
			[]string{"Try fruit, e.g. apples or pears. ", "Next thought. "},
			[]string{"Try fruit, e.g. apples or pears.", "Next thought."},
		},
		{
			"i.e. mid-sentence",
			[]string{"The plan, i.e. the schedule, still holds. "},
			[]string{"The plan, i.e. the schedule, still holds."},
		},
		{
			"e.g. split across stream chunks",
			[]string{"Pick a number, e.g", ". four. "},
			[]string{"Pick a number, e.g. four."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := make(chan string, len(tt.in))
			out := ChunkSentences(in)
			for _, chunk := range tt.in {
				in <- chunk
			}
			close(in)

			var got []string
			for chunk := range out {
				got = append(got, chunk)
			}

			if len(got) != len(tt.want) {
				t.Fatalf("got %d chunks %q, want %d %q", len(got), got, len(tt.want), tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// This package segments; it does not batch. Batching for prosody lives in the
// pipeline, where it can tell which segments actually become audio — see
// TestVoiceBatchingCountsAudibleSegments in internal/pipeline.
func TestChunkSentencesEmitsOneSentencePerChunk(t *testing.T) {
	in := make(chan string, 2)
	out := ChunkSentences(in)

	in <- "Hello world. "
	in <- "Second sentence. "
	close(in)

	var got []string
	for chunk := range out {
		got = append(got, chunk)
	}

	if len(got) != 2 {
		t.Fatalf("len(got) = %d chunks %q, want 2", len(got), got)
	}
	if got[0] != "Hello world." {
		t.Fatalf("got[0] = %q, want %q", got[0], "Hello world.")
	}
	if got[1] != "Second sentence." {
		t.Fatalf("got[1] = %q, want %q", got[1], "Second sentence.")
	}
}

func TestChunkSentencesSegmentation(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			"each sentence is its own chunk",
			[]string{"One. ", "Two. ", "Three. ", "Four. ", "Five. "},
			[]string{"One.", "Two.", "Three.", "Four.", "Five."},
		},
		{
			"many sentences in a single input chunk",
			[]string{"One. Two. Three. "},
			[]string{"One.", "Two.", "Three."},
		},
		{
			"a lone sentence still emits",
			[]string{"Only one. "},
			[]string{"Only one."},
		},
		{
			"trailing text with no terminator still flushes",
			[]string{"One. ", "Two. ", "Three without a period"},
			[]string{"One.", "Two.", "Three without a period"},
		},
		{
			"whitespace only produces nothing",
			[]string{"   ", "\n"},
			nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := make(chan string, len(tt.in))
			out := ChunkSentences(in)
			for _, c := range tt.in {
				in <- c
			}
			close(in)

			var got []string
			for chunk := range out {
				got = append(got, chunk)
			}

			if len(got) != len(tt.want) {
				t.Fatalf("got %d chunks %q, want %d %q", len(got), got, len(tt.want), tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
