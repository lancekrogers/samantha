package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
)

// faultChatError is the one supported fault mode: ollama traffic passes
// through untouched except /api/chat, which fails with 500. Startup checks
// (/api/tags, model validation) succeed, so the failure lands exactly where
// field failures do — mid-conversation, at turn time.
const faultChatError = "chat_error"

// startFaultProxy serves the instance's faulted ollama endpoint. Returns the
// proxy URL to seed as ollama_host and a stop func.
func startFaultProxy(mode, upstream string) (string, func(), error) {
	if mode != faultChatError {
		return "", nil, fmt.Errorf("unknown fault mode %q", mode)
	}
	target, err := url.Parse(upstream)
	if err != nil {
		return "", nil, fmt.Errorf("fault proxy upstream: %w", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, fmt.Errorf("fault proxy listen: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "duet fault injection: chat_error", http.StatusInternalServerError)
	})
	mux.Handle("/", httputil.NewSingleHostReverseProxy(target))
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	return "http://" + ln.Addr().String(), func() { _ = srv.Close() }, nil
}
