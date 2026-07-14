//go:build !integration

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverTailscaleWith(t *testing.T) {
	raw := []byte(`{
		"Self": {
			"DNSName": "mac-studio.tail37114b.ts.net.",
			"TailscaleIPs": ["fd7a:115c:a1e0::1", "100.72.165.77"]
		}
	}`)
	id, err := discoverTailscaleWith(func() ([]byte, error) { return raw, nil })
	if err != nil {
		t.Fatal(err)
	}
	if id.IPv4 != "100.72.165.77" {
		t.Fatalf("IPv4 = %q", id.IPv4)
	}
	if id.DNSName != "mac-studio.tail37114b.ts.net" {
		t.Fatalf("DNSName = %q", id.DNSName)
	}
}

func TestDiscoverTailscaleRequiresIPv4(t *testing.T) {
	raw := []byte(`{"Self":{"DNSName":"x.ts.net.","TailscaleIPs":["fd7a::1"]}}`)
	if _, err := discoverTailscaleWith(func() ([]byte, error) { return raw, nil }); err == nil {
		t.Fatal("expected error without IPv4")
	}
}

func TestEnsureTailscaleCert(t *testing.T) {
	dir := t.TempDir()
	old := certRunner
	t.Cleanup(func() { certRunner = old })
	certRunner = func(domain, certPath, keyPath string) error {
		if domain != "mac.example.ts.net" {
			t.Fatalf("domain = %q", domain)
		}
		if err := os.WriteFile(certPath, []byte("CERT"), 0o600); err != nil {
			return err
		}
		return os.WriteFile(keyPath, []byte("KEY"), 0o600)
	}
	cert, key, err := ensureTailscaleCert(dir, "mac.example.ts.net")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(cert, ".crt") || !strings.HasSuffix(key, ".key") {
		t.Fatalf("paths = %q %q", cert, key)
	}
	if _, err := os.Stat(cert); err != nil {
		t.Fatal(err)
	}
	// Second call overwrites same stable paths.
	cert2, _, err := ensureTailscaleCert(dir, "mac.example.ts.net")
	if err != nil {
		t.Fatal(err)
	}
	if cert2 != cert {
		t.Fatalf("cert path drifted: %q vs %q", cert, cert2)
	}
	if filepath.Dir(cert) != dir {
		t.Fatalf("cert not under dir: %s", cert)
	}
}

func TestPublicServeURL(t *testing.T) {
	if got := publicServeURL("mac.tail.ts.net", 7262); got != "https://mac.tail.ts.net:7262/" {
		t.Fatalf("got %q", got)
	}
	if got := publicServeURL("100.1.2.3", defaultServePort); !strings.Contains(got, "100.1.2.3") {
		t.Fatalf("got %q", got)
	}
}
