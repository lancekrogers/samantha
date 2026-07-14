package netapi

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/events"
)

// connQueueDepth bounds each connection's outbound *event* queue. Bus.Emit runs
// handlers synchronously on pipeline goroutines, so enqueue is non-blocking:
// a client that can't keep up on events is disconnected rather than allowed
// to stall a live turn.
const connQueueDepth = 64

// audioQueueDepth bounds per-connection TTS chunk buffering. Full audio
// queues drop the chunk rather than kicking the client — audio lag must not
// tear down the control/event stream.
const audioQueueDepth = 128

// maxStreamClients caps concurrent WebSocket connections — this is a
// single-user tool, not a service under load.
const maxStreamClients = 8

type streamConn struct {
	out   chan []byte // event / control envelopes
	audio chan []byte // TTS audio_chunk / audio_end / audio_reset only
	kick  chan string
	once  sync.Once

	// audioStream is a per-connection preference set by the audio_output
	// control message. atomic so the hub hot path never takes a second lock.
	audioStream atomic.Bool
	// audioBlocked is set for every stream client when the shared turn is
	// interrupted. It stays set until the next turn starts, preventing tail
	// chunks from the canceled turn from crossing the reset boundary.
	audioBlocked atomic.Bool
}

func (c *streamConn) evict(reason string) {
	c.once.Do(func() { c.kick <- reason })
}

func (c *streamConn) wantsAudio() bool {
	return c.audioStream.Load()
}

// hub owns the single bus subscription and fans events out to connections.
// The bus has one detachable SubscribeAll handler for the server's lifetime;
// connections attach and detach here, never on the bus itself.
type hub struct {
	mu           sync.Mutex
	conns        map[*streamConn]struct{}
	audioBlocked bool

	// streamers counts connections with audio_output mode "stream" so the
	// TTS pump can skip encode/marshal when nobody is listening.
	streamers atomic.Int32

	// micClaimant is the exclusive remote-mic owner (push-to-talk).
	micClaimant *streamConn
	// ingress receives remote PCM when remote mic is enabled.
	ingress *audio.Ingress
}

func newHub() *hub {
	return &hub{conns: make(map[*streamConn]struct{})}
}

// setIngress wires the remote audio ingress used by push-to-talk turns.
func (h *hub) setIngress(ing *audio.Ingress) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ingress = ing
}

func (h *hub) claimMic(c *streamConn) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ingress == nil {
		return errors.New("remote mic is not enabled on this serve instance")
	}
	if h.micClaimant != nil && h.micClaimant != c {
		return errors.New("microphone claimed by another client")
	}
	h.micClaimant = c
	h.ingress.Reset()
	return nil
}

func (h *hub) releaseMic(c *streamConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.micClaimant == c {
		h.micClaimant = nil
		// If the client disconnects mid-utterance, finalize so STT is not
		// left blocked waiting for more frames.
		if h.ingress != nil {
			h.ingress.Finalize()
		}
	}
}

func (h *hub) writeMic(c *streamConn, samples []float32) error {
	h.mu.Lock()
	ing := h.ingress
	claim := h.micClaimant
	h.mu.Unlock()
	if ing == nil {
		return errors.New("remote mic is not enabled")
	}
	if claim != c {
		return errors.New("microphone not claimed by this client")
	}
	return ing.Write(samples)
}

func (h *hub) endMicUtterance(c *streamConn) error {
	h.mu.Lock()
	ing := h.ingress
	claim := h.micClaimant
	h.mu.Unlock()
	if ing == nil {
		return errors.New("remote mic is not enabled")
	}
	if claim != c {
		return errors.New("microphone not claimed by this client")
	}
	ing.Finalize()
	return nil
}

func (h *hub) add(c *streamConn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.conns) >= maxStreamClients {
		return false
	}
	c.audioBlocked.Store(h.audioBlocked)
	h.conns[c] = struct{}{}
	return true
}

func (h *hub) remove(c *streamConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.conns[c]; !ok {
		return
	}
	delete(h.conns, c)
	if c.audioStream.Swap(false) {
		h.streamers.Add(-1)
	}
}

