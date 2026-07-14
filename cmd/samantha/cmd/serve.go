//go:build !integration

package cmd

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/brain"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/events"
	"github.com/lancekrogers/samantha/internal/netapi"
	"github.com/lancekrogers/samantha/internal/pipeline"
	"github.com/lancekrogers/samantha/internal/session"
	"github.com/lancekrogers/samantha/internal/stt"
	"github.com/lancekrogers/samantha/internal/tts"
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
	// publicHost is the hostname phones should open (MagicDNS). Empty when
	// clients should use the bind address as-is.
	servePublicHost string
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Serve Samantha to your LAN or Tailscale over HTTPS + WebSocket",
	Long: `Run Samantha as a network-accessible instance: phones and other devices
send text or push-to-talk audio and stream the live conversation over
/v1/stream.

Quick paths:

  samantha serve --tailscale
      One-shot remote voice for Tailscale: bind the 100.x address, obtain a
      real HTTPS cert via "tailscale cert", mute the host speaker by default,
      and print the MagicDNS URL + pairing code for your phone.

  samantha serve
      LAN (or auto-detected private bind). Self-signed TLS by default; pass
      --tls-cert/--tls-key for a real cert (required for iOS Safari mic/audio).

Embedded phone page: https://<host>:<port>/ — pair with the short code (or
paste the bearer token), tap Start, hold Talk. Protocol details:
docs/serve-protocol.md.

Local full voice (mic + speakers on this machine) is still "samantha" TUI —
serve is for remote clients, not a replacement for the conversation TUI.

Auth is mandatory. --revoke-tokens invalidates the bearer. mDNS advertises
_samantha._tcp on the LAN (--no-mdns to disable; --tailscale implies
--no-mdns because MagicDNS is the discovery). Remote tools stay off unless
remote_tools_enabled is set.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
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
	serveCmd.Flags().BoolVar(&serveRemoteMic, "remote-mic", true, "Enable phone push-to-talk (remote STT over the WebSocket)")
	serveCmd.Flags().BoolVar(&serveTailscale, "tailscale", false, "One-shot Tailscale mode: 100.x bind, tailscale cert, MagicDNS URL, host speaker muted unless --no-voice=false")
	rootCmd.AddCommand(serveCmd)
}

// applyServeTailscaleDefaults configures bind/TLS/voice when --tailscale is set.
// Explicit flags still win (bind, tls-cert, no-voice when Changed).
func applyServeTailscaleDefaults(cmd *cobra.Command) error {
	if !serveTailscale {
		return nil
	}

	id, err := discoverTailscale()
	if err != nil {
		return err
	}
	servePublicHost = id.DNSName

	if !cmd.Flags().Changed("bind") || serveBind == "" {
		serveBind = id.IPv4
	}
	// MagicDNS replaces LAN mDNS for phone clients on the tailnet.
	if !cmd.Flags().Changed("no-mdns") {
		serveNoMDNS = true
	}
	// Prefer phone-only audio: mute host speaker unless the user opted out.
	if !cmd.Flags().Changed("no-voice") {
		serveNoVoice = true
	}

	if serveTLSCert == "" && serveTLSKey == "" {
		tlsDir := filepath.Join(config.ConfigDir(), "serve", "tls")
		cert, key, err := ensureTailscaleCert(tlsDir, id.DNSName)
		if err != nil {
			return err
		}
		serveTLSCert, serveTLSKey = cert, key
		fmt.Printf("  %s %s\n", keyStyle.Render("Tailscale cert:"), cert)
		fmt.Printf("  %s %s\n", keyStyle.Render("MagicDNS:"), id.DNSName)
		fmt.Printf("  %s %s\n", keyStyle.Render("Bind:"), serveBind)
	}
	return nil
}

func runRevokeTokens() error {
	dir := filepath.Join(config.ConfigDir(), "serve")
	if err := netapi.RevokeTokens(dir); err != nil {
		return err
	}
	fmt.Printf("  Revoked serve token under %s\n", dir)
	fmt.Println(dimStyle.Render("  Any running serve instance using that token will stop."))
	fmt.Println(dimStyle.Render("  Next `samantha serve` will mint a new token (and pairing code)."))
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
	if err := config.EnsureRuntimeAssets(ctx, cfg, req, modelProgress); err != nil {
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
			fmt.Printf("  warning: failed to save session: %v\n", err)
		}
	}()

	credsDir := filepath.Join(config.ConfigDir(), "serve")
	var creds *netapi.Credentials
	if serveTLSCert != "" || serveTLSKey != "" {
		creds, err = netapi.LoadOrCreateCredentialsWithTLS(credsDir, serveTLSCert, serveTLSKey)
	} else {
		creds, err = netapi.LoadOrCreateCredentials(credsDir)
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
			printServeBanner(listenAddr, creds, cfg)
			if !serveNoMDNS {
				if disc, err := netapi.StartDiscovery(listenAddr, creds.Fingerprint, cfg.AgentName); err != nil {
					fmt.Printf("  warning: mDNS advertise failed: %v\n", err)
				} else if disc != nil {
					go func() {
						<-ctx.Done()
						disc.Stop()
					}()
					fmt.Println(dimStyle.Render("  mDNS: advertising " + netapi.ServiceType + " (disable with --no-mdns)"))
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

	ttsProvider, ttsCleanup, err := tts.NewProvider(cfg)
	if err != nil {
		return fail(fmt.Errorf("init TTS: %w", err))
	}
	if ttsCleanup != nil {
		cleanups = append(cleanups, ttsCleanup)
	}
	p.TTS = ttsProvider

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
	fmt.Printf("  %s\n", titleStyle.Render("Samantha serve"))
	fmt.Printf("  %s %s\n", keyStyle.Render("Listening:"), "https://"+addr)

	// Prefer MagicDNS (or other public host) for the phone-facing URL.
	pageHost := servePublicHost
	if pageHost == "" {
		if host, _, err := net.SplitHostPort(addr); err == nil {
			pageHost = host
		} else {
			pageHost = addr
		}
	}
	pageURL := publicServeURL(pageHost, servePort)
	fmt.Printf("  %s %s\n", keyStyle.Render("Open on phone:"), pageURL)

	if creds.ExternalTLS {
		fp := creds.Fingerprint
		if len(fp) > 16 {
			fp = fp[:16] + "…"
		}
		fmt.Printf("  %s %s\n", keyStyle.Render("TLS:"), "external cert ("+fp+")")
	} else {
		fmt.Printf("  %s %s\n", keyStyle.Render("Cert SHA-256:"), creds.Fingerprint)
		fmt.Println(dimStyle.Render("  Self-signed — for iOS Safari use: samantha serve --tailscale"))
	}
	if creds.TokenCreated {
		fmt.Printf("  %s %s\n", keyStyle.Render("Token:"), creds.Token)
		fmt.Println(dimStyle.Render("  Shown once — stored under " + config.ConfigDir() + "/serve/"))
	} else {
		fmt.Println(dimStyle.Render("  Token on file at " + config.ConfigDir() + "/serve/token"))
	}
	if creds.Pairing != nil {
		fmt.Printf("  %s %s  (expires %s)\n",
			keyStyle.Render("Pairing code:"),
			creds.Pairing.Code,
			creds.Pairing.ExpiresAt.Format("15:04:05"))
		fmt.Println(dimStyle.Render("  Phone: open the URL → enter code → Pair → Start → Hold to Talk"))
		fmt.Println(dimStyle.Render("  The code is single-use; restart serve to issue another."))
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
		fmt.Println(dimStyle.Render("  Mode: --tailscale (MagicDNS URL above; host speaker muted unless --no-voice=false)"))
	}
	fmt.Println(dimStyle.Render("  Debug: samantha connect " + addr + " --token <token>"))
	fmt.Println(dimStyle.Render("  Local full voice on this machine: run `samantha` (TUI), not serve"))
	fmt.Println()
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
