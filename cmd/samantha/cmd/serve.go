//go:build !integration

package cmd

import (
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
	"github.com/lancekrogers/samantha/internal/session"
	"github.com/lancekrogers/samantha/internal/tts"
	"github.com/lancekrogers/samantha/internal/ui"
)

const defaultServePort = 7262 // "SAMA"

var (
	serveBind        string
	servePort        int
	serveNoVoice     bool
	serveAllowPublic bool
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Serve Samantha to your LAN over HTTPS + WebSocket",
	Long: `Run Samantha as a network-accessible instance: other devices on your
LAN (or tailnet) send text turns and stream the live conversation over
/v1/stream. Turns are text-only in serve mode — the mic stays off.

TTS always runs so clients can opt into phone-side audio with the
audio_output control message (mode "stream"). Responses also speak through
this machine's speaker unless --no-voice is set (host muted; stream clients
still hear).

Auth is mandatory: a bearer token and self-signed TLS certificate are
generated on first run. Remote turns never get tool access unless
remote_tools_enabled is set in config.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
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
	rootCmd.AddCommand(serveCmd)
}

func runServe(cfg *config.Config) error {
	ctx, cancel := signalContext()
	defer cancel()

	// Phase 3: TTS is always required so stream clients can hear responses
	// even when the host speaker is muted (--no-voice).
	req := config.AssetRequest{NeedTTS: true}
	if err := config.EnsureRuntimeAssets(ctx, cfg, req, modelProgress); err != nil {
		return fmt.Errorf("ensure runtime assets: %w", err)
	}

	bus := events.NewBus()
	ui.New(bus, cfg.AgentName) // local terminal mirrors the event stream

	// Remote turns are gated by remote_tools_enabled only. Brains' ThinkFull
	// path reads cfg.VoiceToolsEnabled, and RunTurnTextMode never passes
	// p.VoiceToolsEnabled — so clone config and align the flag before the
	// pipeline/brains are built. The local voice_tools_enabled setting must
	// not leak tool power to the network (and the reverse must not claim
	// tools are on when they are not).
	serveCfg := *cfg
	serveCfg.VoiceToolsEnabled = cfg.RemoteToolsEnabled

	// text=true: serve runs no local input loop and the mic never goes hot;
	// every turn arrives over the wire. silent=true skips buildPipeline's
	// TTS/player; we install TTS + AudioFanout ourselves for Phase 3.
	p, pipelineCleanup, err := buildPipeline(ctx, &serveCfg, bus, true, true)
	if err != nil {
		return fmt.Errorf("init pipeline: %w", err)
	}

	// Host speaker is optional; remote stream clients always get a fanout.
	var localPlayer audio.Engine
	var playerCleanup func()
	if !serveNoVoice {
		player := audio.NewPlayer()
		localPlayer = player
		playerCleanup = func() { _ = player.Close() }
	}

	ttsProvider, ttsCleanup, err := tts.NewProvider(&serveCfg)
	if err != nil {
		if playerCleanup != nil {
			playerCleanup()
		}
		pipelineCleanup()
		return fmt.Errorf("init TTS: %w", err)
	}

	fanout := netapi.NewAudioFanout(localPlayer)
	p.TTS = ttsProvider
	p.Player = fanout

	defer func() {
		_ = fanout.Close()
		if ttsCleanup != nil {
			ttsCleanup()
		}
		if playerCleanup != nil {
			playerCleanup()
		}
		pipelineCleanup()
	}()

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

	creds, err := netapi.LoadOrCreateCredentials(filepath.Join(config.ConfigDir(), "serve"))
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

	server := netapi.New(netapi.Options{
		Bind:         addr,
		AllowPublic:  serveAllowPublic,
		Credentials:  creds,
		Bus:          bus,
		Dispatcher:   dispatcher,
		Audio:        fanout,
		ListSessions: listSessionSummaries,
		Providers: netapi.Providers{
			Brain: cfg.BrainProvider,
			STT:   "", // no STT in serve mode
			TTS:   cfg.TTSProvider,
		},
		OnListening: func(bound net.Addr) {
			// Prefer the real bound address (port 0, dual-stack formatting).
			listenAddr := bound.String()
			if listenAddr == "" {
				listenAddr = addr
			}
			printServeBanner(listenAddr, creds, cfg)
		},
	})

	return server.ListenAndServe(ctx)
}

func printServeBanner(addr string, creds *netapi.Credentials, cfg *config.Config) {
	fmt.Println()
	fmt.Printf("  %s\n", titleStyle.Render("Samantha serve"))
	fmt.Printf("  %s %s\n", keyStyle.Render("Listening:"), "https://"+addr)
	fmt.Printf("  %s %s\n", keyStyle.Render("Cert SHA-256:"), creds.Fingerprint)
	if creds.TokenCreated {
		fmt.Printf("  %s %s\n", keyStyle.Render("Token:"), creds.Token)
		fmt.Println(dimStyle.Render("  Shown once — stored under " + config.ConfigDir() + "/serve/"))
	} else {
		fmt.Println(dimStyle.Render("  Token on file at " + config.ConfigDir() + "/serve/token"))
	}
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
	fmt.Println(dimStyle.Render("  Try: samantha connect " + addr + " --token <token>"))
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
