package extractors

import (
	"context"
	"fmt"
	"io"
	"net/http"
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
	Timeout   time.Duration
	MaxBytes  int64
	UserAgent string
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
	if timeout <= 0 {
		timeout = DefaultFetchTimeout
	}
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxFetchRedirects {
				return fmt.Errorf("fetch: stopped after %d redirects", maxFetchRedirects)
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
	if client == nil {
		client = NewFetchClient(opts.Timeout)
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
