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

	"github.com/lancekrogers/samantha/pkg/voiceagent/events"
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

// Create-if-absent is the durable idempotency receipt for deterministic keys.
func TestWriteIntentFileWithIDIsCreateIfAbsent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "intents")
	first := IntentRequest{Type: "note", Body: "original", Source: "meeting", CapturedAt: "2026-08-06T00:00:00Z"}
	path, created, err := WriteIntentFileWithID(dir, "meeting-m1-span-a", first)
	if err != nil || !created {
		t.Fatalf("first write = created %v, err %v", created, err)
	}
	second := first
	second.Body = "attempted overwrite"
	path2, created, err := WriteIntentFileWithID(dir, "meeting-m1-span-a", second)
	if err != nil || created {
		t.Fatalf("second write = created %v, err %v; want existing-file success", created, err)
	}
	if path2 != path {
		t.Fatalf("paths differ: %s vs %s", path, path2)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "original") || strings.Contains(string(raw), "overwrite") {
		t.Fatalf("first write must win:\n%s", raw)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("files = %d, want 1", len(entries))
	}
}

func TestWriteIntentFileWithIDRecoversPartialExistingReceipt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "intents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "meeting-m1-span-a.json")
	if err := os.WriteFile(path, []byte(`{"type":"note","body":`), 0o600); err != nil {
		t.Fatal(err)
	}

	want := IntentRequest{
		Type: "note", Body: "recovered idea", Source: "meeting",
		CapturedAt: "2026-08-06T00:00:00Z",
	}
	gotPath, created, err := WriteIntentFileWithID(dir, "meeting-m1-span-a", want)
	if err != nil || !created {
		t.Fatalf("recovery write = created %v, err %v", created, err)
	}
	if gotPath != path {
		t.Fatalf("path = %s, want %s", gotPath, path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got IntentRequest
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("recovered receipt is invalid JSON: %v\n%s", err, raw)
	}
	if got.Body != want.Body {
		t.Fatalf("recovered body = %q, want %q", got.Body, want.Body)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Fatalf("recovery left staged files behind: %v", entries)
	}
}
