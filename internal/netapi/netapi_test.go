package netapi

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/events"
)

// --- auth ---

func TestCredentialsGeneratedOnceAndReloaded(t *testing.T) {
	dir := t.TempDir()

	first, err := LoadOrCreateCredentials(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !first.TokenCreated {
		t.Fatal("first load must report token creation")
	}
	if len(first.Token) != 64 {
		t.Fatalf("token length = %d, want 64 hex chars", len(first.Token))
	}

	second, err := LoadOrCreateCredentials(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second.TokenCreated {
		t.Fatal("second load must not regenerate the token")
	}
	if second.Token != first.Token || second.Fingerprint != first.Fingerprint {
		t.Fatal("credentials changed across loads")
	}
}

func TestVerifyRequest(t *testing.T) {
	creds := &Credentials{Token: "secret-token"}

	req := func(header string) *http.Request {
		r, _ := http.NewRequest(http.MethodGet, "/v1/status", nil)
		if header != "" {
			r.Header.Set("Authorization", header)
		}
		return r
	}

	if !creds.VerifyRequest(req("Bearer secret-token")) {
		t.Error("valid token rejected")
	}
	for _, bad := range []string{"", "Bearer wrong", "secret-token", "Basic secret-token"} {
		if creds.VerifyRequest(req(bad)) {
			t.Errorf("accepted invalid Authorization %q", bad)
		}
	}
}

// --- envelope ---

func TestEncodeEventEnvelopes(t *testing.T) {
	tests := []struct {
		event events.Event
		want  map[string]any
	}{
		{
			events.ResponseReady{Response: "hi", Interrupted: true},
			map[string]any{"type": "response_ready", "response": "hi", "interrupted": true},
		},
		{
			events.ThinkingComplete{Response: "r", Elapsed: 1500 * time.Millisecond},
			map[string]any{"type": "thinking_complete", "response": "r", "elapsed_ms": float64(1500)},
		},
		{
			events.ConversationCleared{},
			map[string]any{"type": "conversation_cleared"},
		},
		{
			events.Error{Stage: "turn", Message: "boom"},
			map[string]any{"type": "error", "stage": "turn", "message": "boom"},
		},
	}

	for _, tt := range tests {
		data, err := marshalEvent(tt.event)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != len(tt.want) {
			t.Errorf("%T: envelope = %v, want %v", tt.event, got, tt.want)
			continue
		}
		for k, v := range tt.want {
			if got[k] != v {
				t.Errorf("%T: field %q = %v, want %v", tt.event, k, got[k], v)
			}
		}
	}
}

func TestTurnMetricsEncodeAsMilliseconds(t *testing.T) {
	data, err := marshalEvent(events.TurnMetrics{ModelCompleteElapsed: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["model_complete_ms"] != float64(2000) {
		t.Fatalf("model_complete_ms = %v, want 2000 (nanosecond leak?)", got["model_complete_ms"])
	}
}

// --- dispatcher ---

type scriptedRunner struct {
	mu     sync.Mutex
	inputs []string
	block  bool // park until ctx cancellation
	runs   chan struct{}
}

func (r *scriptedRunner) RunTurnTextMode(ctx context.Context, input string) error {
	r.mu.Lock()
	r.inputs = append(r.inputs, input)
	r.mu.Unlock()
	if r.runs != nil {
		r.runs <- struct{}{}
	}
	if r.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func (r *scriptedRunner) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.inputs...)
}

func TestDispatcherRunsTextTurnsInOrder(t *testing.T) {
	runner := &scriptedRunner{runs: make(chan struct{}, 4)}
	d := NewDispatcher(runner, events.NewBus(), nil, nil)
	go d.Run(t.Context())

	for _, text := range []string{"one", "two", "three"} {
		if err := d.SubmitText(text); err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		select {
		case <-runner.runs:
		case <-time.After(2 * time.Second):
			t.Fatal("turn never ran")
		}
	}
	got := runner.got()
	if len(got) != 3 || got[0] != "one" || got[1] != "two" || got[2] != "three" {
		t.Fatalf("turns ran as %v, want [one two three]", got)
	}
}

func TestDispatcherInterruptCancelsInFlightTurn(t *testing.T) {
	runner := &scriptedRunner{block: true, runs: make(chan struct{}, 1)}
	d := NewDispatcher(runner, events.NewBus(), nil, nil)
	go d.Run(t.Context())

	if err := d.SubmitText("park me"); err != nil {
		t.Fatal(err)
	}
	<-runner.runs
	if !d.TurnActive() {
		t.Fatal("turn not reported active")
	}

	d.Interrupt()
	deadline := time.After(2 * time.Second)
	for d.TurnActive() {
		select {
		case <-deadline:
			t.Fatal("interrupt did not end the in-flight turn")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestDispatcherClearEmitsEvent(t *testing.T) {
	bus := events.NewBus()
	cleared := make(chan struct{}, 1)
	events.Subscribe(bus, func(events.ConversationCleared) { cleared <- struct{}{} })

	wiped := false
	d := NewDispatcher(&scriptedRunner{}, bus, func() { wiped = true }, nil)
	go d.Run(t.Context())

	if err := d.ClearHistory(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cleared:
	case <-time.After(2 * time.Second):
		t.Fatal("ConversationCleared never emitted")
	}
	if !wiped {
		t.Fatal("clearHistory callback not invoked")
	}
}

func TestDispatcherQueueFullReturnsBusy(t *testing.T) {
	d := NewDispatcher(&scriptedRunner{}, events.NewBus(), nil, nil)
	// Not running: fill the queue to capacity.
	for range dispatchQueueDepth {
		if err := d.SubmitText("x"); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.SubmitText("overflow"); !errors.Is(err, ErrBusy) {
		t.Fatalf("overflow error = %v, want ErrBusy", err)
	}
}

// --- bind validation and rate limiting ---

func TestValidateBind(t *testing.T) {
	tests := []struct {
		bind        string
		allowPublic bool
		wantErr     bool
	}{
		{"127.0.0.1:0", false, false},
		{"192.168.1.10:7262", false, false},
		{"10.0.0.5:7262", false, false},
		{"100.64.1.2:7262", false, false}, // Tailscale / CGNAT
		{"100.127.0.1:7262", false, false},
		{"0.0.0.0:7262", false, true},
		{"[::]:7262", false, true},
		{"8.8.8.8:7262", false, true},
		{"8.8.8.8:7262", true, false},
		{"myhost.example:7262", false, true},
	}
	for _, tt := range tests {
		err := validateBind(tt.bind, tt.allowPublic)
		if (err != nil) != tt.wantErr {
			t.Errorf("validateBind(%q, %v) error = %v, wantErr %v", tt.bind, tt.allowPublic, err, tt.wantErr)
		}
	}
}

func TestRateLimiterCapsPerWindow(t *testing.T) {
	l := newRateLimiter(3, time.Minute)
	now := time.Now()
	for i := range 3 {
		if !l.allow("10.0.0.1:5555", now) {
			t.Fatalf("request %d under the cap was denied", i+1)
		}
	}
	if l.allow("10.0.0.1:5555", now) {
		t.Fatal("request over the cap was allowed")
	}
	if !l.allow("10.0.0.2:5555", now) {
		t.Fatal("distinct client blocked by another client's usage")
	}
	if !l.allow("10.0.0.1:5555", now.Add(2*time.Minute)) {
		t.Fatal("new window did not reset the cap")
	}
}

// --- hub ---

func TestHubEvictsSlowClients(t *testing.T) {
	h := newHub()
	slow := &streamConn{out: make(chan []byte, 1), kick: make(chan struct{})}
	h.add(slow)

	h.broadcast([]byte("one")) // fills the queue
	h.broadcast([]byte("two")) // overflows: evict

	select {
	case <-slow.kick:
	case <-time.After(time.Second):
		t.Fatal("slow client not evicted")
	}

	// Eviction reclaims the hub slot immediately.
	h.mu.Lock()
	n := len(h.conns)
	h.mu.Unlock()
	if n != 0 {
		t.Fatalf("hub still holds %d conns after evict, want 0", n)
	}
}

// A resume whose waiter canceled before apply must not run the resume callback.
func TestResumeSkippedAfterWaiterCancel(t *testing.T) {
	resumed := make(chan string, 1)
	// Park the first turn so resume sits behind it in the queue.
	runner := &scriptedRunner{block: true, runs: make(chan struct{}, 1)}
	d := NewDispatcher(runner, events.NewBus(), nil, func(id string) error {
		resumed <- id
		return nil
	})
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	go d.Run(runCtx)

	if err := d.SubmitText("hold the queue"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.runs:
	case <-time.After(time.Second):
		t.Fatal("blocking turn never started")
	}

	waitCtx, waitCancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- d.ResumeSession(waitCtx, "sess-1") }()

	// Cancel the waiter while resume is still queued behind the blocked turn.
	time.Sleep(20 * time.Millisecond)
	waitCancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ResumeSession error = %v, want canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ResumeSession did not return after cancel")
	}

	// Unblock the turn so the resume op reaches apply; it must be skipped.
	d.Interrupt()
	time.Sleep(50 * time.Millisecond)
	select {
	case id := <-resumed:
		t.Fatalf("resume callback ran for %q after waiter canceled", id)
	default:
	}
}

// --- end-to-end over TLS ---

func startTestServer(t *testing.T, runner TurnRunner, bus *events.Bus) (*Server, string, *Credentials) {
	t.Helper()
	creds, err := LoadOrCreateCredentials(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	d := NewDispatcher(runner, bus, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go d.Run(ctx)

	s := New(Options{
		Bind:        "127.0.0.1:0",
		Credentials: creds,
		Bus:         bus,
		Dispatcher:  d,
		Providers:   Providers{Brain: "claude", TTS: "kokoro"},
		ListSessions: func() []SessionSummary {
			return []SessionSummary{{ID: "abc123", Summary: "test session", Turns: 4}}
		},
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
			t.Fatal("server never bound")
		case <-time.After(5 * time.Millisecond):
		}
	}
	return s, s.Addr().String(), creds
}

func insecureClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
}

func TestServerRejectsMissingToken(t *testing.T) {
	_, addr, _ := startTestServer(t, &scriptedRunner{}, events.NewBus())

	resp, err := insecureClient().Get("https://" + addr + "/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status without token = %d, want 401", resp.StatusCode)
	}
}

func TestServerStatusAndSessions(t *testing.T) {
	_, addr, creds := startTestServer(t, &scriptedRunner{}, events.NewBus())
	client := insecureClient()

	get := func(path string) map[string]any {
		req, _ := http.NewRequest(http.MethodGet, "https://"+addr+path, nil)
		req.Header.Set("Authorization", "Bearer "+creds.Token)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, resp.StatusCode)
		}
		var v any
		if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
			t.Fatal(err)
		}
		if m, ok := v.(map[string]any); ok {
			return m
		}
		return map[string]any{"list": v}
	}

	status := get("/v1/status")
	if status["turn_active"] != false {
		t.Errorf("turn_active = %v, want false", status["turn_active"])
	}
	providers := status["providers"].(map[string]any)
	if providers["brain"] != "claude" {
		t.Errorf("brain provider = %v, want claude", providers["brain"])
	}

	sessions := get("/v1/sessions")["list"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("sessions = %v, want one entry", sessions)
	}
	if sessions[0].(map[string]any)["id"] != "abc123" {
		t.Errorf("session id = %v, want abc123", sessions[0])
	}
}

func TestServerStreamEndToEnd(t *testing.T) {
	bus := events.NewBus()
	runner := &scriptedRunner{runs: make(chan struct{}, 1)}
	_, addr, creds := startTestServer(t, runner, bus)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	header := http.Header{}
	header.Set("Authorization", "Bearer "+creds.Token)
	ws, _, err := websocket.Dial(ctx, "wss://"+addr+"/v1/stream", &websocket.DialOptions{
		HTTPClient: insecureClient(),
		HTTPHeader: header,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close(websocket.StatusNormalClosure, "")

	// Client -> server: a text turn reaches the pipeline.
	msg, _ := json.Marshal(controlMessage{Type: "text_input", Text: "hello over the wire"})
	if err := ws.Write(ctx, websocket.MessageText, msg); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.runs:
	case <-ctx.Done():
		t.Fatal("text_input never reached the turn runner")
	}
	if got := runner.got(); got[0] != "hello over the wire" {
		t.Fatalf("turn input = %q", got[0])
	}

	// Server -> client: bus events stream as envelopes.
	bus.Emit(events.ResponseReady{Response: "hi from samantha"})
	_, data, err := ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	if env["type"] != "response_ready" || env["response"] != "hi from samantha" {
		t.Fatalf("streamed envelope = %v", env)
	}
}

func TestServerStreamRejectsWithoutToken(t *testing.T) {
	_, addr, _ := startTestServer(t, &scriptedRunner{}, events.NewBus())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, "wss://"+addr+"/v1/stream", &websocket.DialOptions{
		HTTPClient: insecureClient(),
	})
	if err == nil {
		t.Fatal("unauthenticated WebSocket dial succeeded")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("dial response = %v, want 401", resp)
	}
}

func TestServerRefusesPublicBind(t *testing.T) {
	creds, err := LoadOrCreateCredentials(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := New(Options{
		Bind:        "0.0.0.0:0",
		Credentials: creds,
		Bus:         events.NewBus(),
		Dispatcher:  NewDispatcher(&scriptedRunner{}, events.NewBus(), nil, nil),
	})
	if err := s.ListenAndServe(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "refusing to bind") {
		t.Fatalf("public bind error = %v, want refusal", err)
	}
}

func TestResumeEndpoint(t *testing.T) {
	bus := events.NewBus()
	resumed := ""
	d := NewDispatcher(&scriptedRunner{}, bus, nil, func(id string) error {
		if id == "missing" {
			return fmt.Errorf("session %s not found", id)
		}
		resumed = id
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go d.Run(ctx)

	creds, err := LoadOrCreateCredentials(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := New(Options{Bind: "127.0.0.1:0", Credentials: creds, Bus: bus, Dispatcher: d})
	go func() { _ = s.ListenAndServe(ctx) }()
	deadline := time.After(2 * time.Second)
	for s.Addr() == nil {
		select {
		case <-deadline:
			t.Fatal("server never bound")
		case <-time.After(5 * time.Millisecond):
		}
	}
	addr := s.Addr().String()
	client := insecureClient()

	post := func(path string) int {
		req, _ := http.NewRequest(http.MethodPost, "https://"+addr+path, nil)
		req.Header.Set("Authorization", "Bearer "+creds.Token)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if code := post("/v1/sessions/abc123/resume"); code != http.StatusOK {
		t.Fatalf("resume = %d, want 200", code)
	}
	if resumed != "abc123" {
		t.Fatalf("resumed id = %q", resumed)
	}
	if code := post("/v1/sessions/missing/resume"); code != http.StatusUnprocessableEntity {
		t.Fatalf("missing resume = %d, want 422", code)
	}
}

// --- Phase 3 audio stream ---

// drainEngine consumes a PCM stream without hardware — local-mute path.
type drainEngine struct {
	streams atomic.Int32
}

func (d *drainEngine) PlayStream(ctx context.Context, stream *audio.PCMStream) (*audio.Playback, error) {
	d.streams.Add(1)
	if _, err := stream.WaitReady(ctx); err != nil {
		return nil, err
	}
	started := make(chan struct{})
	done := make(chan audio.PlaybackResult, 1)
	go func() {
		first := true
		for {
			select {
			case <-ctx.Done():
				if first {
					close(started)
				}
				done <- audio.PlaybackResult{Interrupted: true, Err: ctx.Err()}
				close(done)
				return
			case frames, ok := <-stream.Frames():
				if !ok {
					if first {
						close(started)
					}
					done <- audio.PlaybackResult{Err: stream.Err()}
					close(done)
					return
				}
				if first && len(frames) > 0 {
					first = false
					close(started)
				}
			}
		}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-started:
	}
	return audio.NewPlayback(started, done), nil
}

func (d *drainEngine) Stop()           {}
func (d *drainEngine) IsPlaying() bool { return false }
func (d *drainEngine) Close() error    { return nil }

func TestAudioFanoutStreamsWithoutLocalSpeaker(t *testing.T) {
	h := newHub()
	conn := &streamConn{out: make(chan []byte, 16), kick: make(chan struct{})}
	conn.setAudioStream(true)
	h.add(conn)

	fanout := NewAudioFanout(nil)
	fanout.AttachHub(h)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	stream := audio.NewPCMStream(ctx)
	if err := stream.SetSampleRate(24000); err != nil {
		t.Fatal(err)
	}

	errCh := make(chan error, 1)
	go func() {
		playback, err := fanout.PlayStream(ctx, stream)
		if err != nil {
			errCh <- err
			return
		}
		<-playback.Done()
		errCh <- nil
	}()

	// Give PlayStream a moment to enter WaitReady (already satisfied) and pump.
	time.Sleep(10 * time.Millisecond)
	if err := stream.Write([]float32{0.5, -0.5, 0.25}); err != nil {
		t.Fatal(err)
	}
	stream.Close()

	if err := <-errCh; err != nil {
		t.Fatalf("PlayStream: %v", err)
	}

	var sawChunk, sawEnd bool
	deadline := time.After(2 * time.Second)
	for !sawChunk || !sawEnd {
		select {
		case msg := <-conn.out:
			var env map[string]any
			if err := json.Unmarshal(msg, &env); err != nil {
				t.Fatal(err)
			}
			switch env["type"] {
			case "audio_chunk":
				sawChunk = true
				if env["format"] != audioWireFormat {
					t.Fatalf("format = %v", env["format"])
				}
				if env["sample_rate"] != float64(24000) {
					t.Fatalf("sample_rate = %v", env["sample_rate"])
				}
				data, _ := env["data"].(string)
				raw, err := base64.StdEncoding.DecodeString(data)
				if err != nil || len(raw) != 6 {
					t.Fatalf("pcm payload = %d bytes err=%v, want 6", len(raw), err)
				}
			case "audio_end":
				sawEnd = true
				if env["reason"] != "complete" {
					t.Fatalf("audio_end reason = %v", env["reason"])
				}
			}
		case <-deadline:
			t.Fatalf("chunk=%v end=%v — timed out waiting for audio envelopes", sawChunk, sawEnd)
		}
	}
}

func TestAudioFanoutWithLocalEngine(t *testing.T) {
	local := &drainEngine{}
	fanout := NewAudioFanout(local)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	stream := audio.NewPCMStream(ctx)
	_ = stream.SetSampleRate(16000)

	go func() {
		time.Sleep(5 * time.Millisecond)
		_ = stream.Write([]float32{0.1, 0.2, 0.3, 0.4})
		stream.Close()
	}()

	playback, err := fanout.PlayStream(ctx, stream)
	if err != nil {
		t.Fatal(err)
	}
	result := <-playback.Done()
	if result.Err != nil {
		t.Fatalf("playback result: %v", result.Err)
	}
	if local.streams.Load() != 1 {
		t.Fatalf("local PlayStream calls = %d, want 1", local.streams.Load())
	}
}

func TestAudioOutputPreferenceGatesChunks(t *testing.T) {
	bus := events.NewBus()
	creds, err := LoadOrCreateCredentials(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fanout := NewAudioFanout(nil)
	d := NewDispatcher(&scriptedRunner{runs: make(chan struct{}, 1)}, bus, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go d.Run(ctx)

	s := New(Options{
		Bind:        "127.0.0.1:0",
		Credentials: creds,
		Bus:         bus,
		Dispatcher:  d,
		Audio:       fanout,
	})
	go func() { _ = s.ListenAndServe(ctx) }()
	deadline := time.After(2 * time.Second)
	for s.Addr() == nil {
		select {
		case <-deadline:
			t.Fatal("server never bound")
		case <-time.After(5 * time.Millisecond):
		}
	}
	addr := s.Addr().String()

	dial := func() *websocket.Conn {
		t.Helper()
		header := http.Header{}
		header.Set("Authorization", "Bearer "+creds.Token)
		ws, _, err := websocket.Dial(ctx, "wss://"+addr+"/v1/stream", &websocket.DialOptions{
			HTTPClient: insecureClient(),
			HTTPHeader: header,
		})
		if err != nil {
			t.Fatal(err)
		}
		return ws
	}

	wsStream := dial()
	defer wsStream.Close(websocket.StatusNormalClosure, "")
	wsQuiet := dial()
	defer wsQuiet.Close(websocket.StatusNormalClosure, "")

	// Only one client opts into audio.
	optIn, _ := json.Marshal(controlMessage{Type: "audio_output", Mode: "stream"})
	if err := wsStream.Write(ctx, websocket.MessageText, optIn); err != nil {
		t.Fatal(err)
	}
	// Give the server a beat to apply the preference.
	time.Sleep(20 * time.Millisecond)

	// Push a synthetic TTS stream through the fanout.
	pcmStream := audio.NewPCMStream(ctx)
	_ = pcmStream.SetSampleRate(24000)
	go func() {
		time.Sleep(5 * time.Millisecond)
		_ = pcmStream.Write([]float32{0.9, -0.9})
		pcmStream.Close()
	}()
	if _, err := fanout.PlayStream(ctx, pcmStream); err != nil {
		t.Fatal(err)
	}

	// Stream client must see audio_chunk; quiet client must only see (or nothing).
	readType := func(ws *websocket.Conn, timeout time.Duration) (string, bool) {
		rctx, rcancel := context.WithTimeout(ctx, timeout)
		defer rcancel()
		_, data, err := ws.Read(rctx)
		if err != nil {
			return "", false
		}
		var env map[string]any
		_ = json.Unmarshal(data, &env)
		typ, _ := env["type"].(string)
		return typ, true
	}

	typ, ok := readType(wsStream, 2*time.Second)
	if !ok || typ != "audio_chunk" {
		t.Fatalf("stream client got type %q ok=%v, want audio_chunk", typ, ok)
	}
	// Quiet client should not receive audio within a short window.
	if typ, ok := readType(wsQuiet, 150*time.Millisecond); ok {
		t.Fatalf("quiet client unexpectedly received %q", typ)
	}
}

func TestHubBroadcastAudioSkipsOptOut(t *testing.T) {
	h := newHub()
	on := &streamConn{out: make(chan []byte, 2), kick: make(chan struct{})}
	off := &streamConn{out: make(chan []byte, 2), kick: make(chan struct{})}
	on.setAudioStream(true)
	h.add(on)
	h.add(off)

	h.broadcastAudio([]byte(`{"type":"audio_chunk"}`))
	h.broadcast([]byte(`{"type":"info"}`))

	select {
	case <-on.out:
	default:
		t.Fatal("opt-in client missed audio")
	}
	select {
	case msg := <-off.out:
		if string(msg) != `{"type":"info"}` {
			t.Fatalf("opt-out client got %s, want only the event broadcast", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("opt-out client missed event broadcast")
	}
	// Ensure no audio leaked to opt-out.
	select {
	case msg := <-off.out:
		t.Fatalf("opt-out client has extra message: %s", msg)
	default:
	}
}
