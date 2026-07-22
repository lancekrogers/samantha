package tts

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestManagedQwenSessionHandshakeAndSynthesis(t *testing.T) {
	script := filepath.Join(t.TempDir(), "fake-worker.sh")
	source := `#!/bin/sh
echo '{"protocol":"samantha-qwen/v1","type":"ready","voices":["Vivian"]}'
while IFS= read -r request; do
  case "$request" in
    *'"type":"shutdown"'*) exit 0 ;;
    *) echo '{"protocol":"samantha-qwen/v1","type":"complete","request_id":"qwen-1","sample_rate":24000}' ;;
  esac
done
`
	if err := os.WriteFile(script, []byte(source), 0o700); err != nil {
		t.Fatal(err)
	}
	session, err := startManagedQwenSession("sh", script, "/models/qwen", time.Second)
	if err != nil {
		t.Fatalf("startManagedQwenSession() error = %v", err)
	}
	defer session.Close()
	if err := session.Synthesize(context.Background(), SynthesisRequest{
		Text: "hello", Mode: VoiceModeCustomVoice, Voice: "Vivian", Language: "English",
	}, filepath.Join(t.TempDir(), "speech.wav")); err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
}

func TestManagedQwenSessionRejectsBadHandshake(t *testing.T) {
	script := filepath.Join(t.TempDir(), "bad-worker.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho nope\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := startManagedQwenSession("sh", script, "/models/qwen", time.Second); err == nil {
		t.Fatal("bad managed worker handshake was accepted")
	}
}
