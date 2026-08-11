//go:build !integration

package cmd

import (
	"reflect"
	"testing"
)

func TestResolveServeBindHostsExplicit(t *testing.T) {
	orig := serveBind
	t.Cleanup(func() { serveBind = orig })

	cases := []struct {
		name string
		bind string
		want []string
	}{
		{"single host", "192.168.1.5", []string{"192.168.1.5"}},
		{"comma list", "127.0.0.1,192.168.1.5", []string{"127.0.0.1", "192.168.1.5"}},
		{"spaces trimmed", " 127.0.0.1 , 192.168.1.5 ", []string{"127.0.0.1", "192.168.1.5"}},
		{"trailing comma", "192.168.1.5,", []string{"192.168.1.5"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			serveBind = tc.bind
			if got := resolveServeBindHosts(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("resolveServeBindHosts(%q) = %v, want %v", tc.bind, got, tc.want)
			}
		})
	}
}

func TestResolveServeBindHostsDefaultIncludesLoopback(t *testing.T) {
	orig := serveBind
	t.Cleanup(func() { serveBind = orig })

	for _, bind := range []string{"", " , "} {
		serveBind = bind
		hosts := resolveServeBindHosts()
		if len(hosts) == 0 {
			t.Fatalf("bind %q: no hosts resolved", bind)
		}
		// The primary is machine-dependent (LAN, tailnet, or loopback), but
		// loopback must always be served so same-machine clients connect.
		found := false
		for _, h := range hosts {
			if h == "127.0.0.1" {
				found = true
			}
		}
		if !found {
			t.Fatalf("bind %q: hosts %v do not include loopback", bind, hosts)
		}
		if hosts[0] == "127.0.0.1" && len(hosts) != 1 {
			t.Fatalf("bind %q: loopback-only machine must not duplicate loopback: %v", bind, hosts)
		}
	}
}
