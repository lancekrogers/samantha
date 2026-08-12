package textclean

import "testing"

// Error cases first. Every entry here is a form that a naive number expansion
// garbles, and most of them are why general integer/decimal expansion was
// removed rather than guarded — see the package doc and decision D008.
func TestPrepareSpokenTextLeavesNumbersAlone(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"no digits at all", "Nothing numeric here."},
		{"plain integer — eSpeak already says it as words", "The answer is 42"},
		{"simple decimal — eSpeak already says it as words", "Roughly 3.14 times 13"},
		{"single digit", "Take 3 steps"},
		{"five digits", "It printed 12345 lines"},
		{"grouped thousands", "About 1,000 people"},
		// Adjacency: a four-digit run glued to something else is part of that
		// something else. Each of these was a real garbling before the
		// standalone rule existed.
		{"phone number", "call 555-1234 now"},
		{"ISO date", "Released 2026-08-11 finally"},
		{"year range", "The 1999-2000 season"},
		{"slash date", "Due 2026/08/11 sharp"},
		{"time", "Meet at 12:00 sharp"},
		{"currency", "It costs $5 today"},
		{"currency four digits", "It costs $1999 today"},
		{"euro", "€20 or $30"},
		{"version string", "Running 1.2.3 in production"},
		{"version with letter prefix", "Tagged v2 yesterday"},
		{"ordinal", "Finished 42nd overall"},
		{"resolution suffix", "Recorded at 1080p"},
		{"identifier with interior letters", "R2D2 and C3PO"},
		{"short hex", "commit 3f2a1 landed"},
		{"decimal with no leading digit", "It grew by .5 percent"},
		{"leading zero is an identifier", "Agent 007 reporting"},
		{"four-digit year glued to a letter", "The 1999s were loud"},
		{"decimal that starts with a year", "Value 1999.5 exactly"},
		{"negative number keeps eSpeak's handling", "It dropped -5 degrees"},
		{"range", "Pick 5-10 items"},
		{"ratio", "Ratio of 3:1 or better"},
		{"percentage", "That's a 50% gain"},
		{"degrees", "Temperature was 20°C"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PrepareSpokenText(tt.in); got != tt.in {
				t.Errorf("PrepareSpokenText(%q) = %q, want it unchanged", tt.in, got)
			}
		})
	}
}

// The one numeric rule that survived, because it is the one that measured a
// difference: eSpeak reads "1999" as a cardinal, and a person reads it as a year.
func TestPrepareSpokenTextReadsStandaloneYears(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"twentieth century", "Back in 1999", "Back in nineteen ninety-nine"},
		{"round century", "Back in 1900", "Back in nineteen hundred"},
		{"oh-something", "Back in 1905", "Back in nineteen oh five"},
		{"millennium", "Back in 2000", "Back in two thousand"},
		{"two thousand five", "Back in 2005", "Back in two thousand five"},
		{"twenty-tens", "Back in 2010", "Back in twenty ten"},
		{"current decade", "Back in 2026", "Back in twenty twenty-six"},
		{"bottom of range", "Back in 1100", "Back in eleven hundred"},
		{"top of range", "Back in 2099", "Back in twenty ninety-nine"},
		{"trailing period", "It was 1999.", "It was nineteen ninety-nine."},
		{"trailing comma", "In 1999, we shipped", "In nineteen ninety-nine, we shipped"},
		{"in parentheses", "The year (1999) mattered", "The year (nineteen ninety-nine) mattered"},
		{"two years in one sentence", "From 1999 to 2005", "From nineteen ninety-nine to two thousand five"},
		// Outside the range it stays a cardinal for eSpeak to render.
		{"just below range", "Back in 1099", "Back in 1099"},
		{"just above range", "Back in 2100", "Back in 2100"},
		{"1024 is below the range", "Exactly 1024 bytes", "Exactly 1024 bytes"},
		// Accepted collision: a technical constant inside the year range.
		// "Fourteen forty" is how it is said out loud anyway.
		{"accepted collision 1440", "Exactly 1440 minutes", "Exactly fourteen forty minutes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PrepareSpokenText(tt.in); got != tt.want {
				t.Errorf("PrepareSpokenText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestPrepareSpokenTextStripsEmoji(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"emoji mid sentence closes the gap", "Nice 👍 work", "Nice work"},
		{"emoji before punctuation", "Done! 🎉", "Done!"},
		{"trailing emoji", "All set 🚀", "All set"},
		{"emoticon block", "Sounds good 😀", "Sounds good"},
		{"symbol block check mark", "Shipped ✅ today", "Shipped today"},
		{"zwj sequence leaves nothing behind", "Family 👨‍👩‍👧 photo", "Family photo"},
		{"skin tone modifier", "Wave 👋🏽 hello", "Wave hello"},
		{"flag regional indicators", "From 🇺🇸 here", "From here"},
		{"emoji only becomes empty", "🎉", ""},
		// A keycap sequence is a digit wearing emoji decoration. Stripping the
		// decoration and keeping the digit preserves the meaning.
		{"keycap keeps its digit", "Press 1️⃣ now", "Press 1 now"},
		// Base character + U+FE0F: the selector is the text saying "this is
		// decoration". Both go, or a bare unpronounceable glyph reaches eSpeak.
		{"play symbol with presentation selector", "Press ▶️ now", "Press now"},
		{"pause symbol with presentation selector", "Hit ⏸️ now", "Hit now"},
		{"double exclamation emoji", "Wow ‼️ big", "Wow big"},
		{"information emoji", "See ℹ️ here", "See here"},
		{"small square emoji", "Bullet ▪️ item", "Bullet item"},
		// Emoji by default, no selector needed.
		{"watch", "Wait ⌚ a sec", "Wait a sec"},
		{"alarm clock", "Set ⏰ now", "Set now"},
		// Tag sequences: the base is removed and the invisible tag characters
		// must go with it, or they ride into the phonemizer unseen.
		{"tag sequence flag leaves no invisible tail", "From 🏴󠁧󠁢󠁳󠁣󠁴󠁿 here", "From here"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PrepareSpokenText(tt.in); got != tt.want {
				t.Errorf("PrepareSpokenText(%q) = %q (%+q), want %q", tt.in, got, got, tt.want)
			}
		})
	}
}

