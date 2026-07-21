package netapi

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/events"
)

// Options configures a Server. All fields except AllowPublic are required.
type Options struct {
	// Bind is the host:port to listen on. The host must resolve to a
	// loopback, private (RFC1918), link-local, or trusted overlay
	// (Tailscale/CGNAT 100.64/10) address unless AllowPublic is set —
	// serve refuses broader exposure by default.
	Bind        string
	AllowPublic bool

	Credentials  *Credentials
	Bus          *events.Bus
	Dispatcher   *Dispatcher
	ListSessions func() []SessionSummary
	Providers    Providers
	// Audio, when set, is attached to the server hub so Phase 3 stream
	// clients receive TTS audio_chunk envelopes from the pipeline player.
	Audio *AudioFanout
	// Ingress, when set, enables remote push-to-talk (Phase 4 / WI-62e19b).
	// The serve pipeline's STT must already be wired to this same ingress.
	Ingress *audio.Ingress
	// OnListening is called once the TCP listener is bound, before Accept
	// loops run. Use it to print banners with the real bound address.
	OnListening func(addr net.Addr)
}

// Server is the LAN-facing HTTPS + WebSocket surface around one pipeline.
type Server struct {
	opts         Options
	dispatcher   *Dispatcher
	listSessions func() []SessionSummary
	providers    Providers
	hub          *hub
	limiter      *rateLimiter
	started      time.Time

	mu   sync.Mutex
	addr net.Addr
}

func New(opts Options) *Server {
	h := newHub()
	if opts.Audio != nil {
		opts.Audio.AttachHub(h)
	}
	if opts.Ingress != nil {
		h.setIngress(opts.Ingress)
	}
	return &Server{
		opts:         opts,
		dispatcher:   opts.Dispatcher,
		listSessions: opts.ListSessions,
		providers:    opts.Providers,
		hub:          h,
		limiter:      newRateLimiter(30, 10*time.Second),
	}
}

// Addr returns the bound listener address once ListenAndServe has started.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

// ListenAndServe serves until ctx is canceled, then shuts down gracefully.
// The bus subscription is detached on return — the pipeline outlives the
// server cleanly.
func (s *Server) ListenAndServe(ctx context.Context) error {
	if err := validateBind(s.opts.Bind, s.opts.AllowPublic); err != nil {
		return err
	}

	detach := s.hub.attachBus(s.opts.Bus)
	defer detach()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/stream", s.handleStream)
	mux.HandleFunc("GET /v1/status", s.handleStatus)
	mux.HandleFunc("GET /v1/sessions", s.handleSessions)
	mux.HandleFunc("POST /v1/sessions/{id}/resume", s.handleResume)
	// Phase 2: public pairing exchange (short code → long-lived token).
	mux.HandleFunc("POST /v1/pair", s.handlePair)
	// PROTOCOL_DELTAS D2: per-device token list / revoke.
	mux.HandleFunc("GET /v1/devices", s.handleDevices)
	mux.HandleFunc("DELETE /v1/devices/{id}", s.handleDeviceDelete)
	// Embedded phone voice client (public HTML/JS; WS still authenticated).
	web := webFileServer()
	mux.Handle("GET /{$}", web)
	mux.Handle("GET /index.html", web)
	mux.Handle("GET /app.js", web)

	server := &http.Server{
		Handler:           s.limiter.middleware(s.authMiddleware(mux)),
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{s.opts.Credentials.Certificate},
			MinVersion:   tls.VersionTLS12,
		},
	}

	ln, err := net.Listen("tcp", s.opts.Bind)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.opts.Bind, err)
	}
	s.mu.Lock()
	s.addr = ln.Addr()
	s.started = time.Now()
	s.mu.Unlock()

	if s.opts.OnListening != nil {
		s.opts.OnListening(ln.Addr())
	}

	errCh := make(chan error, 1)
	go func() { errCh <- server.ServeTLS(ln, "", "") }()
	monitorCtx, stopMonitor := context.WithCancel(ctx)
	defer stopMonitor()
	revoked := s.watchToken(monitorCtx)

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		return nil
	case <-revoked:
		s.hub.evictAll("credentials revoked")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// watchToken closes its result when another process revokes or rotates the
// credentials file. A nil result disables the select case for credentials
// assembled directly in tests without a backing directory.
func (s *Server) watchToken(ctx context.Context) <-chan struct{} {
	if s.opts.Credentials == nil || s.opts.Credentials.Dir == "" {
		return nil
	}
	revoked := make(chan struct{})
	go func() {
		defer close(revoked)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !s.opts.Credentials.tokenActive() {
					return
				}
			}
		}
	}()
	return revoked
}

// authMiddleware enforces the mandatory bearer token on every endpoint
// except the embedded voice page assets (see isPublicPath).
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if !s.opts.Credentials.VerifyRequest(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing or invalid bearer token"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// cgnatRange is RFC 6598 carrier-grade NAT (100.64.0.0/10). Tailscale assigns
// addresses from this range; net.IP.IsPrivate does not cover it.
var cgnatRange = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// IsTrustedServeIP reports whether ip is safe to bind without --allow-public:
// loopback, RFC1918 private, link-local, or Tailscale/CGNAT overlay.
func IsTrustedServeIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return true
	}
	return cgnatRange.Contains(ip)
}

// validateBind refuses to listen beyond loopback/private/link-local/tailnet
// unless explicitly overridden.
func validateBind(bind string, allowPublic bool) error {
	host, _, err := net.SplitHostPort(bind)
	if err != nil {
		return fmt.Errorf("invalid bind address %q: %w", bind, err)
	}
	if allowPublic {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("bind host %q must be an IP address (use --allow-public to override)", host)
	}
	if ip.IsUnspecified() {
		return fmt.Errorf("refusing to bind %s: it exposes every interface — bind a private address or pass --allow-public", host)
	}
	if !IsTrustedServeIP(ip) {
		return fmt.Errorf("refusing to bind non-private address %s without --allow-public", host)
	}
	return nil
}

// rateLimiter is a fixed-window per-IP cap — a basic abuse guard for a
// single-user tool, not load engineering.
type rateLimiter struct {
	mu       sync.Mutex
	window   time.Duration
	limit    int
	counts   map[string]int
	windowAt time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		window: window,
		limit:  limit,
		counts: make(map[string]int),
	}
}

func (l *rateLimiter) allow(remoteAddr string, now time.Time) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.windowAt) > l.window {
		l.counts = make(map[string]int)
		l.windowAt = now
	}
	l.counts[host]++
	return l.counts[host] <= l.limit
}

func (l *rateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.allow(r.RemoteAddr, time.Now()) {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate limit exceeded"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