// evictAll terminates every live stream at a security boundary such as token
// revocation. Handlers remove themselves from the hub as their sockets close.
func (h *hub) evictAll(reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.conns {
		c.evict(reason)
	}
	if h.micClaimant != nil && h.ingress != nil {
		h.ingress.Finalize()
		h.micClaimant = nil
	}
}

// setConnAudio updates a connection's stream preference and the hub streamer
// count. Safe to call from the control reader while the hub is live.
func (h *hub) setConnAudio(c *streamConn, on bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.conns[c]; !ok {
		return
	}
	was := c.audioStream.Load()
	if was == on {
		return
	}
	c.audioStream.Store(on)
	if on {
		h.streamers.Add(1)
	} else {
		h.streamers.Add(-1)
	}
}

// hasStreamClients reports whether any connected client wants TTS audio.
// Lock-free so the AudioFanout pump can check before encoding.
func (h *hub) hasStreamClients() bool {
	return h.streamers.Load() > 0
}

// broadcast never blocks: a full event queue evicts that client and reclaims
// the hub slot immediately so maxStreamClients capacity is available for new
// connections without waiting for handleStream to return.
func (h *hub) broadcast(msg []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.conns {
		select {
		case c.out <- msg:
		default:
			c.evict("client too slow")
			// If this client was streaming, drop the streamer count now —
			// remove() will also run later, so clear the flag first.
			if c.audioStream.Swap(false) {
				h.streamers.Add(-1)
			}
			delete(h.conns, c)
		}
	}
}

// broadcastAudio delivers TTS wire chunks only to clients that opted into
// stream mode. A full audio queue drops the chunk; it never kicks the client.
func (h *hub) broadcastAudio(msg []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.conns {
		if !c.wantsAudio() || c.audioBlocked.Load() {
			continue
		}
		select {
		case c.audio <- msg:
		default:
			// Drop this chunk; keep the connection for events/control.
		}
	}
}

var audioResetEnvelope = []byte(`{"type":"audio_reset"}`)

// resetAudio establishes an ordered cancellation boundary for every stream
// client. Holding the hub lock excludes broadcastAudio while queued tail
// chunks are drained and audio_reset is enqueued. The browser suppresses audio
// as soon as it sends interrupt, then resumes when it receives this marker.
func (h *hub) resetAudio() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.audioBlocked = true
	for c := range h.conns {
		if !c.wantsAudio() {
			continue
		}
		c.audioBlocked.Store(true)
		for {
			select {
			case <-c.audio:
				continue
			default:
			}
			break
		}
		// The queue was just drained while broadcasts were excluded, so this
		// send cannot block.
		c.audio <- audioResetEnvelope
	}
}

// resumeAudio opens the next-turn side of the reset boundary. ThinkingStarted
// is emitted before synthesis, so no new turn audio can race ahead of this.
func (h *hub) resumeAudio() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.audioBlocked = false
	for c := range h.conns {
		c.audioBlocked.Store(false)
	}
}

func (h *hub) attachBus(bus *events.Bus) (detach func()) {
	return bus.SubscribeAll(func(e events.Event) {
		if _, ok := e.(events.ThinkingStarted); ok {
			h.resumeAudio()
		}
		msg, err := marshalEvent(e)
		if err != nil {
			return
		}
		h.broadcast(msg)
	})
}

// handleStream upgrades /v1/stream and bridges the hub to one client.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}

	conn := &streamConn{
		out:   make(chan []byte, connQueueDepth),
		audio: make(chan []byte, audioQueueDepth),
		kick:  make(chan string, 1),
	}
	if !s.hub.add(conn) {
		ws.Close(websocket.StatusTryAgainLater, "too many clients")
		return
	}
	defer func() {
		s.hub.releaseMic(conn)
		s.hub.remove(conn)
	}()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		for {
			select {
			case <-ctx.Done():
				return
			case reason := <-conn.kick:
				ws.Close(websocket.StatusPolicyViolation, reason)
				return
			case msg := <-conn.out:
				if err := ws.Write(ctx, websocket.MessageText, msg); err != nil {
					return
				}
			case msg := <-conn.audio:
				if err := ws.Write(ctx, websocket.MessageText, msg); err != nil {
					return
				}
			}
		}
	}()

	s.readControls(ctx, ws, conn)
	cancel()
	<-writeDone
	ws.Close(websocket.StatusNormalClosure, "")
}

