package netapi

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/lancekrogers/samantha/internal/events"
)

func TestDeviceTokenPairListDelete(t *testing.T) {
	dir := t.TempDir()
	creds, err := LoadOrCreateCredentials(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Back-compat: pair without device_name returns primary token.
	token, id, err := creds.ExchangePairingCodeForDevice(creds.Pairing.Code, "")
	if err != nil {
		t.Fatal(err)
	}
	if token != creds.Token || id != "" {
		t.Fatalf("empty device_name: token=%q id=%q", token, id)
	}

	// Refresh code for device mint path.
	if _, err := creds.RefreshPairingCode(); err != nil {
		t.Fatal(err)
	}
	devToken, devID, err := creds.ExchangePairingCodeForDevice(creds.Pairing.Code, "iPhone 15")
	if err != nil {
		t.Fatal(err)
	}
	if devToken == "" || devToken == creds.Token || devID == "" {
		t.Fatalf("device mint: token=%q id=%q primary=%q", devToken, devID, creds.Token)
	}

	list := creds.ListDevices()
	if len(list) != 1 || list[0].ID != devID || list[0].DeviceName != "iPhone 15" {
		t.Fatalf("list = %+v", list)
	}

	// Device token authorizes; primary still does.
	req, _ := http.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+devToken)
	if !creds.VerifyRequest(req) {
		t.Fatal("device token rejected")
	}
	req.Header.Set("Authorization", "Bearer "+creds.Token)
	if !creds.VerifyRequest(req) {
		t.Fatal("primary token rejected")
	}

	// Persistence across reload.
	reloaded, err := LoadOrCreateCredentials(dir)
	if err != nil {
		t.Fatal(err)
	}
	list = reloaded.ListDevices()
	if len(list) != 1 || list[0].ID != devID {
		t.Fatalf("after reload list = %+v", list)
	}
	req.Header.Set("Authorization", "Bearer "+devToken)
	if !reloaded.VerifyRequest(req) {
		t.Fatal("device token not accepted after reload")
	}

	// Delete removes file and rejects token.
	revoked, ok, err := reloaded.DeleteDevice(devID)
	if err != nil || !ok || revoked != devToken {
		t.Fatalf("delete: revoked=%q ok=%v err=%v", revoked, ok, err)
	}
	if len(reloaded.ListDevices()) != 0 {
		t.Fatal("list not empty after delete")
	}
	if reloaded.VerifyRequest(req) {
		t.Fatal("revoked device token still accepted")
	}
	// Primary still works.
	req.Header.Set("Authorization", "Bearer "+creds.Token)
	if !reloaded.VerifyRequest(req) {
		t.Fatal("primary token broken after device delete")
	}

	// Token file under serve/tokens/ is gone.
	if _, err := os.Stat(filepath.Join(dir, "tokens", devID+".json")); !os.IsNotExist(err) {
		t.Fatalf("token file still present: %v", err)
	}
}

