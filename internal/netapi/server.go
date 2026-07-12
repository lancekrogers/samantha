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

	"github.com/lancekrogers/samantha/internal/events"
)

// Options configures a Server. All fields except AllowPublic are required.
type Options struct {
	// Bind is the host:port to listen on. The host must resolve to a
	// loopback, private (RFC1918), or link-local address unless AllowPublic
	// is set — serve refuses broader exposure by default.
	Bind        string
	AllowPublic bool

	Credentials  *Credentials
	Bus          *events.Bus
	Dispatcher   *Dispatcher
	ListSessions func() []SessionSummary
	Providers    Providers
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
	return &Server{
		opts:         opts,
		dispatcher:   opts.Dispatcher,
		listSessions: opts.ListSessions,
		providers:    opts.Providers,
		hub:          newHub(),
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

	errCh := make(chan error, 1)
	go func() { errCh <- server.ServeTLS(ln, "", "") }()

	select {
	case <-ctx.Done():
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

// authMiddleware enforces the mandatory bearer token on every endpoint.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.opts.Credentials.VerifyRequest(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing or invalid bearer token"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// validateBind refuses to listen beyond loopback/private/link-local unless
// explicitly overridden.
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
	if !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() {
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
