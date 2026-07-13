//go:build !integration

package cmd

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/coder/websocket"
	"github.com/spf13/cobra"
)

var (
	connectToken       string
	connectFingerprint string
	connectAudioOut    string
)

var connectCmd = &cobra.Command{
	Use:   "connect <host:port>",
	Short: "Debug REPL against a samantha serve instance",
	Long: `Connect to a running "samantha serve" over its WebSocket protocol:
type lines to send text turns, watch the live event stream come back.
Commands: /interrupt, /clear, /audio on, /audio off, /quit.

/audio on sends {"type":"audio_output","mode":"stream"} so the server pushes
base64 PCM audio_chunk envelopes (Phase 3). With --audio-out FILE, raw
pcm_s16le bytes are appended to FILE (and stream mode is enabled on connect)
so you can verify the wire format, e.g.:

  samantha connect host:7262 --token … --audio-out /tmp/out.raw
  ffplay -f s16le -ar 24000 -ac 1 /tmp/out.raw

Pin the server with --fingerprint (printed by serve at startup); without it
the certificate is accepted blindly and its fingerprint printed so you can
pin on the next run.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConnect(args[0])
	},
}

func init() {
	connectCmd.Flags().StringVar(&connectToken, "token", "", "Bearer token issued by samantha serve (required)")
	connectCmd.Flags().StringVar(&connectFingerprint, "fingerprint", "", "Pin the server's certificate SHA-256 (hex)")
	connectCmd.Flags().StringVar(&connectAudioOut, "audio-out", "", "Append raw pcm_s16le audio_chunk payloads to this file")
	_ = connectCmd.MarkFlagRequired("token")
	rootCmd.AddCommand(connectCmd)
}

func runConnect(addr string) error {
	ctx, cancel := signalContext()
	defer cancel()

	tlsConfig := &tls.Config{
		// Self-signed LAN cert: trust is fingerprint pinning, not a CA.
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return fmt.Errorf("server presented no certificate")
			}
			sum := sha256.Sum256(rawCerts[0])
			got := hex.EncodeToString(sum[:])
			if connectFingerprint == "" {
				fmt.Printf("  %s %s\n", dimStyle.Render("Server cert SHA-256 (unpinned — pass --fingerprint to pin):"), got)
				return nil
			}
			if !strings.EqualFold(connectFingerprint, got) {
				return fmt.Errorf("certificate fingerprint mismatch: pinned %s, server presented %s", connectFingerprint, got)
			}
			return nil
		},
	}

	header := http.Header{}
	header.Set("Authorization", "Bearer "+connectToken)
	ws, _, err := websocket.Dial(ctx, "wss://"+addr+"/v1/stream", &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig}},
		HTTPHeader: header,
	})
	if err != nil {
		return fmt.Errorf("connect to %s: %w", addr, err)
	}
	defer ws.Close(websocket.StatusNormalClosure, "")

	var audioFile *os.File
	if connectAudioOut != "" {
		audioFile, err = os.OpenFile(connectAudioOut, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("open --audio-out: %w", err)
		}
		defer audioFile.Close()
	}

	fmt.Printf("  %s %s\n", titleStyle.Render("Connected:"), addr)
	fmt.Println(dimStyle.Render("  Type to talk • /interrupt • /clear • /audio on|off • /quit"))
	if connectAudioOut != "" {
		fmt.Println(dimStyle.Render("  Writing raw PCM to " + connectAudioOut))
	}
	fmt.Println()

	// --audio-out implies stream mode so the file receives chunks without
	// requiring an explicit /audio on.
	if connectAudioOut != "" {
		optIn, _ := json.Marshal(map[string]string{"type": "audio_output", "mode": "stream"})
		if err := ws.Write(ctx, websocket.MessageText, optIn); err != nil {
			return fmt.Errorf("enable audio stream: %w", err)
		}
	}

	go printStream(ctx, ws, audioFile)

	// Read stdin off the main select so the first Ctrl+C (ctx cancel) can
	// close the WebSocket immediately instead of wedging on Scan.
	lines := make(chan string)
	scanErr := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			scanErr <- err
			return
		}
		close(lines)
	}()

	for {
		select {
		case <-ctx.Done():
			_ = ws.Close(websocket.StatusNormalClosure, "interrupted")
			return ctx.Err()
		case err := <-scanErr:
			return err
		case line, ok := <-lines:
			if !ok {
				return nil
			}
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}

			msg := map[string]string{"type": "text_input", "text": line}
			switch {
			case line == "/quit" || line == "/q" || line == "/exit":
				return nil
			case line == "/interrupt" || line == "/i":
				msg = map[string]string{"type": "interrupt"}
			case line == "/clear" || line == "/c":
				msg = map[string]string{"type": "clear_history"}
			case line == "/audio" || line == "/audio on" || line == "/stream":
				msg = map[string]string{"type": "audio_output", "mode": "stream"}
				fmt.Println(dimStyle.Render("  audio stream: on (server will push audio_chunk envelopes)"))
			case line == "/audio off":
				msg = map[string]string{"type": "audio_output", "mode": "off"}
				fmt.Println(dimStyle.Render("  audio stream: off"))
			}

			data, err := json.Marshal(msg)
			if err != nil {
				return err
			}
			if err := ws.Write(ctx, websocket.MessageText, data); err != nil {
				return fmt.Errorf("connection lost: %w", err)
			}
		}
	}
}

// printStream renders incoming envelopes until the connection or ctx dies.
// When audioFile is non-nil, decoded pcm_s16le payloads are appended to it.
func printStream(ctx context.Context, ws *websocket.Conn, audioFile *os.File) {
	var audioMu sync.Mutex
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			if ctx.Err() == nil {
				fmt.Println(dimStyle.Render("  disconnected"))
			}
			return
		}
		var env map[string]any
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}

		asString := func(key string) string {
			s, _ := env[key].(string)
			return s
		}

		switch env["type"] {
		case "user_input":
			fmt.Printf("  %s %s\n", keyStyle.Render("You:"), asString("text"))
		case "response_ready":
			fmt.Printf("  %s %s\n\n", activeStyle.Render("Samantha:"), asString("response"))
		case "thinking_started":
			fmt.Println(dimStyle.Render("  ● thinking..."))
		case "conversation_cleared":
			fmt.Println(dimStyle.Render("  Conversation cleared."))
		case "turn_interrupted":
			fmt.Println(dimStyle.Render("  turn interrupted (" + asString("reason") + ")"))
		case "audio_output_ack":
			fmt.Println(dimStyle.Render("  audio stream: " + asString("mode") + " (acked)"))
		case "audio_chunk":
			b64, _ := env["data"].(string)
			rate, _ := env["sample_rate"].(float64)
			raw, err := base64.StdEncoding.DecodeString(b64)
			n := 0
			if err == nil {
				n = len(raw)
				if audioFile != nil {
					audioMu.Lock()
					_, _ = audioFile.Write(raw)
					audioMu.Unlock()
				}
			}
			fmt.Printf("  %s %d B @ %.0f Hz\n", dimStyle.Render("audio_chunk"), n, rate)
		case "audio_end":
			fmt.Println(dimStyle.Render("  audio_end (" + asString("reason") + ")"))
		case "error":
			fmt.Printf("  %s %s\n", failStyle.Render("Error:"), asString("message"))
		case "info":
			fmt.Println(dimStyle.Render("  " + asString("message")))
		}
	}
}
