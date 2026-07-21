package netapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/events"
)

func TestIntentPOSTCreatesFile(t *testing.T) {
	dir := t.TempDir()
	creds, err := LoadOrCreateCredentials(dir)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	disp := NewDispatcher(&scriptedRunner{}, bus, nil, nil)
	go disp.Run(context.Background())
	srv := New(Options{
		Bind:        "127.0.0.1:0",
		Credentials: creds,
		Bus:         bus,
		Dispatcher:  disp,
		IntentSink:  IntentSinkConfig{Dir: filepath.Join(dir, "intents")},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.ListenAndServe(ctx) }()
	addr := waitAddr(t, srv)
	client := insecureHTTPClient()

	body := `{"type":"note","body":"fix the pairing flow","source":"ios","captured_at":"2026-07-20T00:00:00Z"}`
	req, _ := http.NewRequest(http.MethodPost, "https://"+addr+"/v1/intent", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+creds.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, b)
	}
	var out struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.ID == "" || out.Path == "" {
		t.Fatalf("out=%+v", out)
	}
	if _, err := os.Stat(out.Path); err != nil {
		t.Fatal(err)
	}

	// targets
	req, _ = http.NewRequest(http.MethodGet, "https://"+addr+"/v1/intent/targets", nil)
	req.Header.Set("Authorization", "Bearer "+creds.Token)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("targets status=%d", resp.StatusCode)
	}
}
