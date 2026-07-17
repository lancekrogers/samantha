//go:build integration && calibre

package calibre

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRealCalibredbSearch runs against a real calibredb + library.
// Enable with: go test -tags 'integration calibre' ./internal/calibre/
func TestRealCalibredbSearch(t *testing.T) {
	c := Client{}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	books, err := c.Search(ctx, "cryptography", 20)
	if err != nil {
		t.Fatalf("Search: %v (install Calibre or skip with -tags without calibre)", err)
	}
	if len(books) == 0 {
		t.Log("no cryptography matches (library may differ); Search succeeded")
		return
	}
	for _, b := range books {
		if b.Title == "" {
			t.Fatalf("empty title: %+v", b)
		}
		// BestFormatPath should either return epub/pdf or ErrNoSupportedFormat.
		if _, _, err := c.BestFormatPath(b); err != nil && !errors.Is(err, ErrNoSupportedFormat) {
			t.Fatalf("BestFormatPath(%d): %v", b.ID, err)
		}
	}
}
