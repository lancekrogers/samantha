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

// intentHarness spins up a serve with only the intent surface configured.
func intentHarness(t *testing.T) (addr, token, sinkDir string) {
	t.Helper()
	dir := t.TempDir()
	creds, err := LoadOrCreateCredentials(dir)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	disp := NewDispatcher(&scriptedRunner{}, bus, nil, nil)
	go disp.Run(context.Background())
	sinkDir = filepath.Join(dir, "intents")
	srv := New(Options{
		Bind:        "127.0.0.1:0",
		Credentials: creds,
		Bus:         bus,
		Dispatcher:  disp,
		IntentSink:  IntentSinkConfig{Dir: sinkDir},
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.ListenAndServe(ctx) }()
	return waitAddr(t, srv), creds.Token, sinkDir
}

func postIntent(t *testing.T, addr, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "https://"+addr+"/v1/intent", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := insecureHTTPClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// Mid-meeting ideas carry their moment; the filed JSON must link back to the
// meeting so the idea can point into the transcript.
func TestIntentContextIsPersisted(t *testing.T) {
	addr, token, _ := intentHarness(t)
	resp := postIntent(t, addr, token,
		`{"type":"bug","body":"pairing dies on rotate","source":"ios",
		  "context":{"meeting_id":"m-42","offset_ms":93500}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, raw)
	}
	var out struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out.Path)
	if err != nil {
		t.Fatal(err)
	}
	var filed IntentRequest
	if err := json.Unmarshal(raw, &filed); err != nil {
		t.Fatal(err)
	}
	if filed.Context == nil || filed.Context.MeetingID != "m-42" || filed.Context.OffsetMs != 93500 {
		t.Fatalf("filed context = %+v, want m-42 @ 93500ms", filed.Context)
	}
}

// D3 regression: a contextless capture files JSON with no context key at all —
// existing consumers of the intent files see exactly what they saw before.
func TestIntentWithoutContextIsWireIdentical(t *testing.T) {
	addr, token, _ := intentHarness(t)
	resp := postIntent(t, addr, token,
		`{"type":"note","body":"plain capture","source":"ios","captured_at":"2026-08-05T00:00:00Z"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "context") {
		t.Fatalf("contextless intent file mentions context:\n%s", raw)
	}
}

func TestIntentContextValidation(t *testing.T) {
	addr, token, _ := intentHarness(t)
	tests := []struct {
		name string
		body string
	}{
		{"empty meeting_id", `{"body":"x","context":{"meeting_id":"  ","offset_ms":10}}`},
		{"negative offset", `{"body":"x","context":{"meeting_id":"m-1","offset_ms":-5}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := postIntent(t, addr, token, tt.body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
		})
	}
}
