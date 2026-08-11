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

// The opening chunk must stay a single sentence: time-to-first-audio waits on it
// finishing synthesis, so batching here would directly delay the first sound.
func TestChunkSentencesEmitsFirstSentenceAloneForLowLatency(t *testing.T) {
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
		t.Fatalf("got[0] = %q, want %q — the opening chunk must be one sentence", got[0], "Hello world.")
	}
	// The second sentence is a partial batch flushed at end of stream.
	if got[1] != "Second sentence." {
		t.Fatalf("got[1] = %q, want %q", got[1], "Second sentence.")
	}
}

// After the opening, sentences batch in pairs so the synthesiser can carry
// intonation across the boundary instead of resetting at every period.
func TestChunkSentencesBatchesTwoAfterTheOpening(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			"five sentences: one, then pairs, then a flushed remainder",
			[]string{"One. ", "Two. ", "Three. ", "Four. ", "Five. "},
			[]string{"One.", "Two. Three.", "Four. Five."},
		},
		{
			"four sentences batch evenly after the opening",
			[]string{"One. ", "Two. ", "Three. ", "Four. "},
			[]string{"One.", "Two. Three.", "Four."},
		},
		{
			"a lone sentence still emits",
			[]string{"Only one. "},
			[]string{"Only one."},
		},
		{
			"trailing text with no terminator still flushes",
			[]string{"One. ", "Two. ", "Three without a period"},
			[]string{"One.", "Two. Three without a period"},
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

// Each stream carries its own "have I emitted the opening yet" state. If that
// state were package-level, a second concurrent turn would start mid-policy and
// batch its opening chunk, delaying its first audio.
func TestChunkSentencesOpeningPolicyIsPerStream(t *testing.T) {
	for i := 0; i < 2; i++ {
		in := make(chan string, 3)
		out := ChunkSentences(in)
		in <- "First. "
		in <- "Second. "
		in <- "Third. "
		close(in)

		var got []string
		for chunk := range out {
			got = append(got, chunk)
		}
		if len(got) == 0 || got[0] != "First." {
			t.Fatalf("stream %d: got %q, want the opening chunk to be %q", i, got, "First.")
		}
	}
}
