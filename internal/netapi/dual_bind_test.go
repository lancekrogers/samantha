package netapi

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/pkg/voiceagent/events"
)

// reserveLoopbackPort binds an ephemeral loopback port and releases it so a
// test can hand a concrete second address to ExtraBinds.
func reserveLoopbackPort(t *testing.T) int {
	t.Helper()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func TestListenAndServeExtraBindsServeSameHandler(t *testing.T) {
	creds, err := LoadOrCreateCredentials(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	dispatcher := NewDispatcher(&scriptedRunner{}, bus, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go dispatcher.Run(ctx)

	extra := fmt.Sprintf("127.0.0.1:%d", reserveLoopbackPort(t))
	server := New(Options{
		Bind: "127.0.0.1:0",
		// The duplicate entry must be ignored, not become a bind conflict.
		ExtraBinds:  []string{extra, extra},
		Credentials: creds,
		Bus:         bus,
		Dispatcher:  dispatcher,
	})
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe(ctx) }()

	deadline := time.After(2 * time.Second)
	for len(server.Addrs()) < 2 {
		select {
		case <-deadline:
			t.Fatalf("expected 2 bound addresses, have %v", server.Addrs())
		case <-time.After(5 * time.Millisecond):
		}
	}

	addrs := server.Addrs()
	if len(addrs) != 2 {
		t.Fatalf("Addrs() = %v, want exactly 2 (duplicate extra deduped)", addrs)
	}
	if addrs[0].String() != server.Addr().String() {
		t.Fatalf("Addr() %s must be the primary in Addrs() %v", server.Addr(), addrs)
	}

	client := insecureClient()
	for _, addr := range addrs {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+addr.String()+"/v1/status", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+creds.Token)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET status via %s: %v", addr, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET status via %s: code %d", addr, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("ListenAndServe after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListenAndServe did not return after cancel")
	}
}

func TestOnListeningReportsActualExtraAddresses(t *testing.T) {
	creds, err := LoadOrCreateCredentials(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	dispatcher := NewDispatcher(&scriptedRunner{}, bus, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go dispatcher.Run(ctx)

	primary := fmt.Sprintf("127.0.0.1:%d", reserveLoopbackPort(t))
	got := make(chan []net.Addr, 1)
	server := New(Options{
		Bind: primary,
		// An ephemeral extra: the requested string is useless to clients —
		// the callback must carry the real port the listener received.
		ExtraBinds:  []string{"127.0.0.1:0"},
		Credentials: creds,
		Bus:         bus,
		Dispatcher:  dispatcher,
		OnListening: func(addrs []net.Addr) { got <- addrs },
	})
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe(ctx) }()

	var addrs []net.Addr
	select {
	case addrs = <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("OnListening never fired")
	}
	if len(addrs) != 2 {
		t.Fatalf("OnListening addrs = %v, want primary + extra", addrs)
	}
	if addrs[0].String() != primary {
		t.Fatalf("primary must come first: %v (want %s)", addrs, primary)
	}
	extra, ok := addrs[1].(*net.TCPAddr)
	if !ok || extra.Port == 0 {
		t.Fatalf("extra must carry its actual ephemeral port, got %v", addrs[1])
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("ListenAndServe after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListenAndServe did not return after cancel")
	}
}

func TestListenAndServeRejectsPublicExtraBind(t *testing.T) {
	creds, err := LoadOrCreateCredentials(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := New(Options{
		Bind:        "127.0.0.1:0",
		ExtraBinds:  []string{"8.8.8.8:443"},
		Credentials: creds,
		Bus:         events.NewBus(),
		Dispatcher:  NewDispatcher(&scriptedRunner{}, events.NewBus(), nil, nil),
	})
	err = server.ListenAndServe(context.Background())
	if err == nil || !strings.Contains(err.Error(), "refusing to bind") {
		t.Fatalf("public extra bind must be refused, got %v", err)
	}
}
