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
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Credentials are the bearer token and TLS identity `serve` requires on
// every connection. Auth is mandatory — there is no "trusted LAN, skip
// auth" mode.
//
// Token is the primary/shared bearer (serve/token). PROTOCOL_DELTAS D2 also
// mints per-device tokens under serve/tokens/; either form authenticates.
type Credentials struct {
	Token       string
	Certificate tls.Certificate
	Fingerprint string // SHA-256 of the leaf cert DER, hex
	Dir         string // credentials directory (token/cert files)

	// TokenCreated reports whether this load generated a fresh token; the
	// caller prints the token exactly once, at creation.
	TokenCreated bool

	// ExternalTLS is true when the certificate was loaded from caller-
	// supplied paths (e.g. `tailscale cert` material) instead of the
	// self-signed TOFU pair under the serve credentials dir.
	ExternalTLS bool

	// Pairing is a short-lived code clients exchange for Token over TLS.
	// Regenerated each serve start; single-use once exchanged.
	Pairing *PairingCode

	// devices holds per-device tokens (nil only for in-memory test creds
	// constructed without LoadOrCreateCredentials).
	devices *deviceStore

	pairingMu sync.Mutex
}

// PairingCode is a single-use, short-lived code for LAN/tailnet device
// pairing without pasting the long bearer token by hand.
type PairingCode struct {
	Code      string
	ExpiresAt time.Time
	used      bool
}

// pairingTTL is how long a printed pairing code remains valid.
const pairingTTL = 10 * time.Minute

const (
	tokenFile = "token"
	certFile  = "cert.pem"
	keyFile   = "key.pem"
)

// CertIdentity supplies optional DNS names and IPs embedded as SANs when a
// new self-signed certificate is minted. Existing cert files are never
// rewritten (fingerprint stays stable for TOFU clients).
type CertIdentity struct {
	DNSNames []string
	IPs      []net.IP
}

// LoadOrCreateCredentials loads the serve token and a self-signed TLS
// certificate from dir, generating any that are missing. Secrets are stored
// 0600 and never land in the YAML config.
func LoadOrCreateCredentials(dir string) (*Credentials, error) {
	return loadCredentials(dir, "", "", CertIdentity{})
}

// LoadOrCreateCredentialsWithIdentity is like LoadOrCreateCredentials but
// stamps MagicDNS / bind IPs into a newly generated self-signed cert so
// browsers opening the public URL see a matching name.
func LoadOrCreateCredentialsWithIdentity(dir string, id CertIdentity) (*Credentials, error) {
	return loadCredentials(dir, "", "", id)
}

// LoadOrCreateCredentialsWithTLS loads the serve token from dir and a
// caller-supplied TLS certificate/key pair (e.g. from `tailscale cert`).
// Both certPath and keyPath are required when either is set.
func LoadOrCreateCredentialsWithTLS(dir, certPath, keyPath string) (*Credentials, error) {
	if (certPath == "") != (keyPath == "") {
		return nil, fmt.Errorf("both --tls-cert and --tls-key are required when loading an external certificate")
	}
	return loadCredentials(dir, certPath, keyPath, CertIdentity{})
}

