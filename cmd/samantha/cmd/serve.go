//go:build !integration

package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/brain"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/events"
	"github.com/lancekrogers/samantha/internal/netapi"
	"github.com/lancekrogers/samantha/internal/pipeline"
	"github.com/lancekrogers/samantha/internal/session"
	"github.com/lancekrogers/samantha/internal/stt"
	"github.com/lancekrogers/samantha/internal/ui"
)

const defaultServePort = 7262 // "SAMA"

var (
	serveBind         string
	servePort         int
	serveNoVoice      bool
	serveAllowPublic  bool
	serveTLSCert      string
	serveTLSKey       string
	serveNoMDNS       bool
	serveRevokeTokens bool
	serveRemoteMic    bool
	serveTailscale    bool
	serveBannerJSON   bool
	// publicHost is the hostname phones should open (MagicDNS). Empty when
	// clients should use the bind address as-is.
	servePublicHost string
	// serveTLSFallbackNote is set when --tailscale could not obtain a real
	// HTTPS cert and fell back to self-signed (still reachable over the tailnet).
	serveTLSFallbackNote string
)

// serveHumanOut receives all human-readable banner and log output. With
// --banner-json it is redirected to stderr so stdout carries only the
// machine-readable JSON event lines a supervisor parses.
var serveHumanOut io.Writer = os.Stdout

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Serve Samantha to your LAN or Tailscale over HTTPS + WebSocket",
	Long: `Run Samantha as a network-accessible instance for any device on your
LAN or Tailscale network: phones, tablets, laptops, browsers, or
samantha connect. Clients send text or push-to-talk audio and stream the
live conversation over /v1/stream.

Quick paths:

  samantha serve --tailscale
      Remote access over Tailscale (anywhere on your tailnet): bind the 100.x
      address, print a link + pairing code, mute the host speaker. Prefers a
      trusted cert so every browser can use the mic; if that is unavailable,
      starts in limited mode (most desktop browsers work after one warning;
      the UI prints a free setup link for full mic support).

  samantha serve
      Same Wi‑Fi / LAN (auto-detected private bind). Same client page and
      pairing flow. Prefer --tailscale when devices are not on the same
      network, or pass --tls-cert/--tls-key for trusted HTTPS on the LAN.

Embedded voice page: open the link → enter code → Pair → Start → Hold to Talk.
Protocol details: docs/serve-protocol.md.

Local full voice (mic + speakers on this machine) is still "samantha" TUI —
serve is for remote clients, not a replacement for the conversation TUI.
SSH/Termius into the host still uses the local TUI audio path; remote mic and
speakers require the HTTPS voice page or samantha connect.

Auth is mandatory. --revoke-tokens invalidates the bearer. mDNS advertises
_samantha._tcp on the LAN (--no-mdns to disable; --tailscale implies
--no-mdns because MagicDNS is the discovery). Remote tools stay off unless
remote_tools_enabled is set.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if serveBannerJSON {
			serveHumanOut = os.Stderr
		}
		if serveRevokeTokens {
			return runRevokeTokens()
		}
		if err := applyServeTailscaleDefaults(cmd); err != nil {
			return err
		}
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		return runServe(cfg)
	},
}

func init() {
	serveCmd.Flags().StringVar(&serveBind, "bind", "", "IP to bind (default: auto-detected private LAN address)")
	serveCmd.Flags().IntVar(&servePort, "port", defaultServePort, "Port to listen on")
	serveCmd.Flags().BoolVar(&serveNoVoice, "no-voice", false, "Do not speak responses through the local speaker")
	serveCmd.Flags().BoolVar(&serveAllowPublic, "allow-public", false, "Allow binding a non-private interface (dangerous)")
	serveCmd.Flags().StringVar(&serveTLSCert, "tls-cert", "", "TLS certificate PEM (e.g. from tailscale cert)")
	serveCmd.Flags().StringVar(&serveTLSKey, "tls-key", "", "TLS private key PEM (e.g. from tailscale cert)")
	serveCmd.Flags().BoolVar(&serveNoMDNS, "no-mdns", false, "Do not advertise via mDNS/Bonjour")
	serveCmd.Flags().BoolVar(&serveRevokeTokens, "revoke-tokens", false, "Revoke the serve bearer token, stop a running server, and exit")
	serveCmd.Flags().BoolVar(&serveRemoteMic, "remote-mic", true, "Enable remote push-to-talk (client STT over the WebSocket)")
	serveCmd.Flags().BoolVar(&serveTailscale, "tailscale", false, "Tailscale remote mode: 100.x bind, MagicDNS URL, prefer trusted cert (self-signed fallback), host speaker muted unless --no-voice=false")
	serveCmd.Flags().BoolVar(&serveBannerJSON, "banner-json", false, "Emit machine-readable JSON event lines on stdout (ready, pairing_code) and route human logs to stderr")
	rootCmd.AddCommand(serveCmd)
}

// applyServeTailscaleDefaults configures bind/TLS/voice when --tailscale is set.
// Explicit flags still win (bind, tls-cert, no-voice when Changed).
//
// A missing or failed `tailscale cert` no longer aborts serve: free / restricted
// tailnets often cannot mint Let's Encrypt material. We fall back to the same
// self-signed TOFU path as LAN serve so any device on the tailnet can still
// reach MagicDNS (browsers accept the warning; samantha connect pins the cert).
func applyServeTailscaleDefaults(cmd *cobra.Command) error {
	if !serveTailscale {
		return nil
	}
	serveTLSFallbackNote = ""

	id, err := discoverTailscale()
	if err != nil {
		return err
	}
	servePublicHost = id.DNSName

	if !cmd.Flags().Changed("bind") || serveBind == "" {
		serveBind = id.IPv4
	}
	// MagicDNS replaces LAN mDNS for remote clients on the tailnet.
	if !cmd.Flags().Changed("no-mdns") {
		serveNoMDNS = true
	}
	// Prefer remote-only audio: mute host speaker unless the user opted out.
	if !cmd.Flags().Changed("no-voice") {
		serveNoVoice = true
	}

	if serveTLSCert == "" && serveTLSKey == "" {
		tlsDir := filepath.Join(config.ConfigDir(), "serve", "tls")
		cert, key, err := ensureTailscaleCert(tlsDir, id.DNSName)
		if err != nil {
			serveTLSFallbackNote = summarizeTailscaleCertError(err)
			// Stable labels for the TUI (and readable for CLI users).
			// Product outcomes only — any device, not iOS-specific.
			fmt.Fprintf(serveHumanOut, "  %s %s\n", failStyle.Render(netapi.LabelClientAccess), netapi.AccessLimited)
			fmt.Fprintf(serveHumanOut, "  %s %s\n", keyStyle.Render(netapi.LabelClientSetup), netapi.ClientSetupURL)
			fmt.Fprintln(serveHumanOut, dimStyle.Render("  Most desktop browsers work after one warning. Full mic support on"))
			fmt.Fprintln(serveHumanOut, dimStyle.Render("  every device: open Client setup → turn on HTTPS Certificates → restart."))
			if serveTLSFallbackNote != "" {
				fmt.Fprintf(serveHumanOut, "  %s %s\n", dimStyle.Render("Detail:"), serveTLSFallbackNote)
			}
		} else {
			serveTLSCert, serveTLSKey = cert, key
			fmt.Fprintf(serveHumanOut, "  %s %s\n", keyStyle.Render(netapi.LabelClientAccess), netapi.AccessFull)
		}
		fmt.Fprintf(serveHumanOut, "  %s %s\n", keyStyle.Render(netapi.LabelNetwork), netapi.NetworkTailscale)
		fmt.Fprintf(serveHumanOut, "  %s %s\n", keyStyle.Render("Network name:"), id.DNSName)
		fmt.Fprintf(serveHumanOut, "  %s %s\n", dimStyle.Render("Bind:"), serveBind)
	}
	return nil
}

func runRevokeTokens() error {
	dir := filepath.Join(config.ConfigDir(), "serve")
	if err := netapi.RevokeTokens(dir); err != nil {
		return err
	}
	fmt.Fprintf(serveHumanOut, "  Revoked serve token under %s\n", dir)
	fmt.Fprintln(serveHumanOut, dimStyle.Render("  Any running serve instance using that token will stop."))
	fmt.Fprintln(serveHumanOut, dimStyle.Render("  Next `samantha serve` will mint a new token (and pairing code)."))
	return nil
}

func runServe(cfg *config.Config) error {
	ctx, cancel := signalContext()
	defer cancel()

	// Phase 3: TTS always required for stream clients. Phase 4 remote mic
	// also needs STT + VAD models when --remote-mic is set (default on).
	req := config.AssetRequest{
		NeedTTS: true,
		NeedSTT: serveRemoteMic,
		NeedVAD: serveRemoteMic,
	}
	if err := config.EnsureRuntimeAssets(ctx, cfg, req, serveModelProgress); err != nil {
		return fmt.Errorf("ensure runtime assets: %w", err)
	}

	bus := events.NewBus()
	ui.New(bus, cfg.AgentName) // local terminal mirrors the event stream

	// Remote turns are gated by remote_tools_enabled only. The pipeline is
	// the sole runtime tools gate (ThinkStream and ThinkFull both take
	// StreamOptions.ToolsEnabled from p.VoiceToolsEnabled), so clone config
	// and set the flag before building brains. Local voice_tools_enabled
	// must not leak tool power to the network.
	serveCfg := *cfg
	serveCfg.VoiceToolsEnabled = cfg.RemoteToolsEnabled

	p, fanout, ingress, cleanup, err := buildServePipeline(ctx, &serveCfg, bus, serveNoVoice, serveRemoteMic)
	if err != nil {
		return fmt.Errorf("init serve pipeline: %w", err)
	}
	defer cleanup()

	// Keep pipeline streaming options in lockstep with the serve policy.
	p.VoiceToolsEnabled = cfg.RemoteToolsEnabled

	if w, ok := p.Brain.(brain.Warmer); ok {
		go w.Warmup(ctx)
	}

	ref := &sessionRef{sess: session.New(cfg.BrainProvider, serveModelName(cfg))}
	p.OnTurn = func() {
		if err := ref.save(p.Brain.History()); err != nil {
			bus.Emit(events.Error{Stage: "session", Message: fmt.Sprintf("save session: %v", err)})
		}
	}
	defer func() {
		if err := ref.save(p.Brain.History()); err != nil {
			fmt.Fprintf(serveHumanOut, "  warning: failed to save session: %v\n", err)
		}
	}()

	credsDir := filepath.Join(config.ConfigDir(), "serve")
	var creds *netapi.Credentials
	if serveTLSCert != "" || serveTLSKey != "" {
		creds, err = netapi.LoadOrCreateCredentialsWithTLS(credsDir, serveTLSCert, serveTLSKey)
	} else {
		// Stamp MagicDNS and/or the real bind IP into the self-signed cert so
		// browsers opening the printed URL pass hostname checks on LAN and
		// Tailscale (TOFU still pins the fingerprint).
		id := netapi.CertIdentity{}
		if servePublicHost != "" {
			id.DNSNames = append(id.DNSNames, servePublicHost)
		}
		bindForSAN := serveBind
		if bindForSAN == "" {
			bindForSAN = defaultServeBind()
		}
		if ip := net.ParseIP(bindForSAN); ip != nil {
			id.IPs = append(id.IPs, ip)
		}
		if len(id.DNSNames) > 0 || len(id.IPs) > 0 {
			creds, err = netapi.LoadOrCreateCredentialsWithIdentity(credsDir, id)
		} else {
			creds, err = netapi.LoadOrCreateCredentials(credsDir)
		}
	}
	if err != nil {
		return err
	}

	dispatcher := netapi.NewDispatcher(p, bus, func() { p.Brain.ClearHistory() }, func(id string) error {
		sess, err := session.Load(id)
		if err != nil {
			return fmt.Errorf("load session: %w", err)
		}
		p.Brain.LoadHistory(sess.Turns)
		ref.swap(sess)
		return nil
	})
	go dispatcher.Run(ctx)

	bind := serveBind
	if bind == "" {
		bind = defaultServeBind()
	}
	addr := net.JoinHostPort(bind, strconv.Itoa(servePort))

	sttName := ""
	if ingress != nil {
		sttName = cfg.STTProvider
	}
	server := netapi.New(netapi.Options{
		Bind:         addr,
		AllowPublic:  serveAllowPublic,
		Credentials:  creds,
		Bus:          bus,
		Dispatcher:   dispatcher,
		Audio:        fanout,
		Ingress:      ingress,
		ListSessions: listSessionSummaries,
		Providers: netapi.Providers{
			Brain: cfg.BrainProvider,
			STT:   sttName,
			TTS:   cfg.TTSProvider,
		},
		OnListening: func(bound net.Addr) {
			// Prefer the real bound address (port 0, dual-stack formatting).
			listenAddr := bound.String()
			if listenAddr == "" {
				listenAddr = addr
			}
			if serveBannerJSON {
				emitServeBannerJSON(listenAddr, creds)
			} else {
				printServeBanner(listenAddr, creds, cfg)
			}
			if !serveNoMDNS {
				if disc, err := netapi.StartDiscovery(listenAddr, creds.Fingerprint, cfg.AgentName); err != nil {
					fmt.Fprintf(serveHumanOut, "  warning: mDNS advertise failed: %v\n", err)
				} else if disc != nil {
					go func() {
						<-ctx.Done()
						disc.Stop()
					}()
					fmt.Fprintln(serveHumanOut, dimStyle.Render("  mDNS: advertising "+netapi.ServiceType+" (disable with --no-mdns)"))
				}
			}
		},
	})

	return server.ListenAndServe(ctx)
}

// buildServePipeline constructs the serve-mode pipeline: text turns always,
// optional remote-mic STT on an audio.Ingress (no host capture device),
// always-on TTS for stream clients, and an AudioFanout player.
// muteHost skips the local speaker but still synthesizes for the wire.
//
// Fanout always owns the local player (if any) so cleanup is single-owner —
// no double Close between serve and buildPipeline.
func buildServePipeline(ctx context.Context, cfg *config.Config, bus *events.Bus, muteHost, remoteMic bool) (*pipeline.Pipeline, *netapi.AudioFanout, *audio.Ingress, func(), error) {
	// Brain only first (text=true, silent=true): no host mic, no default TTS/player.
	p, baseCleanup, err := buildPipeline(ctx, cfg, bus, true, true)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	var cleanups []func()
	cleanups = append(cleanups, baseCleanup)
	cleanup := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}
	fail := func(err error) (*pipeline.Pipeline, *netapi.AudioFanout, *audio.Ingress, func(), error) {
		cleanup()
		return nil, nil, nil, nil, err
	}

	ttsSet, err := newTTSProviderSet(cfg)
	if err != nil {
		return fail(fmt.Errorf("init TTS: %w", err))
	}
	cleanups = append(cleanups, ttsSet.Close)
	p.ReplaceTTS(ttsSet.Primary, ttsSet.Fallback)
	if ttsSet.FallbackWarning != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", ttsSet.FallbackWarning)
	}

	var local audio.Engine
	if !muteHost {
		local = audio.NewPlayerWithDevice(cfg.OutputDevice)
	}
	// Fanout owns local so Close is exactly once via cleanup.
	fanout := netapi.NewOwnedAudioFanout(local)
	cleanups = append(cleanups, func() { _ = fanout.Close() })
	p.Player = fanout

	var ingress *audio.Ingress
	if remoteMic {
		// Remote push-to-talk: STT reads from network PCM, not the host mic.
		// Force VAD so sherpa/whisper paths can build (config may leave it off).
		serveSTTCfg := *cfg
		if !serveSTTCfg.VADEnabled {
			serveSTTCfg.VADEnabled = true
		}
		ingress = audio.NewIngress()
		cleanups = append(cleanups, func() { _ = ingress.Close() })

		vad, err := audio.NewVAD(&serveSTTCfg)
		if err != nil {
			return fail(fmt.Errorf("init VAD for remote mic: %w", err))
		}
		cleanups = append(cleanups, vad.Delete)

		sttProvider, sttCleanup, err := stt.NewProvider(&serveSTTCfg, ingress, vad)
		if err != nil {
			return fail(fmt.Errorf("init STT for remote mic: %w", err))
		}
		if sttCleanup != nil {
			cleanups = append(cleanups, sttCleanup)
		}
		p.STT = sttProvider
		p.VAD = vad
		// Capture stays nil: barge-in on host mic is not used in serve mode.
	}

	return p, fanout, ingress, cleanup, nil
}

func printServeBanner(addr string, creds *netapi.Credentials, cfg *config.Config) {
	fmt.Println()
	fmt.Printf("  %s\n", titleStyle.Render("Use on another device"))

	// Prefer MagicDNS (or other public host) for the client-facing URL.
	pageHost := servePublicHost
	if pageHost == "" {
		if host, _, err := net.SplitHostPort(addr); err == nil {
			pageHost = host
		} else {
			pageHost = addr
		}
	}
	pageURL := publicServeURL(pageHost, servePort)
	fmt.Printf("  %s %s\n", keyStyle.Render(netapi.LabelOpenOnClient), pageURL)

	if serveTailscale {
		fmt.Printf("  %s %s\n", keyStyle.Render(netapi.LabelNetwork), netapi.NetworkTailscale)
	} else {
		fmt.Printf("  %s %s\n", keyStyle.Render(netapi.LabelNetwork), netapi.NetworkLAN)
	}

	// Product-facing access mode (TUI parses these labels via netapi constants).
	// Trusted certs → full voice mic in any browser.
	// Self-signed → page works after a warning; some mobile browsers block mic.
	if creds.ExternalTLS {
		fmt.Printf("  %s %s\n", keyStyle.Render(netapi.LabelClientAccess), netapi.AccessFull)
		fmt.Println(dimStyle.Render("  Any device on this network can use text and the microphone."))
	} else {
		fmt.Printf("  %s %s\n", failStyle.Render(netapi.LabelClientAccess), netapi.AccessLimited)
		if serveTailscale || servePublicHost != "" {
			fmt.Printf("  %s %s\n", keyStyle.Render(netapi.LabelClientSetup), netapi.ClientSetupURL)
			fmt.Println(dimStyle.Render("  Works now: most desktop browsers (accept one warning) + text on any device."))
			fmt.Println(dimStyle.Render("  Full mic on every browser: Client setup → HTTPS Certificates → restart."))
		} else {
			fmt.Println(dimStyle.Render("  Works now: most desktop browsers (accept one warning) + text on any device."))
			fmt.Println(dimStyle.Render("  Full mic on every browser: samantha serve --tailscale (with HTTPS"))
			fmt.Println(dimStyle.Render("  Certificates on), or pass --tls-cert/--tls-key."))
		}
	}

	if creds.Pairing != nil {
		fmt.Printf("  %s %s  (expires %s)\n",
			keyStyle.Render(netapi.LabelPairingCode),
			creds.Pairing.Code,
			creds.Pairing.ExpiresAt.Format("15:04:05"))
		fmt.Println(dimStyle.Render("  On the device: open link → enter code → Pair → Start → Hold to Talk"))
		fmt.Println(dimStyle.Render("  The code is single-use; restart serve to issue another."))
	}

	// Secondary / power-user lines stay dim and last.
	fmt.Printf("  %s %s\n", dimStyle.Render("Listening:"), "https://"+addr)
	if creds.ExternalTLS {
		fp := creds.Fingerprint
		if len(fp) > 16 {
			fp = fp[:16] + "…"
		}
		fmt.Printf("  %s %s\n", dimStyle.Render("TLS:"), "trusted ("+fp+")")
	} else {
		fmt.Printf("  %s %s\n", dimStyle.Render("TLS:"), "browser warning (self-signed)")
		if serveTLSFallbackNote != "" {
			fmt.Printf("  %s %s\n", dimStyle.Render("Detail:"), serveTLSFallbackNote)
		}
	}
	if creds.TokenCreated {
		fmt.Printf("  %s %s\n", keyStyle.Render("Token:"), creds.Token)
		fmt.Println(dimStyle.Render("  Shown once — stored under " + config.ConfigDir() + "/serve/"))
	} else {
		fmt.Println(dimStyle.Render("  Token on file at " + config.ConfigDir() + "/serve/token"))
	}
	fmt.Println(dimStyle.Render("  Revoke: samantha serve --revoke-tokens"))
	if cfg.RemoteToolsEnabled {
		fmt.Println(failStyle.Render("  WARNING: remote_tools_enabled=true — network clients can trigger tool calls on this machine."))
	} else {
		fmt.Println(dimStyle.Render("  Remote tool calls: disabled (remote_tools_enabled=false)"))
	}
	if serveNoVoice {
		fmt.Println(dimStyle.Render("  Host speaker: muted (--no-voice); stream clients can still request audio_output"))
	} else {
		fmt.Println(dimStyle.Render("  Host speaker: on; clients opt in with {\"type\":\"audio_output\",\"mode\":\"stream\"}"))
	}
	if serveRemoteMic {
		fmt.Println(dimStyle.Render("  Remote mic: push-to-talk enabled (hold Talk on the voice page)"))
	} else {
		fmt.Println(dimStyle.Render("  Remote mic: off (--remote-mic=false)"))
	}
	if serveTailscale {
		fmt.Println(dimStyle.Render("  Mode: --tailscale (shared Tailscale network; host speaker muted unless --no-voice=false)"))
	} else {
		fmt.Println(dimStyle.Render("  Mode: LAN / same network (mDNS when enabled)"))
	}
	fmt.Println(dimStyle.Render("  Debug: samantha connect " + addr + " --token <token>"))
	fmt.Println(dimStyle.Render("  Local full voice on this machine: run `samantha` (TUI), not serve"))
	fmt.Println()
}

// emitServeBannerJSON writes the machine-readable ready line (and a
// pairing_code line when a code is minted) to stdout, one JSON object per
// line. The supervising process reads the ready line to learn the URL,
// credentials, and fingerprint instead of scraping the human banner.
func emitServeBannerJSON(listenAddr string, creds *netapi.Credentials) {
	host, port := hostPortFromAddr(listenAddr)
	pageHost := servePublicHost
	if pageHost == "" {
		pageHost = host
	}

	emitBannerLine(netapi.ReadyBanner{
		Event:           "ready",
		ProtocolVersion: netapi.ProtocolVersion,
		URL:             "https://" + net.JoinHostPort(pageHost, strconv.Itoa(port)),
		Port:            port,
		Fingerprint:     creds.Fingerprint,
		Token:           creds.Token,
		MDNS:            !serveNoMDNS,
		Tailscale:       serveTailscale,
		PID:             os.Getpid(),
	})

	if creds.Pairing != nil {
		emitBannerLine(netapi.PairingCodeBanner{
			Event:     "pairing_code",
			Code:      creds.Pairing.Code,
			ExpiresAt: creds.Pairing.ExpiresAt.Format(time.RFC3339),
		})
	}
}

// hostPortFromAddr splits a host:port listen address, falling back to the
// configured serve port when the address carries no parseable port.
func hostPortFromAddr(addr string) (host string, port int) {
	port = servePort
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, port
	}
	if n, err := strconv.Atoi(p); err == nil {
		port = n
	}
	return h, port
}

func emitBannerLine(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  warning: encode serve banner: %v\n", err)
		return
	}
	fmt.Fprintf(os.Stdout, "%s\n", data)
}

// serveModelProgress mirrors modelProgress but writes to serveHumanOut so
// asset-download output never pollutes stdout under --banner-json.
var serveLastProgressPct int

func serveModelProgress(name string, pct float64) {
	iPct := int(pct)
	if pct == 0 {
		serveLastProgressPct = -1
		fmt.Fprintf(serveHumanOut, "  Downloading %s...\n", name)
		return
	}
	if iPct != serveLastProgressPct {
		serveLastProgressPct = iPct
		fmt.Fprintf(serveHumanOut, "\r  %s: %d%%", name, iPct)
		if iPct >= 100 {
			fmt.Fprintln(serveHumanOut)
		}
	}
}

func serveModelName(cfg *config.Config) string {
	switch cfg.BrainProvider {
	case "claude":
		return "claude"
	case "grok":
		if cfg.GrokModel != "" {
			return cfg.GrokModel
		}
		return "grok"
	default:
		return cfg.OllamaModel
	}
}

func listSessionSummaries() []netapi.SessionSummary {
	sessions := session.List()
	out := make([]netapi.SessionSummary, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, netapi.SessionSummary{
			ID:        s.ID,
			Summary:   s.Summary,
			Turns:     len(s.Turns),
			UpdatedAt: s.UpdatedAt,
		})
	}
	return out
}

// defaultServeBind picks the machine's private LAN or tailnet (CGNAT) address,
// falling back to loopback when none is found — never a public interface by
// default.
func defaultServeBind() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	var tailscale string
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipNet.IP.To4()
		if ip == nil {
			continue
		}
		if ip.IsPrivate() {
			return ip.String()
		}
		// Prefer RFC1918 when present; remember a Tailscale/CGNAT address as
		// a secondary choice for machines that only have a tailnet IP.
		if tailscale == "" && netapi.IsTrustedServeIP(ip) && !ip.IsLoopback() {
			tailscale = ip.String()
		}
	}
	if tailscale != "" {
		return tailscale
	}
	return "127.0.0.1"
}

// sessionRef holds the session remote turns save into; resume swaps it while
// the dispatcher guarantees no turn is mid-flight.
type sessionRef struct {
	mu   sync.Mutex
	sess *session.Session
}

func (r *sessionRef) save(turns []brain.Turn) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sess.Save(turns)
}

func (r *sessionRef) swap(sess *session.Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sess = sess
}
