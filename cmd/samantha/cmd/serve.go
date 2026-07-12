//go:build !integration

package cmd

import (
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/brain"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/events"
	"github.com/lancekrogers/samantha/internal/netapi"
	"github.com/lancekrogers/samantha/internal/session"
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
/v1/stream. Turns are text-only in serve mode — the mic stays off — and
responses speak through this machine's speaker unless --no-voice is set.

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

	req := config.AssetRequest{NeedTTS: !serveNoVoice}
	if err := config.EnsureRuntimeAssets(ctx, cfg, req, modelProgress); err != nil {
		return fmt.Errorf("ensure runtime assets: %w", err)
	}

	bus := events.NewBus()
	ui.New(bus, cfg.AgentName) // local terminal mirrors the event stream

	// text=true: serve runs no local input loop and the mic never goes hot;
	// every turn arrives over the wire.
	p, cleanup, err := buildPipeline(ctx, cfg, bus, true, serveNoVoice)
	if err != nil {
		return fmt.Errorf("init pipeline: %w", err)
	}
	defer cleanup()

	// Remote turns are gated by the serve-scoped policy only — the local
	// voice_tools_enabled flag must not leak tool power to the network.
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
		ListSessions: listSessionSummaries,
		Providers: netapi.Providers{
			Brain: cfg.BrainProvider,
			STT:   "", // no STT in serve mode
			TTS:   serveTTSName(cfg),
		},
	})

	printServeBanner(addr, creds, cfg)
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

func serveTTSName(cfg *config.Config) string {
	if serveNoVoice {
		return ""
	}
	return cfg.TTSProvider
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

// defaultServeBind picks the machine's private LAN address, falling back to
// loopback when none is found — never a public interface by default.
func defaultServeBind() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipNet.IP.To4()
		if ip != nil && ip.IsPrivate() {
			return ip.String()
		}
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