func loadCredentials(dir, externalCert, externalKey string, id CertIdentity) (*Credentials, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create serve credentials dir: %w", err)
	}

	creds := &Credentials{Dir: dir, devices: newDeviceStore(dir)}

	tokenPath := filepath.Join(dir, tokenFile)
	tokenBytes, err := os.ReadFile(tokenPath)
	switch {
	case err == nil:
		creds.Token = strings.TrimSpace(string(tokenBytes))
	case os.IsNotExist(err):
		token, err := generateToken()
		if err != nil {
			return nil, err
		}
		creds.Token = token
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

	if err := creds.devices.load(); err != nil {
		return nil, err
	}

	certPath, keyPath := externalCert, externalKey
	if certPath == "" {
		certPath, keyPath = filepath.Join(dir, certFile), filepath.Join(dir, keyFile)
		if err := ensureSelfSignedCert(certPath, keyPath, id); err != nil {
			return nil, err
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
	creds.Pairing, err = newPairingCode()
	if err != nil {
		return nil, err
	}

	return creds, nil
}

func generateToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// newPairingCode builds a 6-digit decimal code (cryptographically random)
// valid for pairingTTL.
func newPairingCode() (*PairingCode, error) {
	return pairingCodeFrom(rand.Reader, time.Now())
}

func pairingCodeFrom(random io.Reader, now time.Time) (*PairingCode, error) {
	// Sampling with rand.Int avoids modulo bias while preserving all one
	// million six-digit values, including leading zeroes.
	n, err := rand.Int(random, big.NewInt(1_000_000))
	if err != nil {
		return nil, fmt.Errorf("generate pairing code: %w", err)
	}
	return &PairingCode{
		Code:      fmt.Sprintf("%06d", n.Int64()),
		ExpiresAt: now.Add(pairingTTL),
	}, nil
}

// RefreshPairingCode issues a new pairing code (e.g. after the previous one
// expires or is used).
func (c *Credentials) RefreshPairingCode() (*PairingCode, error) {
	c.pairingMu.Lock()
	defer c.pairingMu.Unlock()
	pairing, err := newPairingCode()
	if err != nil {
		return nil, err
	}
	c.Pairing = pairing
	return pairing, nil
}

// ExchangePairingCode validates a pairing code and returns the primary
// long-lived bearer token. The code is single-use; on success it is marked
// used. Prefer ExchangePairingCodeForDevice when the client sends a name.
func (c *Credentials) ExchangePairingCode(code string) (token string, err error) {
	if err := c.consumePairingCode(code); err != nil {
		return "", err
	}
	return c.Token, nil
}

// ExchangePairingCodeForDevice validates a pairing code and, when deviceName
// is non-empty, mints a per-device token (D2). Empty deviceName returns the
// primary shared token for back-compat with older clients.
func (c *Credentials) ExchangePairingCodeForDevice(code, deviceName string) (token string, deviceID string, err error) {
	if err := c.consumePairingCode(code); err != nil {
		return "", "", err
	}
	deviceName = strings.TrimSpace(deviceName)
	if deviceName == "" {
		return c.Token, "", nil
	}
	if c.devices == nil {
		return "", "", fmt.Errorf("device tokens unavailable without credentials directory")
	}
	rec, err := c.devices.mint(deviceName)
	if err != nil {
		return "", "", err
	}
	return rec.Token, rec.ID, nil
}

func (c *Credentials) consumePairingCode(code string) error {
	c.pairingMu.Lock()
	defer c.pairingMu.Unlock()

	code = strings.TrimSpace(code)
	if c.Pairing == nil {
		return fmt.Errorf("no pairing code available")
	}
	if c.Pairing.used {
		return fmt.Errorf("pairing code already used — restart serve to issue a new code")
	}
	if time.Now().After(c.Pairing.ExpiresAt) {
		return fmt.Errorf("pairing code expired")
	}
	if subtle.ConstantTimeCompare([]byte(code), []byte(c.Pairing.Code)) != 1 {
		return fmt.Errorf("invalid pairing code")
	}
	if !c.tokenActive() {
		return fmt.Errorf("serve token has been revoked")
	}
	c.Pairing.used = true
	return nil
}

// ListDevices returns public metadata for all paired device tokens.
func (c *Credentials) ListDevices() []DeviceInfo {
	if c.devices == nil {
		return nil
	}
	return c.devices.list()
}

// DeleteDevice revokes one paired device. ok is false when the id is unknown.
// The returned token is the revoked bearer (for stream eviction).
func (c *Credentials) DeleteDevice(id string) (token string, ok bool, err error) {
	if c.devices == nil {
		return "", false, fmt.Errorf("device tokens unavailable without credentials directory")
	}
	return c.devices.delete(id)
}

// MintDeviceToken creates a device token without pairing (tests / internal).
func (c *Credentials) MintDeviceToken(deviceName string) (*DeviceRecord, error) {
	if c.devices == nil {
		return nil, fmt.Errorf("device tokens unavailable without credentials directory")
	}
	return c.devices.mint(deviceName)
}

// RevokeTokens deletes the long-lived bearer token file and all per-device
// tokens. A running server observes primary deletion, rejects further
// controls, closes active streams, and exits; the next serve start mints a
// fresh primary token and pairing code.
func RevokeTokens(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create serve credentials dir: %w", err)
	}
	tokenPath := filepath.Join(dir, tokenFile)
	if err := os.Remove(tokenPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove token: %w", err)
	}
	// Best-effort wipe of D2 device tokens so --revoke-tokens remains global.
	store := newDeviceStore(dir)
	_ = store.load()
	if err := store.clearAll(); err != nil {
		return err
	}
	return nil
}

// tokenActive reports whether this credentials view still matches the
// authoritative on-disk token. Credentials assembled directly in tests have
// no Dir and retain the original in-memory-only behavior.
func (c *Credentials) tokenActive() bool {
	if c.Dir == "" {
		return true
	}
	tokenBytes, err := os.ReadFile(filepath.Join(c.Dir, tokenFile))
	if err != nil {
		return false
	}
	onDisk := strings.TrimSpace(string(tokenBytes))
	return subtle.ConstantTimeCompare([]byte(onDisk), []byte(c.Token)) == 1
}

// RotateToken regenerates the long-lived token in place and returns the new
// credentials view (TLS material reloaded from disk). Existing clients with
// the old token are invalidated immediately.
func RotateToken(dir string) (*Credentials, error) {
	if err := RevokeTokens(dir); err != nil {
		return nil, err
	}
	return LoadOrCreateCredentials(dir)
}

// presentedToken extracts the bearer or stream query token from r.
// Empty string means none was presented.
func presentedToken(r *http.Request) string {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if strings.HasPrefix(header, prefix) {
		return strings.TrimPrefix(header, prefix)
	}
	if r.URL.Path == "/v1/stream" {
		return r.URL.Query().Get("token")
	}
	return ""
}

// AcceptToken reports whether presented is the primary token or an active
// per-device token while the primary credentials file remains present.
func (c *Credentials) AcceptToken(presented string) bool {
	if presented == "" || !c.tokenActive() {
		return false
	}
	if constantTimeTokenMatch(presented, c.Token) {
		return true
	}
	if c.devices != nil && c.devices.acceptToken(presented) {
		return true
	}
	return false
}

// TouchToken updates last_seen for a device token (no-op for primary).
func (c *Credentials) TouchToken(presented string) {
	if c.devices == nil || presented == "" || constantTimeTokenMatch(presented, c.Token) {
		return
	}
	c.devices.touch(presented)
}

// VerifyRequest checks the Authorization bearer header on every protected
// route. Browsers cannot set custom headers on WebSocket handshakes, so the
// stream endpoint alone also accepts ?token=. Accepts primary or D2 device
// tokens while the primary token file remains active.
func (c *Credentials) VerifyRequest(r *http.Request) bool {
	presented := presentedToken(r)
	if !c.AcceptToken(presented) {
		return false
	}
	c.TouchToken(presented)
	return true
}

// ensureSelfSignedCert loads an existing self-signed pair or mints a new one.
// When id carries MagicDNS names / Tailscale IPs (serve --tailscale), an
// existing LAN-only cert (localhost + 127.0.0.1) is rewritten so browsers
// opening the MagicDNS URL do not fail hostname verification. Same-identity
// reloads keep the fingerprint stable for TOFU clients.
func ensureSelfSignedCert(certPath, keyPath string, id CertIdentity) error {
	if _, err := os.Stat(certPath); os.IsNotExist(err) {
		return generateSelfSignedCert(certPath, keyPath, id)
	}
	if !identityRequiresSANs(id) {
		// Keep the existing TOFU pair when no identity was requested.
		return nil
	}
	leaf, err := readLeafCertificate(certPath)
	if err != nil || !certSatisfiesIdentity(leaf, id) {
		return generateSelfSignedCert(certPath, keyPath, id)
	}
	return nil
}

func identityRequiresSANs(id CertIdentity) bool {
	if len(id.IPs) > 0 {
		return true
	}
	for _, name := range id.DNSNames {
		if strings.TrimSpace(strings.TrimSuffix(name, ".")) != "" {
			return true
		}
	}
	return false
}

// certSatisfiesIdentity reports whether leaf already lists every requested
// DNS name and IP. Extra SANs on the leaf are fine.
func certSatisfiesIdentity(leaf *x509.Certificate, id CertIdentity) bool {
	if leaf == nil {
		return false
	}
	haveDNS := make(map[string]struct{}, len(leaf.DNSNames))
	for _, name := range leaf.DNSNames {
		haveDNS[strings.ToLower(strings.TrimSuffix(name, "."))] = struct{}{}
	}
	for _, want := range id.DNSNames {
		want = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(want, ".")))
		if want == "" {
			continue
		}
		if _, ok := haveDNS[want]; !ok {
			return false
		}
	}
	for _, wantIP := range id.IPs {
		if wantIP == nil {
			continue
		}
		found := false
		for _, have := range leaf.IPAddresses {
			if have.Equal(wantIP) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func readLeafCertificate(certPath string) (*x509.Certificate, error) {
	raw, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	var block *pem.Block
	for {
		block, raw = pem.Decode(raw)
		if block == nil {
			return nil, fmt.Errorf("no PEM certificate in %s", certPath)
		}
		if block.Type == "CERTIFICATE" {
			break
		}
	}
	return x509.ParseCertificate(block.Bytes)
}

// generateSelfSignedCert writes a fresh ECDSA P-256 certificate for the
// trust-on-first-use model: clients pin the fingerprint at pairing time, so
// SANs are a convenience, not the trust anchor.
func generateSelfSignedCert(certPath, keyPath string, id CertIdentity) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate TLS key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("generate serial: %w", err)
	}

	hostname, _ := os.Hostname()
	dnsNames := []string{"localhost"}
	if hostname != "" {
		dnsNames = append(dnsNames, hostname)
	}
	for _, name := range id.DNSNames {
		name = strings.TrimSpace(strings.TrimSuffix(name, "."))
		if name == "" || containsString(dnsNames, name) {
			continue
		}
		dnsNames = append(dnsNames, name)
	}
	ips := []net.IP{net.ParseIP("127.0.0.1")}
	for _, ip := range id.IPs {
		if ip == nil {
			continue
		}
		ips = append(ips, ip)
	}

	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "samantha-serve"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		// ECDSA: digital signature only (KeyEncipherment is an RSA concept).
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    dnsNames,
		IPAddresses: ips,
	}

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("create certificate: %w", err)
	}

	certOut, err := os.OpenFile(certPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("write certificate: %w", err)
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		_ = certOut.Close()
		return fmt.Errorf("encode certificate: %w", err)
	}
	if err := certOut.Close(); err != nil {
		return fmt.Errorf("finalize certificate: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal TLS key: %w", err)
	}
	keyOut, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("write TLS key: %w", err)
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}); err != nil {
		_ = keyOut.Close()
		return fmt.Errorf("encode TLS key: %w", err)
	}
	if err := keyOut.Close(); err != nil {
		return fmt.Errorf("finalize TLS key: %w", err)
	}

	return nil
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
