package netapi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
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

	req := func(header, rawURL string) *http.Request {
		if rawURL == "" {
			rawURL = "/v1/status"
		}
		r, _ := http.NewRequest(http.MethodGet, rawURL, nil)
		if header != "" {
			r.Header.Set("Authorization", header)
		}
		return r
	}

	if !creds.VerifyRequest(req("Bearer secret-token", "")) {
		t.Error("valid token rejected")
	}
	if !creds.VerifyRequest(req("", "/v1/stream?token=secret-token")) {
		t.Error("valid query token rejected")
	}
	if creds.VerifyRequest(req("", "/v1/status?token=secret-token")) {
		t.Error("query token accepted outside the WebSocket stream endpoint")
	}
	for _, bad := range []string{"", "Bearer wrong", "secret-token", "Basic secret-token"} {
		if creds.VerifyRequest(req(bad, "")) {
			t.Errorf("accepted invalid Authorization %q", bad)
		}
	}
	if creds.VerifyRequest(req("", "/v1/stream?token=wrong")) {
		t.Error("accepted wrong query token")
	}
}

func TestPairingCodeExchangeAndSingleUse(t *testing.T) {
	creds, err := LoadOrCreateCredentials(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if creds.Pairing == nil || len(creds.Pairing.Code) != 6 {
		t.Fatalf("pairing code = %+v, want 6 digits", creds.Pairing)
	}
	token, err := creds.ExchangePairingCode(creds.Pairing.Code)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if token != creds.Token {
		t.Fatal("exchange returned wrong token")
	}
	if _, err := creds.ExchangePairingCode(creds.Pairing.Code); err == nil {
		t.Fatal("second exchange with same code must fail")
	}
	// Fresh code after refresh works once.
	refreshed, err := creds.RefreshPairingCode()
	if err != nil {
		t.Fatalf("refresh pairing code: %v", err)
	}
	code := refreshed.Code
	if _, err := creds.ExchangePairingCode(code); err != nil {
		t.Fatalf("refresh exchange: %v", err)
	}
	if _, err := creds.ExchangePairingCode("999999"); err == nil {
		t.Fatal("wrong code must fail")
	}
}

func TestPairingCodeExpires(t *testing.T) {
	creds := &Credentials{
		Token: "tok",
		Pairing: &PairingCode{
			Code:      "123456",
			ExpiresAt: time.Now().Add(-time.Second),
		},
	}
	if _, err := creds.ExchangePairingCode("123456"); err == nil {
		t.Fatal("expired pairing code must fail")
	}
}

type entropyFailureReader struct{}

func (entropyFailureReader) Read([]byte) (int, error) {
	return 0, errors.New("entropy unavailable")
}

func TestPairingCodeEntropyFailureFailsClosed(t *testing.T) {
	if _, err := pairingCodeFrom(entropyFailureReader{}, time.Now()); err == nil {
		t.Fatal("pairing code generation succeeded without entropy")
	}
}

func TestPairingCodeConcurrentExchangeIsSingleUse(t *testing.T) {
	creds, err := LoadOrCreateCredentials(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	code := creds.Pairing.Code

	const attempts = 32
	start := make(chan struct{})
	results := make(chan error, attempts)
	var wg sync.WaitGroup
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := creds.ExchangePairingCode(code)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var successes int
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful concurrent exchanges = %d, want exactly 1", successes)
	}
}

func TestRevokeTokensForcesNewToken(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadOrCreateCredentials(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := RevokeTokens(dir); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+first.Token)
	if first.VerifyRequest(req) {
		t.Fatal("revoked credentials still authorize requests")
	}
	if _, err := first.ExchangePairingCode(first.Pairing.Code); err == nil {
		t.Fatal("pairing returned a revoked bearer token")
	}
	second, err := LoadOrCreateCredentials(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !second.TokenCreated {
		t.Fatal("load after revoke must mint a new token")
	}
	if second.Token == first.Token {
		t.Fatal("token unchanged after revoke")
	}
}

func TestRevokeTokensStopsRunningServerAndClosesStream(t *testing.T) {
	dir := t.TempDir()
	creds, err := LoadOrCreateCredentials(dir)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	dispatcher := NewDispatcher(&scriptedRunner{}, bus, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go dispatcher.Run(ctx)

	server := New(Options{
		Bind:        "127.0.0.1:0",
		Credentials: creds,
		Bus:         bus,
		Dispatcher:  dispatcher,
	})
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe(ctx) }()
	deadline := time.After(2 * time.Second)
	for server.Addr() == nil {
		select {
		case <-deadline:
			t.Fatal("server never bound")
		case <-time.After(5 * time.Millisecond):
		}
	}

	readCtx, stopRead := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopRead()
	ws, _, err := websocket.Dial(readCtx, "wss://"+server.Addr().String()+"/v1/stream?token="+creds.Token, &websocket.DialOptions{
		HTTPClient: insecureClient(),
	})
	if err != nil {
		t.Fatalf("dial stream: %v", err)
	}
	defer ws.Close(websocket.StatusNormalClosure, "")

	if err := RevokeTokens(dir); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ws.Read(readCtx); err == nil {
		t.Fatal("stream remained open after token revocation")
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("server exit after revoke: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop after token revocation")
	}
}

func TestPairHTTPEndpoint(t *testing.T) {
	creds, err := LoadOrCreateCredentials(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	code := creds.Pairing.Code
	bus := events.NewBus()
	disp := NewDispatcher(&scriptedRunner{}, bus, nil, nil)
	go disp.Run(context.Background())

	srv := New(Options{
		Bind:        "127.0.0.1:0",
		Credentials: creds,
		Bus:         bus,
		Dispatcher:  disp,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe(ctx) }()

	// Wait for bind.
	var addr string
	for i := 0; i < 50; i++ {
		if a := srv.Addr(); a != nil {
			addr = a.String()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if addr == "" {
		t.Fatal("server never bound")
	}

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	body := strings.NewReader(`{"code":"` + code + `"}`)
	req, _ := http.NewRequest(http.MethodPost, "https://"+addr+"/v1/pair", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("pair status = %d body=%s", resp.StatusCode, b)
	}
	var out struct {
		Token       string `json:"token"`
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Token != creds.Token || out.Fingerprint != creds.Fingerprint {
		t.Fatalf("pair response = %+v", out)
	}

	// Second use of the same code must fail (single-use).
	req2, _ := http.NewRequest(http.MethodPost, "https://"+addr+"/v1/pair", strings.NewReader(`{"code":"`+code+`"}`))
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode == http.StatusOK {
		t.Fatal("reused pairing code accepted")
	}
	cancel()
	<-errCh
}

func TestLoadExternalTLSCertificate(t *testing.T) {
	dir := t.TempDir()
	// Mint a self-signed pair first, then reload it as "external".
	first, err := LoadOrCreateCredentials(dir)
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, certFile)
	keyPath := filepath.Join(dir, keyFile)

	second, err := LoadOrCreateCredentialsWithTLS(t.TempDir(), certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !second.ExternalTLS {
		t.Fatal("expected ExternalTLS")
	}
	if second.Fingerprint != first.Fingerprint {
		t.Fatalf("fingerprint mismatch: %s vs %s", second.Fingerprint, first.Fingerprint)
	}
}

func TestLoadCredentialsWithIdentitySANs(t *testing.T) {
	dir := t.TempDir()
	id := CertIdentity{
		DNSNames: []string{"mac-studio.tail37114b.ts.net"},
		IPs:      []net.IP{net.ParseIP("100.72.165.77")},
	}
	creds, err := LoadOrCreateCredentialsWithIdentity(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if creds.ExternalTLS {
		t.Fatal("self-signed should not be ExternalTLS")
	}
	leaf, err := x509.ParseCertificate(creds.Certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	foundDNS, foundIP := false, false
	for _, name := range leaf.DNSNames {
		if name == "mac-studio.tail37114b.ts.net" {
			foundDNS = true
		}
	}
	for _, ip := range leaf.IPAddresses {
		if ip.Equal(net.ParseIP("100.72.165.77")) {
			foundIP = true
		}
	}
	if !foundDNS || !foundIP {
		t.Fatalf("SANs missing MagicDNS/IP: dns=%v ips=%v", leaf.DNSNames, leaf.IPAddresses)
	}
	// Same identity must not rotate the TOFU fingerprint.
	again, err := LoadOrCreateCredentialsWithIdentity(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if again.Fingerprint != creds.Fingerprint {
		t.Fatal("same-identity reload rewrote existing cert fingerprint")
	}
	// Expanded identity (e.g. LAN cert → MagicDNS) must rewrite so browsers
	// opening the new hostname pass name verification.
	expanded := CertIdentity{
		DNSNames: []string{"mac-studio.tail37114b.ts.net", "other.example"},
		IPs:      []net.IP{net.ParseIP("100.72.165.77")},
	}
	rewritten, err := LoadOrCreateCredentialsWithIdentity(dir, expanded)
	if err != nil {
		t.Fatal(err)
	}
	if rewritten.Fingerprint == creds.Fingerprint {
		t.Fatal("expanded identity should rewrite cert missing new SANs")
	}
	leaf2, err := x509.ParseCertificate(rewritten.Certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if !certSatisfiesIdentity(leaf2, expanded) {
		t.Fatalf("rewritten cert missing expanded SANs: dns=%v ips=%v", leaf2.DNSNames, leaf2.IPAddresses)
	}
}

func TestLoadCredentialsRewritesLANCertForMagicDNS(t *testing.T) {
	// Reproduces the iPhone failure mode: first serve was LAN (localhost SANs
	// only), then --tailscale prints a MagicDNS URL that the old cert does not
	// cover → Safari hostname mismatch.
	dir := t.TempDir()
	lan, err := LoadOrCreateCredentials(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := CertIdentity{
		DNSNames: []string{"mac-studio.tail37114b.ts.net"},
		IPs:      []net.IP{net.ParseIP("100.72.165.77")},
	}
	tail, err := LoadOrCreateCredentialsWithIdentity(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if tail.Fingerprint == lan.Fingerprint {
		t.Fatal("LAN-only cert should be rewritten for MagicDNS identity")
	}
	leaf, err := x509.ParseCertificate(tail.Certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if !certSatisfiesIdentity(leaf, id) {
		t.Fatalf("rewritten SANs = dns=%v ips=%v", leaf.DNSNames, leaf.IPAddresses)
	}
}

func TestVoicePageIsPublic(t *testing.T) {
	_, addr, creds := startTestServer(t, &scriptedRunner{}, events.NewBus())
	client := insecureClient()

	// HTML must load without a token (browser entrypoint).
	resp, err := client.Get("https://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Samantha") {
		t.Fatalf("voice page body missing title, got %q", string(body)[:min(80, len(body))])
	}

	// API still requires auth.
	resp2, err := client.Get("https://" + addr + "/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /v1/status without token = %d, want 401", resp2.StatusCode)
	}

	// Query-token auth is restricted to the browser WebSocket endpoint.
	req, _ := http.NewRequest(http.MethodGet, "https://"+addr+"/v1/status?token="+creds.Token, nil)
	resp3, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /v1/status?token = %d, want 401", resp3.StatusCode)
	}
}

func TestHubAudioResetDropsCanceledTailUntilNextTurn(t *testing.T) {
	h := newHub()
	bus := events.NewBus()
	detach := h.attachBus(bus)
	defer detach()
	c := &streamConn{
		out:   make(chan []byte, 1),
		audio: make(chan []byte, audioQueueDepth),
		kick:  make(chan string, 1),
	}
	if !h.add(c) {
		t.Fatal("failed to add stream connection")
	}
	h.setConnAudio(c, true)

	h.broadcastAudio([]byte(`{"type":"audio_chunk","segment_id":1}`))
	h.resetAudio()
	h.broadcastAudio([]byte(`{"type":"audio_chunk","segment_id":1}`))

	select {
	case got := <-c.audio:
		if string(got) != string(audioResetEnvelope) {
			t.Fatalf("first envelope after reset = %s, want %s", got, audioResetEnvelope)
		}
	default:
		t.Fatal("audio_reset was not queued")
	}
	select {
	case got := <-c.audio:
		t.Fatalf("canceled tail crossed reset boundary: %s", got)
	default:
	}
	late := &streamConn{
		out:   make(chan []byte, 1),
		audio: make(chan []byte, audioQueueDepth),
		kick:  make(chan string, 1),
	}
	if !h.add(late) {
		t.Fatal("failed to add late stream connection")
	}
	h.setConnAudio(late, true)
	h.broadcastAudio([]byte(`{"type":"audio_chunk","segment_id":1}`))
	select {
	case got := <-late.audio:
		t.Fatalf("connection added during reset received canceled audio: %s", got)
	default:
	}

	bus.Emit(events.ThinkingStarted{})
	h.broadcastAudio([]byte(`{"type":"audio_chunk","segment_id":2}`))
	select {
	case got := <-c.audio:
		if !strings.Contains(string(got), `"segment_id":2`) {
			t.Fatalf("next-turn audio = %s, want segment 2", got)
		}
	default:
		t.Fatal("next-turn audio remained blocked")
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

func TestBannerEventsMarshalToSingleLine(t *testing.T) {
	tests := []struct {
		name   string
		banner any
		want   map[string]any
	}{
		{
			name: "ready",
			banner: ReadyBanner{
				Event:           "ready",
				ProtocolVersion: ProtocolVersion,
				URL:             "https://192.168.1.20:7262",
				Port:            7262,
				Fingerprint:     "abc123",
				Token:           "deadbeef",
				MDNS:            true,
				Tailscale:       false,
				PID:             4242,
			},
			want: map[string]any{
				"event":            "ready",
				"protocol_version": float64(1),
				"url":              "https://192.168.1.20:7262",
				"port":             float64(7262),
				"fingerprint":      "abc123",
				"token":            "deadbeef",
				"mdns":             true,
				"tailscale":        false,
				"pid":              float64(4242),
			},
		},
		{
			name: "pairing_code",
			banner: PairingCodeBanner{
				Event:     "pairing_code",
				Code:      "123456",
				ExpiresAt: "2026-07-19T12:00:00Z",
			},
			want: map[string]any{
				"event":      "pairing_code",
				"code":       "123456",
				"expires_at": "2026-07-19T12:00:00Z",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.banner)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "\n") {
				t.Fatalf("banner JSON must be a single line, got %q", data)
			}
			var got map[string]any
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("field count = %d (%v), want %d (%v)", len(got), got, len(tt.want), tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("field %q = %v, want %v", k, got[k], v)
				}
			}
		})
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

func TestStartDiscoverySkipsUnreachableBindAddresses(t *testing.T) {
	discovery, err := StartDiscovery("127.0.0.1:7262", "fingerprint", "Samantha")
	if err != nil {
		t.Fatalf("loopback discovery: %v", err)
	}
	if discovery != nil {
		discovery.Stop()
		t.Fatal("loopback listener must not advertise on LAN interfaces")
	}
	if _, err := StartDiscovery("0.0.0.0:7262", "fingerprint", "Samantha"); err == nil {
		t.Fatal("unspecified bind advertised without a concrete endpoint")
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
	slow := &streamConn{
		out:   make(chan []byte, 1),
		audio: make(chan []byte, audioQueueDepth),
		kick:  make(chan string, 1),
	}
	h.add(slow)

	h.broadcast([]byte("one")) // fills the event queue
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
	if status["protocol_version"] != float64(ProtocolVersion) {
		t.Errorf("protocol_version = %v, want %d", status["protocol_version"], ProtocolVersion)
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

func TestServerStreamAcceptsQueryToken(t *testing.T) {
	_, addr, creds := startTestServer(t, &scriptedRunner{}, events.NewBus())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, "wss://"+addr+"/v1/stream?token="+creds.Token, &websocket.DialOptions{
		HTTPClient: insecureClient(),
	})
	if err != nil {
		if resp != nil {
			t.Fatalf("query-token WebSocket dial failed with status %d: %v", resp.StatusCode, err)
		}
		t.Fatalf("query-token WebSocket dial failed: %v", err)
	}
	defer ws.Close(websocket.StatusNormalClosure, "")
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

func newTestConn(outDepth, audioDepth int) *streamConn {
	return &streamConn{
		out:   make(chan []byte, outDepth),
		audio: make(chan []byte, audioDepth),
		kick:  make(chan string, 1),
	}
}

// bufferedPCM fills a stream and closes it before PlayStream so tests never
// race on scheduler timing.
func bufferedPCM(t *testing.T, ctx context.Context, rate int, frames []float32) *audio.PCMStream {
	t.Helper()
	stream := audio.NewPCMStream(ctx)
	if err := stream.SetSampleRate(rate); err != nil {
		t.Fatal(err)
	}
	if err := stream.Write(frames); err != nil {
		t.Fatal(err)
	}
	stream.Close()
	return stream
}

func TestAudioFanoutStreamsWithoutLocalSpeaker(t *testing.T) {
	h := newHub()
	conn := newTestConn(16, 16)
	h.add(conn)
	h.setConnAudio(conn, true)

	fanout := NewAudioFanout(nil)
	fanout.AttachHub(h)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	stream := bufferedPCM(t, ctx, 24000, []float32{0.5, -0.5, 0.25})
	playback, err := fanout.PlayStream(ctx, stream)
	if err != nil {
		t.Fatalf("PlayStream: %v", err)
	}
	<-playback.Done()

	var sawChunk, sawEnd bool
	deadline := time.After(2 * time.Second)
	for !sawChunk || !sawEnd {
		select {
		case msg := <-conn.audio:
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

func TestAudioFanoutSkipsEncodeWithoutStreamClients(t *testing.T) {
	h := newHub()
	// Connected but not opted into audio — streamers count stays 0.
	conn := newTestConn(4, 4)
	h.add(conn)

	fanout := NewAudioFanout(nil)
	fanout.AttachHub(h)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream := bufferedPCM(t, ctx, 24000, []float32{0.1, 0.2})
	playback, err := fanout.PlayStream(ctx, stream)
	if err != nil {
		t.Fatal(err)
	}
	<-playback.Done()

	select {
	case msg := <-conn.audio:
		t.Fatalf("unexpected audio message with no stream clients: %s", msg)
	default:
	}
}

func TestAudioFanoutWithLocalEngine(t *testing.T) {
	local := &drainEngine{}
	fanout := NewAudioFanout(local)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	stream := bufferedPCM(t, ctx, 16000, []float32{0.1, 0.2, 0.3, 0.4})
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

	readEnv := func(ws *websocket.Conn, timeout time.Duration) (map[string]any, bool) {
		rctx, rcancel := context.WithTimeout(ctx, timeout)
		defer rcancel()
		_, data, err := ws.Read(rctx)
		if err != nil {
			return nil, false
		}
		var env map[string]any
		_ = json.Unmarshal(data, &env)
		return env, true
	}

	// Only one client opts into audio; wait for the ack (no sleep).
	optIn, _ := json.Marshal(controlMessage{Type: "audio_output", Mode: "stream"})
	if err := wsStream.Write(ctx, websocket.MessageText, optIn); err != nil {
		t.Fatal(err)
	}
	ack, ok := readEnv(wsStream, 2*time.Second)
	if !ok || ack["type"] != "audio_output_ack" || ack["mode"] != "stream" {
		t.Fatalf("audio_output_ack = %v ok=%v", ack, ok)
	}

	// Pre-buffer TTS so PlayStream is deterministic.
	pcmStream := bufferedPCM(t, ctx, 24000, []float32{0.9, -0.9})
	if _, err := fanout.PlayStream(ctx, pcmStream); err != nil {
		t.Fatal(err)
	}

	// Stream client must see audio_chunk (may also see audio_end after).
	var sawChunk bool
	for range 4 {
		env, ok := readEnv(wsStream, 2*time.Second)
		if !ok {
			break
		}
		if env["type"] == "audio_chunk" {
			sawChunk = true
			break
		}
	}
	if !sawChunk {
		t.Fatal("stream client never received audio_chunk")
	}
	// Quiet client should not receive anything from this TTS fanout.
	if env, ok := readEnv(wsQuiet, 100*time.Millisecond); ok {
		t.Fatalf("quiet client unexpectedly received %v", env)
	}
}

func TestHubBroadcastAudioSkipsOptOut(t *testing.T) {
	h := newHub()
	on := newTestConn(2, 2)
	off := newTestConn(2, 2)
	h.add(on)
	h.add(off)
	h.setConnAudio(on, true)

	h.broadcastAudio([]byte(`{"type":"audio_chunk"}`))
	h.broadcast([]byte(`{"type":"info"}`))

	select {
	case <-on.audio:
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
	select {
	case msg := <-off.audio:
		t.Fatalf("opt-out client has audio message: %s", msg)
	default:
	}
}

func TestAudioQueueDropDoesNotKick(t *testing.T) {
	h := newHub()
	// Audio queue depth 1: second chunk drops without kicking.
	c := newTestConn(4, 1)
	h.add(c)
	h.setConnAudio(c, true)

	h.broadcastAudio([]byte("chunk-1"))
	h.broadcastAudio([]byte("chunk-2")) // drop

	select {
	case <-c.kick:
		t.Fatal("full audio queue must not kick the client")
	default:
	}
	// Event channel still works after audio drop.
	h.broadcast([]byte("event"))
	select {
	case msg := <-c.out:
		if string(msg) != "event" {
			t.Fatalf("event = %s", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("event not delivered after audio drop")
	}
}

func TestDecodePCMS16LE(t *testing.T) {
	// two samples: 0 and 16384 (~0.5)
	raw := []byte{0x00, 0x00, 0x00, 0x40}
	b64 := base64.StdEncoding.EncodeToString(raw)
	samples, err := decodePCMS16LE(b64, 16000)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 2 {
		t.Fatalf("len=%d", len(samples))
	}
	if samples[0] != 0 {
		t.Fatalf("s0=%v", samples[0])
	}
	if samples[1] < 0.49 || samples[1] > 0.51 {
		t.Fatalf("s1=%v", samples[1])
	}
	if _, err := decodePCMS16LE(b64, 48000); err == nil {
		t.Fatal("want sample_rate error")
	}
}

func TestSubmitVoiceRequiresVoiceRunner(t *testing.T) {
	d := NewDispatcher(&scriptedRunner{}, events.NewBus(), nil, nil)
	if d.VoiceEnabled() {
		t.Fatal("scriptedRunner is not a VoiceTurnRunner")
	}
	if err := d.SubmitVoice(); err == nil {
		t.Fatal("SubmitVoice must fail without voice runner")
	}
}
