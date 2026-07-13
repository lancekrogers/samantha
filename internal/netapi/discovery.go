package netapi

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/grandcat/zeroconf"
)

// ServiceType is the mDNS/Bonjour type advertised by samantha serve.
// Clients browse for _samantha._tcp.local.
const ServiceType = "_samantha._tcp"

// Discovery advertises the serve endpoint on the local network via mDNS.
// It never carries the bearer token or pairing code — only the port and a
// cert fingerprint hint for TOFU comparison.
type Discovery struct {
	server *zeroconf.Server
}

// StartDiscovery registers a Bonjour service for the given TCP bind address.
// host is a human-readable instance name (defaults to hostname).
// Returns nil, nil when the bind address cannot be parsed (discovery is
// best-effort and must not block serve).
func StartDiscovery(bind string, fingerprint string, instance string) (*Discovery, error) {
	host, portStr, err := net.SplitHostPort(bind)
	if err != nil {
		return nil, fmt.Errorf("parse bind for mDNS: %w", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("parse bind port for mDNS: %w", err)
	}
	if instance == "" {
		instance, _ = os.Hostname()
		if instance == "" {
			instance = "samantha"
		}
	}
	// Truncate fingerprint for TXT; full fingerprint is available over TLS
	// pair/status for clients that already connected.
	hint := fingerprint
	if len(hint) > 16 {
		hint = hint[:16]
	}
	txt := []string{
		"path=/v1/stream",
		"fp=" + hint,
	}
	// Prefer advertising on the bound interface when the host is a concrete IP.
	var ifaces []net.Interface
	if ip := net.ParseIP(host); ip != nil && !ip.IsUnspecified() && !ip.IsLoopback() {
		if iface, err := interfaceForIP(ip); err == nil && iface != nil {
			ifaces = []net.Interface{*iface}
		}
	}

	server, err := zeroconf.Register(instance, ServiceType, "local.", port, txt, ifaces)
	if err != nil {
		return nil, fmt.Errorf("mdns register: %w", err)
	}
	return &Discovery{server: server}, nil
}

// Stop unregisters the mDNS service.
func (d *Discovery) Stop() {
	if d == nil || d.server == nil {
		return
	}
	d.server.Shutdown()
}

func interfaceForIP(ip net.IP) (*net.Interface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			if ipNet.IP.Equal(ip) {
				return &iface, nil
			}
		}
	}
	return nil, fmt.Errorf("no interface for %s", ip)
}

// WaitDiscovery is a test helper: browse briefly for samantha instances.
// Production clients use platform Bonjour APIs; this is only for tests.
func WaitDiscovery(ctx context.Context, timeout time.Duration) ([]*zeroconf.ServiceEntry, error) {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return nil, err
	}
	entries := make(chan *zeroconf.ServiceEntry, 8)
	var found []*zeroconf.ServiceEntry
	browseCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	go func() {
		for e := range entries {
			found = append(found, e)
		}
	}()
	if err := resolver.Browse(browseCtx, ServiceType, "local.", entries); err != nil {
		return found, err
	}
	<-browseCtx.Done()
	return found, nil
}
