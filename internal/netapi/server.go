package netapi

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/events"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
	"github.com/lancekrogers/samantha/internal/meeting/remote"
)

// Options configures a Server. All fields except AllowPublic are required.
type Options struct {
	// Bind is the host:port to listen on. The host must resolve to a
	// loopback, private (RFC1918), link-local, or trusted overlay
	// (Tailscale/CGNAT 100.64/10) address unless AllowPublic is set —
	// serve refuses broader exposure by default.
	Bind        string
	AllowPublic bool
	// ExtraBinds are additional host:port addresses served by the same
	// handler, TLS certificate, and auth. Each entry passes the same
	// validateBind policy as Bind. Lets one serve reach loopback clients
	// (the Mac app) and LAN clients (a paired phone) simultaneously.
	// Duplicates of Bind or of earlier entries are ignored.
	ExtraBinds []string

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
	// OnListening is called once every TCP listener is bound, before Accept
	// loops run. addrs are the actual bound addresses (primary first, deduped,
	// real ports under :0 requests) — advertise these, never the requested
	// bind strings.
	OnListening func(addrs []net.Addr)
	// IntentSink configures POST /v1/intent (PROTOCOL_DELTAS D3). Optional;
	// defaults to file mode under credentials Dir/intents.
	IntentSink IntentSinkConfig
	// Meetings enables the /v1/meeting capture surface (PROTOCOL_DELTAS D6).
	// Optional: when nil those routes are not registered and GET /v1/status
	// reports the meetings capability as false.
	Meetings *remote.Manager
	// RouteMeeting files a finished meeting into a campaign
	// (POST /v1/meeting/{id}/route → `camp idea notes import-meeting`).
	// Injected by serve so netapi stays out of camp discovery and config;
	// nil answers that route with 503.
	RouteMeeting RouteMeetingFunc
}

// RouteMeetingFunc routes a finished meeting's summary to a campaign.
// Implementations gate on camp CI0009 support (meeting.SupportsImportMeeting)
// before filing when capture resolves to the meetings importer.
type RouteMeetingFunc func(ctx context.Context, summary meetinglog.Summary, campaign, capture string) (remote.RouteReceipt, error)

// Server is the LAN-facing HTTPS + WebSocket surface around one pipeline.
type Server struct {
	opts         Options
	dispatcher   *Dispatcher
	listSessions func() []SessionSummary
	providers    Providers
	hub          *hub
	limiter      *rateLimiter
	meetings     *remote.Manager
	routeMeeting RouteMeetingFunc
	// segmentLimiter gives meeting audio its own budget. Resuming a
	// ten-minute outbox is a legitimate burst of ~120 small, idempotent,
	// size-capped writes; the general abuse guard would throttle exactly the
	// recovery path the meeting design exists for.
	segmentLimiter *rateLimiter
	started        time.Time

	mu    sync.Mutex
	addr  net.Addr
	addrs []net.Addr
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
		opts:           opts,
		dispatcher:     opts.Dispatcher,
		listSessions:   opts.ListSessions,
		providers:      opts.Providers,
		hub:            h,
		limiter:        newRateLimiter(30, 10*time.Second),
		meetings:       opts.Meetings,
		routeMeeting:   opts.RouteMeeting,
		segmentLimiter: newRateLimiter(segmentRateLimit, 10*time.Second),
	}
}

// Addr returns the primary bound listener address once ListenAndServe has
// started.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

// Addrs returns every bound listener address (Bind first, then ExtraBinds)
// once ListenAndServe has started.
func (s *Server) Addrs() []net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]net.Addr(nil), s.addrs...)
}

// ListenAndServe serves until ctx is canceled, then shuts down gracefully.
// The bus subscription is detached on return — the pipeline outlives the
// server cleanly.
func (s *Server) ListenAndServe(ctx context.Context) error {
	binds := []string{s.opts.Bind}
	seen := map[string]struct{}{s.opts.Bind: {}}
	for _, b := range s.opts.ExtraBinds {
		if _, dup := seen[b]; dup {
			continue
		}
		seen[b] = struct{}{}
		binds = append(binds, b)
	}
	for _, b := range binds {
		if err := validateBind(b, s.opts.AllowPublic); err != nil {
			return err
		}
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
	// PROTOCOL_DELTAS D3: intent capture + targets.
	mux.HandleFunc("POST /v1/intent", s.handleIntent)
	mux.HandleFunc("GET /v1/intent/targets", s.handleIntentTargets)
	// PROTOCOL_DELTAS D6: phone-driven meeting capture. Registered only when
	// configured, so a serve without it answers 404 rather than pretending.
	if s.meetings != nil {
		mux.HandleFunc("POST /v1/meeting/start", s.handleMeetingStart)
		mux.HandleFunc("PUT /v1/meeting/{id}/segments/{seq}", s.handleMeetingSegment)
		mux.HandleFunc("POST /v1/meeting/{id}/control", s.handleMeetingControl)
		mux.HandleFunc("POST /v1/meeting/{id}/stop", s.handleMeetingStop)
		mux.HandleFunc("GET /v1/meeting/{id}", s.handleMeetingStatus)
		mux.HandleFunc("POST /v1/meeting/{id}/route", s.handleMeetingRoute)
		mux.HandleFunc("GET /v1/meeting/{id}/document", s.handleMeetingDocument)
	}
	// Embedded phone voice client (public HTML/JS; WS still authenticated).
	web := webFileServer()
	mux.Handle("GET /{$}", web)
	mux.Handle("GET /index.html", web)
	mux.Handle("GET /app.js", web)

	server := &http.Server{
		Handler:           s.rateLimit(s.authMiddleware(mux)),
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{s.opts.Credentials.Certificate},
			MinVersion:   tls.VersionTLS12,
		},
	}

	lns := make([]net.Listener, 0, len(binds))
	for _, b := range binds {
		ln, err := net.Listen("tcp", b)
		if err != nil {
			for _, open := range lns {
				_ = open.Close()
			}
			return fmt.Errorf("listen on %s: %w", b, err)
		}
		lns = append(lns, ln)
	}
	s.mu.Lock()
	s.addr = lns[0].Addr()
	s.addrs = make([]net.Addr, len(lns))
	for i, ln := range lns {
		s.addrs[i] = ln.Addr()
	}
	s.started = time.Now()
	s.mu.Unlock()

	if s.opts.OnListening != nil {
		s.opts.OnListening(s.Addrs())
	}

	errCh := make(chan error, len(lns))
	for _, ln := range lns {
		go func() { errCh <- server.ServeTLS(ln, "", "") }()
	}
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
		// One listener failed; bring the rest down before reporting.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
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

// segmentRateLimit is the per-IP allowance for meeting segment uploads in the
// same ten-second window the general limiter uses. It covers a full outbox
// drain (~120 segments) plus live capture continuing underneath it.
const segmentRateLimit = 240

// rateLimit applies the general abuse guard, except to meeting segment
// uploads, which get their own budget (see Server.segmentLimiter).
func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limiter := s.limiter
		if isMeetingSegmentPath(r.Method, r.URL.Path) {
			limiter = s.segmentLimiter
		}
		if !limiter.allow(r.RemoteAddr, time.Now()) {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate limit exceeded"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isMeetingSegmentPath matches PUT /v1/meeting/{id}/segments/{seq}.
func isMeetingSegmentPath(method, path string) bool {
	return method == http.MethodPut &&
		strings.HasPrefix(path, "/v1/meeting/") &&
		strings.Contains(path, "/segments/")
}