// readControls decodes inbound control messages until the connection dies.
func (s *Server) readControls(ctx context.Context, ws *websocket.Conn, conn *streamConn) {
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if !s.opts.Credentials.tokenActive() {
			conn.evict("credentials revoked")
			return
		}

		var msg controlMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			s.sendError(conn, "malformed control message")
			continue
		}

		switch msg.Type {
		case "text_input":
			if msg.Text == "" {
				s.sendError(conn, "text_input requires text")
				continue
			}
			if err := s.dispatcher.SubmitText(msg.Text); err != nil {
				s.sendError(conn, dispatchErrText(err))
			}
		case "interrupt":
			s.hub.resetAudio()
			s.dispatcher.Interrupt()
		case "clear_history":
			if err := s.dispatcher.ClearHistory(); err != nil {
				s.sendError(conn, dispatchErrText(err))
			}
		case "audio_output":
			// Phase 3: per-connection preference. mode "stream" opts into
			// TTS audio_chunk delivery; anything else turns it off. Default
			// is off so event-only clients are unaffected.
			mode := "off"
			if msg.Mode == "stream" {
				mode = "stream"
			}
			s.hub.setConnAudio(conn, mode == "stream")
			s.sendAudioOutputAck(conn, mode)
		case "voice_start":
			// Phase 4 / WI-62e19b: exclusive push-to-talk. Claim mic, reset
			// ingress, and enqueue a voice turn that blocks on remote PCM.
			if err := s.hub.claimMic(conn); err != nil {
				s.sendError(conn, err.Error())
				continue
			}
			if err := s.dispatcher.SubmitVoice(); err != nil {
				s.hub.releaseMic(conn)
				s.sendError(conn, dispatchErrText(err))
			}
		case "audio_input":
			samples, err := decodePCMS16LE(msg.Data, msg.SampleRate)
			if err != nil {
				s.sendError(conn, "audio_input: "+err.Error())
				continue
			}
			if err := s.hub.writeMic(conn, samples); err != nil {
				s.sendError(conn, err.Error())
			}
		case "voice_end":
			if err := s.hub.endMicUtterance(conn); err != nil {
				s.sendError(conn, err.Error())
				continue
			}
			// Drop exclusive claim after finalizing so another client can talk
			// while TTS plays. releaseMic finalizes again (idempotent).
			s.hub.releaseMic(conn)
		default:
			s.sendError(conn, "unknown control message type: "+msg.Type)
		}
	}
}

// decodePCMS16LE decodes base64 little-endian mono PCM into float32 samples
// at the capture sample rate. Non-16 kHz input is rejected (client resamples).
func decodePCMS16LE(b64 string, sampleRate int) ([]float32, error) {
	if b64 == "" {
		return nil, errors.New("empty data")
	}
	if sampleRate != 0 && sampleRate != audio.SampleRate {
		return nil, errors.New("sample_rate must be 16000 (resample on the client)")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, errors.New("invalid base64")
	}
	if len(raw)%2 != 0 {
		return nil, errors.New("odd pcm byte length")
	}
	n := len(raw) / 2
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		s := int16(binary.LittleEndian.Uint16(raw[i*2:]))
		out[i] = float32(s) / 32768
	}
	return out, nil
}

// sendAudioOutputAck confirms the applied stream preference on the event
// channel so clients do not have to assume success.
func (s *Server) sendAudioOutputAck(conn *streamConn, mode string) {
	msg, err := json.Marshal(map[string]any{
		"type": "audio_output_ack",
		"mode": mode,
	})
	if err != nil {
		return
	}
	select {
	case conn.out <- msg:
	default:
		conn.evict("client too slow")
	}
}

// sendError delivers a per-connection error envelope (not broadcast — only
// the offending client sees it).
func (s *Server) sendError(conn *streamConn, text string) {
	msg, err := marshalEvent(events.Error{Stage: "netapi", Message: text})
	if err != nil {
		return
	}
	select {
	case conn.out <- msg:
	default:
		conn.evict("client too slow")
	}
}

func dispatchErrText(err error) string {
	if errors.Is(err, ErrBusy) {
		return "server is busy — try again shortly"
	}
	return err.Error()
}
