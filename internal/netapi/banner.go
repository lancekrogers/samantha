package netapi

// ProtocolVersion is the integer serve reports in GET /v1/status and the
// machine-readable ready banner so clients can gate features against a stable
// contract version.
//
// 2 adds the /v1/meeting capture surface (PROTOCOL_DELTAS D6). Clients should
// still prefer the per-feature capability flags in GET /v1/status over the
// version number, since serve can be built or configured without a feature.
const ProtocolVersion = 2

// ReadyBanner is the single JSON line `serve --banner-json` writes to stdout
// once the listener is bound. A supervising process reads this line to learn
// the URL, credentials, and fingerprint instead of scraping the human banner.
// Fields reflect what is real for the current mode (e.g. Tailscale) but the
// key set stays stable.
type ReadyBanner struct {
	Event           string `json:"event"` // always "ready"
	ProtocolVersion int    `json:"protocol_version"`
	URL             string `json:"url"`
	Port            int    `json:"port"`
	Fingerprint     string `json:"fingerprint"`
	Token           string `json:"token"`
	MDNS            bool   `json:"mdns"`
	Tailscale       bool   `json:"tailscale"`
	PID             int    `json:"pid"`
	// Binds lists every bound host:port, primary first. It is always present
	// on a serve that reached OnListening, including single-bind serves, so a
	// client picks a reachable address instead of inferring one from URL
	// (ADR-008). URL stays the address remote clients should open.
	Binds []string `json:"binds,omitempty"`
	// ClientSetupURL is present only in limited client-access mode (a Tailscale
	// serve that could not mint a trusted cert). Its presence means "limited";
	// its absence means full access.
	ClientSetupURL string `json:"client_setup_url,omitempty"`
}

// PairingCodeBanner is written to stdout whenever serve mints a pairing code
// (the Mac app renders a QR from it).
type PairingCodeBanner struct {
	Event     string `json:"event"` // always "pairing_code"
	Code      string `json:"code"`
	ExpiresAt string `json:"expires_at"` // RFC3339
}
