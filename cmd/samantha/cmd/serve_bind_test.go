//go:build !integration

package cmd

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/lancekrogers/samantha/internal/netapi"
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

// G35 / ADR-008: the host app is always a loopback client, so a tailnet serve
// must keep 127.0.0.1 reachable without ever promoting it above the tailnet
// address remote clients are told to open.
func TestResolveServeBindHostsTailscaleAppendsLoopback(t *testing.T) {
	withServeBindState(t)
	serveTailscale = true

	cases := []struct {
		name string
		bind string
		want []string
	}{
		// An unresolvable --bind is the caller's error to hit at listen time,
		// but it must not cost the host its loopback route on the way there.
		{"unparseable host still gains loopback", "not-a-host", []string{"not-a-host", "127.0.0.1"}},
		{"tailnet address", "100.64.0.7", []string{"100.64.0.7", "127.0.0.1"}},
		{"magicdns name", "mac-studio.tail37114b.ts.net", []string{"mac-studio.tail37114b.ts.net", "127.0.0.1"}},
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

func TestResolveServeBindHostsAlreadyLoopbackIsNotDuplicated(t *testing.T) {
	withServeBindState(t)
	serveTailscale = true

	cases := []struct {
		name string
		bind string
		want []string
	}{
		{"ipv4 loopback", "100.64.0.7,127.0.0.1", []string{"100.64.0.7", "127.0.0.1"}},
		{"other ipv4 loopback", "100.64.0.7,127.0.0.2", []string{"100.64.0.7", "127.0.0.2"}},
		{"ipv6 loopback", "100.64.0.7,::1", []string{"100.64.0.7", "::1"}},
		{"bracketed ipv6 loopback", "100.64.0.7,[::1]", []string{"100.64.0.7", "[::1]"}},
		{"hostname", "100.64.0.7,localhost", []string{"100.64.0.7", "localhost"}},
		// An unspecified bind already answers on loopback; a second listener
		// on the same port would fail to bind rather than add reachability.
		{"unspecified ipv4", "0.0.0.0", []string{"0.0.0.0"}},
		{"unspecified ipv6", "::", []string{"::"}},
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

func TestResolveServeBindHostsNonTailscaleUnchanged(t *testing.T) {
	withServeBindState(t)
	serveTailscale = false

	cases := []struct {
		name string
		bind string
		want []string
	}{
		{"explicit lan stays single", "192.168.1.5", []string{"192.168.1.5"}},
		{"explicit loopback stays single", "127.0.0.1", []string{"127.0.0.1"}},
		{"explicit list verbatim", "192.168.1.5,10.0.0.4", []string{"192.168.1.5", "10.0.0.4"}},
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

// G17: clients read binds[] to choose a reachable address, so a single-bind
// serve must still advertise one rather than making them parse url.
func TestEmitServeBannerJSONAlwaysCarriesBinds(t *testing.T) {
	withServeBindState(t)

	cases := []struct {
		name     string
		binds    []string
		wantJSON []string
	}{
		{"single bind", []string{"192.168.1.5:7262"}, []string{"192.168.1.5:7262"}},
		{"dual bind", []string{"100.64.0.7:7262", "127.0.0.1:7262"}, []string{"100.64.0.7:7262", "127.0.0.1:7262"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			banner := captureReadyBanner(t, tc.binds[0], tc.binds)
			if !reflect.DeepEqual(banner.Binds, tc.wantJSON) {
				t.Fatalf("binds = %v, want %v", banner.Binds, tc.wantJSON)
			}
		})
	}
}

// The presence of client_setup_url is the wire signal for limited access, so
// a trusted-cert serve must omit the key entirely rather than send it empty.
func TestEmitServeBannerJSONCarriesClientSetupURLOnlyInLimitedMode(t *testing.T) {
	withServeBindState(t)

	cases := []struct {
		name     string
		setupURL string
		wantKey  bool
	}{
		{"full access omits key", "", false},
		{"limited access carries key", netapi.ClientSetupURL, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			serveClientSetupURL = tc.setupURL

			raw := captureBannerLine(t, "127.0.0.1:7262", []string{"127.0.0.1:7262"})
			var keys map[string]json.RawMessage
			if err := json.Unmarshal(raw, &keys); err != nil {
				t.Fatalf("decode banner: %v", err)
			}
			if _, ok := keys["client_setup_url"]; ok != tc.wantKey {
				t.Fatalf("client_setup_url present = %v, want %v (line %s)", ok, tc.wantKey, raw)
			}

			var banner netapi.ReadyBanner
			if err := json.Unmarshal(raw, &banner); err != nil {
				t.Fatalf("decode ready banner: %v", err)
			}
			if banner.ClientSetupURL != tc.setupURL {
				t.Fatalf("client_setup_url = %q, want %q", banner.ClientSetupURL, tc.setupURL)
			}
		})
	}
}

// withServeBindState isolates the package-level serve flags this file mutates.
func withServeBindState(t *testing.T) {
	t.Helper()
	bind, tailscale := serveBind, serveTailscale
	setup, host, out := serveClientSetupURL, servePublicHost, serveBannerOut
	t.Cleanup(func() {
		serveBind, serveTailscale = bind, tailscale
		serveClientSetupURL, servePublicHost, serveBannerOut = setup, host, out
	})
	serveBind, serveTailscale = "", false
	serveClientSetupURL, servePublicHost = "", ""
}

// captureBannerLine runs the real emitter and returns the ready line it wrote.
func captureBannerLine(t *testing.T, listenAddr string, binds []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	serveBannerOut = &buf
	emitServeBannerJSON(listenAddr, binds, &netapi.Credentials{Token: "tok", Fingerprint: "9f3c"})

	line, _, found := bytes.Cut(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if !found && len(line) == 0 {
		t.Fatalf("emitServeBannerJSON wrote nothing")
	}
	return line
}

func captureReadyBanner(t *testing.T, listenAddr string, binds []string) netapi.ReadyBanner {
	t.Helper()
	var banner netapi.ReadyBanner
	if err := json.Unmarshal(captureBannerLine(t, listenAddr, binds), &banner); err != nil {
		t.Fatalf("decode ready banner: %v", err)
	}
	return banner
}
