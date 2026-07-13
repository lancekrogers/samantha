package netapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"

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
	audio chan []byte // TTS audio_chunk / audio_end only
	kick  chan struct{}
	once  sync.Once

	// audioStream is a per-connection preference set by the audio_output
	// control message. atomic so the hub hot path never takes a second lock.
	audioStream atomic.Bool
}

func (c *streamConn) evict() {
	c.once.Do(func() { close(c.kick) })
}

func (c *streamConn) wantsAudio() bool {
	return c.audioStream.Load()
}

// hub owns the single bus subscription and fans events out to connections.
// The bus has one detachable SubscribeAll handler for the server's lifetime;
// connections attach and detach here, never on the bus itself.
type hub struct {
	mu    sync.Mutex
	conns map[*streamConn]struct{}

	// streamers counts connections with audio_output mode "stream" so the
	// TTS pump can skip encode/marshal when nobody is listening.
	streamers atomic.Int32
}

func newHub() *hub {
	return &hub{conns: make(map[*streamConn]struct{})}
}

func (h *hub) add(c *streamConn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.conns) >= maxStreamClients {
		return false
	}
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
			c.evict()
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
		if !c.wantsAudio() {
			continue
		}
		select {
		case c.audio <- msg:
		default:
			// Drop this chunk; keep the connection for events/control.
		}
	}
}

func (h *hub) attachBus(bus *events.Bus) (detach func()) {
	return bus.SubscribeAll(func(e events.Event) {
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
		kick:  make(chan struct{}),
	}
	if !s.hub.add(conn) {
		ws.Close(websocket.StatusTryAgainLater, "too many clients")
		return
	}
	defer s.hub.remove(conn)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		for {
			select {
			case <-ctx.Done():
				return
			case <-conn.kick:
				ws.Close(websocket.StatusPolicyViolation, "client too slow")
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
		default:
			s.sendError(conn, "unknown control message type: "+msg.Type)
		}
	}
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
		conn.evict()
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
		conn.evict()
	}
}

func dispatchErrText(err error) string {
	if errors.Is(err, ErrBusy) {
		return "server is busy — try again shortly"
	}
	return err.Error()
}
