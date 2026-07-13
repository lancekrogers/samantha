package netapi

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Credentials are the bearer token and TLS identity `serve` requires on
// every connection. Auth is mandatory — there is no "trusted LAN, skip
// auth" mode.
type Credentials struct {
	Token       string
	Certificate tls.Certificate
	Fingerprint string // SHA-256 of the leaf cert DER, hex

	// TokenCreated reports whether this load generated a fresh token; the
	// caller prints the token exactly once, at creation.
	TokenCreated bool

	// ExternalTLS is true when the certificate was loaded from caller-
	// supplied paths (e.g. `tailscale cert` material) instead of the
	// self-signed TOFU pair under the serve credentials dir.
	ExternalTLS bool
}

const (
	tokenFile = "token"
	certFile  = "cert.pem"
	keyFile   = "key.pem"
)

// LoadOrCreateCredentials loads the serve token and a self-signed TLS
// certificate from dir, generating any that are missing. Secrets are stored
// 0600 and never land in the YAML config.
func LoadOrCreateCredentials(dir string) (*Credentials, error) {
	return loadCredentials(dir, "", "")
}

// LoadOrCreateCredentialsWithTLS loads the serve token from dir and a
// caller-supplied TLS certificate/key pair (e.g. from `tailscale cert`).
// Both certPath and keyPath are required when either is set.
func LoadOrCreateCredentialsWithTLS(dir, certPath, keyPath string) (*Credentials, error) {
	if (certPath == "") != (keyPath == "") {
		return nil, fmt.Errorf("both --tls-cert and --tls-key are required when loading an external certificate")
	}
	return loadCredentials(dir, certPath, keyPath)
}

func loadCredentials(dir, externalCert, externalKey string) (*Credentials, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create serve credentials dir: %w", err)
	}

	creds := &Credentials{}

	tokenPath := filepath.Join(dir, tokenFile)
	tokenBytes, err := os.ReadFile(tokenPath)
	switch {
	case err == nil:
		creds.Token = strings.TrimSpace(string(tokenBytes))
	case os.IsNotExist(err):
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return nil, fmt.Errorf("generate token: %w", err)
		}
		creds.Token = hex.EncodeToString(raw)
		if err := os.WriteFile(tokenPath, []byte(creds.Token+"\n"), 0o600); err != nil {
			return nil, fmt.Errorf("store token: %w", err)
		}
		creds.TokenCreated = true
	default:
		return nil, fmt.Errorf("read token: %w", err)
	}
	if creds.Token == "" {
		return nil, fmt.Errorf("token file %s is empty — delete it to regenerate", tokenPath)
	}

	certPath, keyPath := externalCert, externalKey
	if certPath == "" {
		certPath, keyPath = filepath.Join(dir, certFile), filepath.Join(dir, keyFile)
		if _, err := os.Stat(certPath); os.IsNotExist(err) {
			if err := generateSelfSignedCert(certPath, keyPath); err != nil {
				return nil, err
			}
		}
	} else {
		creds.ExternalTLS = true
	}

	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load TLS certificate: %w", err)
	}
	creds.Certificate = cert
	sum := sha256.Sum256(cert.Certificate[0])
	creds.Fingerprint = hex.EncodeToString(sum[:])

	return creds, nil
}

// VerifyRequest checks the Authorization bearer header, or a token query
// parameter. Browsers cannot set custom headers on WebSocket handshakes, so
// the embedded voice page authenticates with ?token=.
func (c *Credentials) VerifyRequest(r *http.Request) bool {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if strings.HasPrefix(header, prefix) {
		presented := strings.TrimPrefix(header, prefix)
		if subtle.ConstantTimeCompare([]byte(presented), []byte(c.Token)) == 1 {
			return true
		}
	}
	// Query token is for browser WS only — still constant-time compare.
	q := r.URL.Query().Get("token")
	if q == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(q), []byte(c.Token)) == 1
}

// generateSelfSignedCert writes a fresh ECDSA P-256 certificate for the
// trust-on-first-use model: clients pin the fingerprint at pairing time, so
// SANs are a convenience, not the trust anchor.
func generateSelfSignedCert(certPath, keyPath string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate TLS key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("generate serial: %w", err)
	}

	hostname, _ := os.Hostname()
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "samantha-serve"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		// ECDSA: digital signature only (KeyEncipherment is an RSA concept).
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	if hostname != "" {
		tmpl.DNSNames = append(tmpl.DNSNames, hostname)
	}

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("create certificate: %w", err)
	}

	certOut, err := os.OpenFile(certPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("write certificate: %w", err)
	}
	defer certOut.Close()
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		return fmt.Errorf("encode certificate: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal TLS key: %w", err)
	}
	keyOut, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("write TLS key: %w", err)
	}
	defer keyOut.Close()
	if err := pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}); err != nil {
		return fmt.Errorf("encode TLS key: %w", err)
	}

	return nil
}
