package brain

import "testing"

func TestChunkSentencesEmitsEachSentenceForLowLatency(t *testing.T) {
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
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0] != "Hello world." {
		t.Fatalf("got[0] = %q, want %q", got[0], "Hello world.")
	}
	if got[1] != "Second sentence." {
		t.Fatalf("got[1] = %q, want %q", got[1], "Second sentence.")
	}
}
