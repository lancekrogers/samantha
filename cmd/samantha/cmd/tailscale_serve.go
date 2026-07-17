//go:build !integration

package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// tailscaleSelf is the subset of `tailscale status --json` we need for serve.
type tailscaleSelf struct {
	DNSName      string   `json:"DNSName"`
	TailscaleIPs []string `json:"TailscaleIPs"`
}

type tailscaleStatusJSON struct {
	Self             tailscaleSelf `json:"Self"`
	MagicDNSSuffix   string        `json:"MagicDNSSuffix"`
	CurrentTailnet   *struct {
		MagicDNSSuffix string `json:"MagicDNSSuffix"`
	} `json:"CurrentTailnet"`
}

// tailscaleIdentity is the bind IP + public MagicDNS hostname for phone clients.
type tailscaleIdentity struct {
	IPv4     string // e.g. 100.72.165.77
	DNSName  string // e.g. mac-studio.tail37114b.ts.net (no trailing dot)
	CertFile string
	KeyFile  string
}

// discoverTailscale runs `tailscale status --json` and picks the first IPv4
// CGNAT address plus the MagicDNS name. The binary must be on PATH.
func discoverTailscale() (tailscaleIdentity, error) {
	return discoverTailscaleWith(func() ([]byte, error) {
		cmd := exec.Command("tailscale", "status", "--json")
		out, err := cmd.Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
				return nil, fmt.Errorf("tailscale status: %w (%s)", err, strings.TrimSpace(string(ee.Stderr)))
			}
			return nil, fmt.Errorf("tailscale status: %w (is the Tailscale CLI installed and logged in?)", err)
		}
		return out, nil
	})
}

func discoverTailscaleWith(run func() ([]byte, error)) (tailscaleIdentity, error) {
	raw, err := run()
	if err != nil {
		return tailscaleIdentity{}, err
	}
	var st tailscaleStatusJSON
	if err := json.Unmarshal(raw, &st); err != nil {
		return tailscaleIdentity{}, fmt.Errorf("parse tailscale status: %w", err)
	}

	dns := strings.TrimSuffix(strings.TrimSpace(st.Self.DNSName), ".")
	if dns == "" {
		return tailscaleIdentity{}, fmt.Errorf("tailscale status has no DNSName for this machine (is MagicDNS enabled?)")
	}

	var ipv4 string
	for _, ipStr := range st.Self.TailscaleIPs {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if v4 := ip.To4(); v4 != nil {
			ipv4 = v4.String()
			break
		}
	}
	if ipv4 == "" {
		return tailscaleIdentity{}, fmt.Errorf("no Tailscale IPv4 address on this node")
	}

	return tailscaleIdentity{IPv4: ipv4, DNSName: dns}, nil
}

// ensureTailscaleCert writes a Let's Encrypt cert from `tailscale cert` into
// dir (typically ~/.obey/.../serve/tls). Re-running refreshes when needed.
// certRunner is overridable in tests.
var certRunner = runTailscaleCert

func ensureTailscaleCert(dir, domain string) (certPath, keyPath string, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("create tls dir: %w", err)
	}
	// Stable names so restarts reuse paths; tailscale cert rewrites when renewing.
	safe := strings.ReplaceAll(domain, "/", "_")
	certPath = filepath.Join(dir, safe+".crt")
	keyPath = filepath.Join(dir, safe+".key")
	if err := certRunner(domain, certPath, keyPath); err != nil {
		return "", "", err
	}
	return certPath, keyPath, nil
}

func runTailscaleCert(domain, certPath, keyPath string) error {
	cmd := exec.Command("tailscale", "cert",
		"--cert-file", certPath,
		"--key-file", keyPath,
		domain,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr
	if err := cmd.Run(); err != nil {
		msg := cleanTailscaleCLIOutput(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("tailscale cert %s: %s", domain, msg)
	}
	// Restrict key perms even if the CLI left them wider.
	_ = os.Chmod(certPath, 0o600)
	_ = os.Chmod(keyPath, 0o600)
	return nil
}

// cleanTailscaleCLIOutput drops noisy version-skew warnings so the real failure
// (e.g. "account does not support getting TLS certs") is readable in the TUI.
func cleanTailscaleCLIOutput(raw string) string {
	var keep []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "warning: client version") {
			continue
		}
		keep = append(keep, line)
	}
	return strings.TrimSpace(strings.Join(keep, " "))
}

// summarizeTailscaleCertError returns a short, actionable reason for the banner
// and TUI when falling back to self-signed TLS.
func summarizeTailscaleCertError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "does not support getting tls certs"),
		strings.Contains(lower, "does not support getting tls certificates"):
		return "tailnet cannot mint HTTPS certs (enable HTTPS Certificates in admin console, or stay on self-signed)"
	case strings.Contains(lower, "https certificates"),
		strings.Contains(lower, "cert is not allowed"):
		return "HTTPS Certificates not enabled for this tailnet"
	case strings.Contains(lower, "executable file not found"),
		strings.Contains(lower, "not found in $path"):
		return "tailscale CLI not found on PATH"
	default:
		// Keep it one line for TUI width.
		msg = strings.ReplaceAll(msg, "\n", " ")
		if len(msg) > 160 {
			msg = msg[:157] + "…"
		}
		return msg
	}
}

// publicServeURL builds the URL phones should open (MagicDNS host preferred).
func publicServeURL(host string, port int) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if port <= 0 {
		port = defaultServePort
	}
	return fmt.Sprintf("https://%s/", net.JoinHostPort(host, strconv.Itoa(port)))
}
