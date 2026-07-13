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
// Returns nil, nil for loopback because advertising a LAN service that is not
// reachable from the LAN would mislead clients.
func StartDiscovery(bind string, fingerprint string, instance string) (*Discovery, error) {
	host, portStr, err := net.SplitHostPort(bind)
	if err != nil {
		return nil, fmt.Errorf("parse bind for mDNS: %w", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("parse bind port for mDNS: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsUnspecified() {
		return nil, fmt.Errorf("mDNS requires a concrete bind IP, got %q", host)
	}
	if ip.IsLoopback() {
		return nil, nil
	}
	iface, err := interfaceForIP(ip)
	if err != nil {
		return nil, err
	}
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "samantha"
	}
	if instance == "" {
		instance = hostname
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
	// RegisterProxy keeps the DNS answers aligned with the exact address the
	// HTTPS listener bound. Register would publish every address on iface.
	server, err := zeroconf.RegisterProxy(
		instance,
		ServiceType,
		"local.",
		port,
		hostname,
		[]string{ip.String()},
		txt,
		[]net.Interface{*iface},
	)
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
	if err := resolver.Browse(browseCtx, ServiceType, "local.", entries); err != nil {
		return found, err
	}
	for {
		select {
		case entry, ok := <-entries:
			if !ok {
				return found, nil
			}
			found = append(found, entry)
		case <-browseCtx.Done():
			// Drain entries already delivered before the cancellation boundary.
			for {
				select {
				case entry, ok := <-entries:
					if !ok {
						return found, nil
					}
					found = append(found, entry)
				default:
					return found, nil
				}
			}
		}
	}
}