// Typography that carries meaning when spoken must survive the emoji sweep.
//
// This asserts the EXACT output, not merely that each rune appears somewhere.
// An earlier version checked only for presence and was green while currency was
// being severed from its amount — a test that cannot fail for the defect it
// exists to catch is worse than no test, because it reads like coverage.
func TestPrepareSpokenTextKeepsMeaningfulSymbols(t *testing.T) {
	tests := []struct{ in, want string }{
		{"It cost €20 or $30", "It cost €20 or $30"},
		{"She said “hello” and left", "She said “hello” and left"},
		{"Wait — actually, no", "Wait — actually, no"},
		{"To be continued…", "To be continued…"},
		{"That's a 50% gain", "That's a 50% gain"},
		{"Temperature was 20°C", "Temperature was 20°C"},
		{"Ratio of 3:1 or better", "Ratio of 3:1 or better"},
		// Carve-outs: deleting these changes what the sentence says rather than
		// how it is decorated. Without them "3 ✕ 4" becomes "3 4".
		{"3 ✕ 4", "3 ✕ 4"},
		{"Play C♯ then B♭", "Play C♯ then B♭"},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := PrepareSpokenText(tt.in); got != tt.want {
				t.Errorf("PrepareSpokenText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// Accepted collateral of the U+2600-27BF sweep, pinned so it stays a recorded
// decision rather than becoming a surprise. These are decoration: removing them
// costs nothing a listener would notice.
func TestPrepareSpokenTextAcceptedEmojiCollateral(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Step ✓ done", "Step done"},
		{"Card ♠ suit", "Card suit"},
		{"Note ♪ here", "Note here"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := PrepareSpokenText(tt.in); got != tt.want {
				t.Errorf("PrepareSpokenText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// Whitespace repair runs only on text that actually lost an emoji. Applied
// unconditionally it rewrites prose it has no business touching — "5 . 5" would
// become "5. 5", manufacturing a sentence boundary out of stray spacing.
func TestPrepareSpokenTextLeavesUntouchedTextExactlyAlone(t *testing.T) {
	untouched := []string{
		"5 . 5",
		"Double  spaced  prose stays as written",
		"  leading and trailing spaces  ",
		"line one\nline two",
		"Spaced , comma",
	}
	for _, in := range untouched {
		t.Run(in, func(t *testing.T) {
			if got := PrepareSpokenText(in); got != in {
				t.Errorf("PrepareSpokenText(%q) = %q, want it unchanged", in, got)
			}
		})
	}
}

// The transform must be idempotent. A second call site would be an easy and
// invisible mistake; a transform that mangles its own output would turn that
// mistake into garbled speech instead of a no-op.
func TestPrepareSpokenTextIsIdempotent(t *testing.T) {
	inputs := []string{
		"The answer is 42, roughly 3.14 times 13. 🎉",
		"Back in 1999 we shipped v2 on the 3rd",
		"Nice 👍 work — 1,000 users at 99.9% uptime",
		"Zero 0 and 0.5 and 007 and 12345",
		"From 🏴󠁧󠁢󠁳󠁣󠁴󠁿 to 🇺🇸 in 2026",
		"",
	}
	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			once := PrepareSpokenText(in)
			twice := PrepareSpokenText(once)
			if once != twice {
				t.Errorf("not idempotent:\n  once:  %q\n  twice: %q", once, twice)
			}
		})
	}
}

// Kokoro's transform must include the shared rules rather than reimplementing
// them.
func TestPrepareKokoroTextIncludesSpokenRules(t *testing.T) {
	got := PrepareKokoroText("Back in 1999 🎉")
	want := "Back in nineteen ninety-nine"
	if got != want {
		t.Errorf("PrepareKokoroText = %q, want %q", got, want)
	}
}

// Cleaning can empty a non-empty request. Callers must handle that rather than
// hand "" to a synthesiser, so the behaviour is pinned here.
func TestPrepareKokoroTextCanEmptyANonEmptyRequest(t *testing.T) {
	if got := PrepareKokoroText("👍"); got != "" {
		t.Errorf("PrepareKokoroText(%q) = %q, want empty", "👍", got)
	}
}
