package extractors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchArticleSuccess(t *testing.T) {
	const html = "<html><head><title>T</title></head><body><p>Hello</p></body></html>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("request missing User-Agent")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html))
	}))
	defer srv.Close()

	body, err := FetchArticle(context.Background(), srv.Client(), srv.URL, FetchOptions{})
	if err != nil {
		t.Fatalf("FetchArticle() error = %v", err)
	}
	if string(body) != html {
		t.Errorf("body = %q, want the served HTML", body)
	}
}

func TestFetchArticleRejectsNonHTMLContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF"))
	}))
	defer srv.Close()

	_, err := FetchArticle(context.Background(), srv.Client(), srv.URL, FetchOptions{})
	if err == nil || !strings.Contains(err.Error(), "content-type") {
		t.Fatalf("error = %v, want a content-type rejection", err)
	}
}

func TestFetchArticleEnforcesSizeLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(strings.Repeat("x", 1000)))
	}))
	defer srv.Close()

	_, err := FetchArticle(context.Background(), srv.Client(), srv.URL, FetchOptions{MaxBytes: 100})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v, want a size-limit error", err)
	}
}

func TestFetchArticleHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	_, err := FetchArticle(context.Background(), srv.Client(), srv.URL, FetchOptions{})
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("error = %v, want an HTTP error", err)
	}
}

func TestFetchArticleCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // never respond until the client cancels
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := FetchArticle(ctx, srv.Client(), srv.URL, FetchOptions{})
	if err == nil {
		t.Fatal("expected a cancellation/timeout error")
	}
}
