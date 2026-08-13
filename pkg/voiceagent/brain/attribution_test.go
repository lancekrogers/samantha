package brain

import "testing"

type testNames map[string]string

func (n testNames) Display(id string) string {
	if n == nil {
		return id
	}
	if d, ok := n[id]; ok {
		return d
	}
	return id
}

func TestUserSpeakerLabel(t *testing.T) {
	if got := UserSpeakerLabel("", nil); got != "User" {
		t.Fatalf("empty = %q", got)
	}
	if got := UserSpeakerLabel("speaker-1", nil); got != "speaker-1" {
		t.Fatalf("id only = %q", got)
	}
	if got := UserSpeakerLabel("speaker-1", testNames{"speaker-1": "Lance"}); got != "Lance" {
		t.Fatalf("renamed = %q", got)
	}
}

func TestPromptUserLine(t *testing.T) {
	line := promptUserLine(Turn{Role: "user", Content: "hi", Speaker: "speaker-1"}, "Sam", testNames{"speaker-1": "Lance"})
	if line != "Lance: hi" {
		t.Fatalf("user line = %q", line)
	}
	line = promptUserLine(Turn{Role: "samantha", Content: "hello"}, "Sam", nil)
	if line != "Sam: hello" {
		t.Fatalf("agent line = %q", line)
	}
}
