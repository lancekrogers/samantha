package netapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/pkg/voiceagent/events"
)

// startPersonaServer boots a server whose only extra wiring is the persona
// resolver, so a nil one can be tested as its own case.
func startPersonaServer(t *testing.T, list func() ([]PersonaSummary, error)) (string, *Credentials) {
	t.Helper()
	creds, err := LoadOrCreateCredentials(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	d := NewDispatcher(&scriptedRunner{}, bus, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go d.Run(ctx)

	s := New(Options{
		Bind:         "127.0.0.1:0",
		Credentials:  creds,
		Bus:          bus,
		Dispatcher:   d,
		ListPersonas: list,
	})
	go func() {
		if err := s.ListenAndServe(ctx); err != nil {
			t.Errorf("ListenAndServe: %v", err)
		}
	}()
	deadline := time.After(2 * time.Second)
	for s.Addr() == nil {
		select {
		case <-deadline:
			cancel()
			t.Fatal("server never bound")
		case <-time.After(5 * time.Millisecond):
		}
	}
	return s.Addr().String(), creds
}

func getPersonas(t *testing.T, addr, token string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "https://"+addr+"/v1/personas", nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := insecureClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

func samplePersonas() []PersonaSummary {
	return []PersonaSummary{
		{
			ID: "samantha", DisplayName: "Samantha", Active: false, Builtin: true,
			Brain: PersonaBrain{Provider: "ollama", Model: "qwen2.5:14b"},
			TTS:   PersonaTTS{Provider: "kokoro", Voice: "af_heart"},
		},
		{
			ID: "uncle-fu", DisplayName: "Uncle Fu", Active: true, Builtin: false,
			Brain: PersonaBrain{Provider: "ollama", Model: "qwen2.5:14b"},
			TTS:   PersonaTTS{Provider: "qwen3-tts", Voice: "Uncle_Fu", Tier: "1.7b"},
		},
	}
}

func TestPersonasRouteShape(t *testing.T) {
	addr, creds := startPersonaServer(t, func() ([]PersonaSummary, error) {
		return samplePersonas(), nil
	})

	status, body := getPersonas(t, addr, creds.Token)
	if status != http.StatusOK {
		t.Fatalf("GET /v1/personas = %d, want 200", status)
	}
	rows, ok := body["personas"].([]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("personas = %v, want two rows", body["personas"])
	}
	first := rows[0].(map[string]any)
	for _, key := range []string{"id", "display_name", "active", "builtin", "brain", "tts"} {
		if _, ok := first[key]; !ok {
			t.Errorf("row missing %q (got %v)", key, first)
		}
	}
	if first["builtin"] != true {
		t.Errorf("builtin = %v, want true for samantha", first["builtin"])
	}
	second := rows[1].(map[string]any)
	if second["active"] != true {
		t.Errorf("active = %v, want true for uncle-fu", second["active"])
	}
	tts := second["tts"].(map[string]any)
	if tts["provider"] != "qwen3-tts" || tts["voice"] != "Uncle_Fu" || tts["tier"] != "1.7b" {
		t.Errorf("tts = %v", tts)
	}
	// Tier is a stable key, not an omitted one: a decoder should not have to
	// distinguish absent from empty.
	if got := rows[0].(map[string]any)["tts"].(map[string]any)["tier"]; got != "" {
		t.Errorf("kokoro tier = %v, want an empty string", got)
	}
}

// A serve without the resolver must 404 rather than answer an empty list: a
// client feature-detects the capability by status.
func TestPersonasRouteAbsentWithoutResolver(t *testing.T) {
	addr, creds := startPersonaServer(t, nil)

	if status, _ := getPersonas(t, addr, creds.Token); status != http.StatusNotFound {
		t.Fatalf("GET /v1/personas = %d, want 404 when ListPersonas is nil", status)
	}
}

func TestPersonasRouteRequiresBearer(t *testing.T) {
	addr, _ := startPersonaServer(t, func() ([]PersonaSummary, error) {
		return samplePersonas(), nil
	})

	if status, _ := getPersonas(t, addr, ""); status != http.StatusUnauthorized {
		t.Fatalf("GET /v1/personas without a token = %d, want 401", status)
	}
}

func TestPersonasRouteResolverError(t *testing.T) {
	addr, creds := startPersonaServer(t, func() ([]PersonaSummary, error) {
		return nil, errors.New("personas dir unreadable")
	})

	status, body := getPersonas(t, addr, creds.Token)
	if status != http.StatusInternalServerError {
		t.Fatalf("GET /v1/personas = %d, want 500", status)
	}
	if body["error"] != "personas dir unreadable" {
		t.Fatalf("error = %v, want the resolver's message", body["error"])
	}
}

// A resolver that legitimately finds nothing answers 200 with an empty array,
// never a JSON null a decoder has to special-case.
func TestPersonasRouteEmptyList(t *testing.T) {
	addr, creds := startPersonaServer(t, func() ([]PersonaSummary, error) {
		return nil, nil
	})

	status, body := getPersonas(t, addr, creds.Token)
	if status != http.StatusOK {
		t.Fatalf("GET /v1/personas = %d, want 200", status)
	}
	rows, ok := body["personas"].([]any)
	if !ok || len(rows) != 0 {
		t.Fatalf("personas = %v, want an empty array", body["personas"])
	}
}