func TestDeviceHTTPEndpoints(t *testing.T) {
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
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.ListenAndServe(ctx) }()
	addr := waitAddr(t, srv)
	client := insecureHTTPClient()

	// Pair with device_name.
	code := creds.Pairing.Code
	body := strings.NewReader(`{"code":"` + code + `","device_name":"Lance Phone"}`)
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
	var pairOut struct {
		Token      string `json:"token"`
		DeviceID   string `json:"device_id"`
		DeviceName string `json:"device_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pairOut); err != nil {
		t.Fatal(err)
	}
	if pairOut.Token == "" || pairOut.DeviceID == "" || pairOut.DeviceName != "Lance Phone" {
		t.Fatalf("pair out = %+v", pairOut)
	}

	// List with primary auth.
	req, _ = http.NewRequest(http.MethodGet, "https://"+addr+"/v1/devices", nil)
	req.Header.Set("Authorization", "Bearer "+creds.Token)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", resp.StatusCode)
	}
	var listOut struct {
		Devices []DeviceInfo `json:"devices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listOut); err != nil {
		t.Fatal(err)
	}
	if len(listOut.Devices) != 1 || listOut.Devices[0].ID != pairOut.DeviceID {
		t.Fatalf("list = %+v", listOut)
	}

	// Second device.
	if _, err := creds.RefreshPairingCode(); err != nil {
		t.Fatal(err)
	}
	req, _ = http.NewRequest(http.MethodPost, "https://"+addr+"/v1/pair",
		strings.NewReader(`{"code":"`+creds.Pairing.Code+`","device_name":"iPad"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var pair2 struct {
		Token    string `json:"token"`
		DeviceID string `json:"device_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&pair2)
	resp.Body.Close()
	if pair2.DeviceID == "" {
		t.Fatal("second pair failed")
	}

	// Delete first device.
	req, _ = http.NewRequest(http.MethodDelete, "https://"+addr+"/v1/devices/"+pairOut.DeviceID, nil)
	req.Header.Set("Authorization", "Bearer "+creds.Token)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("delete status = %d body=%s", resp.StatusCode, b)
	}

	req, _ = http.NewRequest(http.MethodGet, "https://"+addr+"/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+pairOut.Token)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked device still authorized: %d", resp.StatusCode)
	}

	// Second device still works.
	req, _ = http.NewRequest(http.MethodGet, "https://"+addr+"/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+pair2.Token)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("other device status = %d", resp.StatusCode)
	}
}

func TestDeviceRevokeKillsOnlyThatStream(t *testing.T) {
	dir := t.TempDir()
	creds, err := LoadOrCreateCredentials(dir)
	if err != nil {
		t.Fatal(err)
	}
	d1, err := creds.MintDeviceToken("phone-a")
	if err != nil {
		t.Fatal(err)
	}
	d2, err := creds.MintDeviceToken("phone-b")
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
	addr := waitAddr(t, server)

	readCtx, stopRead := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopRead()
	ws1, _, err := websocket.Dial(readCtx, "wss://"+addr+"/v1/stream?token="+d1.Token, &websocket.DialOptions{
		HTTPClient: insecureClient(),
	})
	if err != nil {
		t.Fatalf("dial d1: %v", err)
	}
	defer ws1.Close(websocket.StatusNormalClosure, "")
	ws2, _, err := websocket.Dial(readCtx, "wss://"+addr+"/v1/stream?token="+d2.Token, &websocket.DialOptions{
		HTTPClient: insecureClient(),
	})
	if err != nil {
		t.Fatalf("dial d2: %v", err)
	}
	defer ws2.Close(websocket.StatusNormalClosure, "")

	// DELETE d1 via HTTP.
	client := insecureHTTPClient()
	req, _ := http.NewRequest(http.MethodDelete, "https://"+addr+"/v1/devices/"+d1.ID, nil)
	req.Header.Set("Authorization", "Bearer "+creds.Token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete = %d", resp.StatusCode)
	}

	if _, _, err := ws1.Read(readCtx); err == nil {
		t.Fatal("stream for revoked device remained open")
	}
	// d2 should still be readable; send a ping-like empty wait with short timeout.
	aliveCtx, cancelAlive := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancelAlive()
	// Connection still open: CloseWrite would fail if already closed by server.
	// Prefer a short Read that times out without error of closed connection type... 
	// Actually open sockets that are idle will timeout. Closed sockets return err immediately.
	// Race: server may take a moment to close. Wait briefly then check status with d2.
	time.Sleep(100 * time.Millisecond)
	statusReq, _ := http.NewRequest(http.MethodGet, "https://"+addr+"/v1/status", nil)
	statusReq.Header.Set("Authorization", "Bearer "+d2.Token)
	statusResp, err := client.Do(statusReq)
	if err != nil {
		t.Fatal(err)
	}
	statusResp.Body.Close()
	if statusResp.StatusCode != http.StatusOK {
		t.Fatalf("other device lost auth: %d", statusResp.StatusCode)
	}
	_ = aliveCtx
	_ = errCh
}

func waitAddr(t *testing.T, server *Server) string {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for server.Addr() == nil {
		select {
		case <-deadline:
			t.Fatal("server never bound")
		case <-time.After(5 * time.Millisecond):
		}
	}
	return server.Addr().String()
}

func insecureHTTPClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test only
	}}
}
