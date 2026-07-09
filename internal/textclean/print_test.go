package textclean

import "testing"

func TestCleanPrintArtifacts(t *testing.T) {
	cases := map[string]struct {
		in, want string
	}{
		"hyphenation": {
			in:   "docu-\nment text",
			want: "document text",
		},
		"page number": {
			in:   "Hello world.\n\n12\n\nNext page.",
			want: "Hello world.\n\nNext page.",
		},
		"wrapped paragraph": {
			in:   "This is a long\nline that wraps.",
			want: "This is a long line that wraps.",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := CleanPrintArtifacts(tc.in); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}
