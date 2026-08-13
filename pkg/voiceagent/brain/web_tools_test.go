package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestToolWebSearchReturnsStructuredResults(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got := r.Form.Get("q"); got != "most expensive home Peru" {
			t.Errorf("query = %q", got)
		}
		fmt.Fprint(w, `<html><body><div class="result results_links">
<a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Flisting">Luxury listing</a>
<a class="result__snippet">A record-priced home in Lima.</a>
</div></body></html>`)
	}))
	t.Cleanup(server.Close)

	raw := toolWebSearchWithClient(context.Background(), map[string]any{
		"query": "most expensive home Peru",
	}, server.Client(), server.URL)
	var got struct {
		Query   string            `json:"query"`
		Results []webSearchResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("result is not JSON: %v: %s", err, raw)
	}
	if got.Query != "most expensive home Peru" || len(got.Results) != 1 {
		t.Fatalf("result = %#v", got)
	}
	if got.Results[0].URL != "https://example.com/listing" || got.Results[0].Title != "Luxury listing" {
		t.Fatalf("search result = %#v", got.Results[0])
	}
}

func TestToolFetchURLReturnsReadableText(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>Listing</title><style>hidden</style></head><body><h1>Ocean home</h1><script>hidden()</script><p>Price: $20 million</p></body></html>`)
	}))
	t.Cleanup(server.Close)

	raw := toolFetchURLWithClient(context.Background(), map[string]any{"url": server.URL}, server.Client())
	var got struct {
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("result is not JSON: %v: %s", err, raw)
	}
	if got.Title != "Listing" || !strings.Contains(got.Content, "Price: $20 million") {
		t.Fatalf("fetch result = %#v", got)
	}
	if strings.Contains(got.Content, "hidden") {
		t.Fatalf("fetch content retained script/style text: %q", got.Content)
	}
}

func TestToolFetchURLRejectsNonHTTPURL(t *testing.T) {
	t.Parallel()
	got := toolFetchURL(context.Background(), map[string]any{"url": "file:///etc/passwd"})
	if !strings.Contains(got, "HTTP or HTTPS") {
		t.Fatalf("result = %q", got)
	}
}
