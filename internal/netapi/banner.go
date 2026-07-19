package netapi

// ProtocolVersion is the integer serve reports in GET /v1/status and the
// machine-readable ready banner so clients can gate features against a stable
// contract version.
const ProtocolVersion = 1

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
}

// PairingCodeBanner is written to stdout whenever serve mints a pairing code
// (the Mac app renders a QR from it).
type PairingCodeBanner struct {
	Event     string `json:"event"` // always "pairing_code"
	Code      string `json:"code"`
	ExpiresAt string `json:"expires_at"` // RFC3339
}
