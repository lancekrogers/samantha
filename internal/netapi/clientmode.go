package netapi

// Product-facing serve banner labels shared by `samantha serve` and the TUI.
// The TUI scrapes these exact prefixes from child stdout — keep them stable.
const (
	// ClientSetupURL is the free Tailscale admin page that enables trusted
	// HTTPS so strict browsers (notably mobile Safari) can use the mic.
	ClientSetupURL = "https://login.tailscale.com/admin/dns"

	LabelOpenOnClient = "Open on client:"
	LabelPairingCode  = "Pairing code:"
	LabelNetwork      = "Network:"
	LabelClientAccess = "Client access:"
	LabelClientSetup  = "Client setup:"

	NetworkTailscale = "tailscale"
	NetworkLAN       = "lan"

	AccessFull    = "full"
	AccessLimited = "limited"
)
