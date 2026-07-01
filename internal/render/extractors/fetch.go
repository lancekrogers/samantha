package extractors

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Default article fetch safeguards.
const (
	DefaultFetchTimeout   = 20 * time.Second
	DefaultMaxArticleSize = 5 << 20 // 5 MiB
	DefaultUserAgent      = "samantha-render/1.0"
	maxFetchRedirects     = 5
)

// FetchOptions configures article fetching safeguards.
type FetchOptions struct {
	Timeout           time.Duration
	MaxBytes          int64
	UserAgent         string
	AllowPrivateHosts bool
}

func (o FetchOptions) withDefaults() FetchOptions {
	if o.Timeout <= 0 {
		o.Timeout = DefaultFetchTimeout
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultMaxArticleSize
	}
	if o.UserAgent == "" {
		o.UserAgent = DefaultUserAgent
	}
	return o
}

// NewFetchClient returns an *http.Client with the timeout and a redirect limit
// applied. Tests can pass their own client to FetchArticle instead.
func NewFetchClient(timeout time.Duration) *http.Client {
	return newFetchClient(timeout, false)
}

func newFetchClient(timeout time.Duration, allowPrivateHosts bool) *http.Client {
	if timeout <= 0 {
		timeout = DefaultFetchTimeout
	}
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxFetchRedirects {
				return fmt.Errorf("fetch: stopped after %d redirects", maxFetchRedirects)
			}
			if err := validateFetchURL(req.URL.String(), allowPrivateHosts); err != nil {
				return err
			}
			return nil
		},
	}
}

// FetchArticle GETs rawURL through client and returns the HTML body, enforcing a
// content-type check, a max body size, and a user agent. The context bounds the
// request. Fetching is kept separate from extraction so extraction can be tested
// without network.
func FetchArticle(ctx context.Context, client *http.Client, rawURL string, opts FetchOptions) ([]byte, error) {
	opts = opts.withDefaults()
	if err := validateFetchURL(rawURL, opts.AllowPrivateHosts); err != nil {
		return nil, err
	}
	if client == nil {
		client = newFetchClient(opts.Timeout, opts.AllowPrivateHosts)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	req.Header.Set("User-Agent", opts.UserAgent)
	req.Header.Set("Accept", "text/html,text/plain")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: HTTP %d", rawURL, resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" &&
		!strings.Contains(ct, "text/html") && !strings.Contains(ct, "text/plain") {
		return nil, fmt.Errorf("fetch %s: unexpected content-type %q", rawURL, ct)
	}

	// Read one byte past the limit to detect oversize bodies.
	body, err := io.ReadAll(io.LimitReader(resp.Body, opts.MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	if int64(len(body)) > opts.MaxBytes {
		return nil, fmt.Errorf("fetch %s: body exceeds %d byte limit", rawURL, opts.MaxBytes)
	}
	return body, nil
}

func validateFetchURL(rawURL string, allowPrivateHosts bool) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("fetch %s: unsupported URL scheme %q", rawURL, u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("fetch %s: missing host", rawURL)
	}
	if allowPrivateHosts {
		return nil
	}
	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("fetch %s: host %q is not allowed", rawURL, host)
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("fetch %s: resolve host: %w", rawURL, err)
	}
	for _, ip := range ips {
		if isPrivateFetchIP(ip) {
			return fmt.Errorf("fetch %s: host %q resolves to disallowed address %s", rawURL, host, ip)
		}
	}
	return nil
}

func isPrivateFetchIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast()
}
