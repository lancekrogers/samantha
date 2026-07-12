package netapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"

	"github.com/coder/websocket"

	"github.com/lancekrogers/samantha/internal/events"
)

// connQueueDepth bounds each connection's outbound queue. Bus.Emit runs
// handlers synchronously on pipeline goroutines, so enqueue is non-blocking:
// a client that can't keep up is disconnected rather than allowed to stall a
// live turn.
const connQueueDepth = 64

// maxStreamClients caps concurrent WebSocket connections — this is a
// single-user tool, not a service under load.
const maxStreamClients = 8

type streamConn struct {
	out  chan []byte
	kick chan struct{} // closed exactly once to evict a slow client
	once sync.Once
}

func (c *streamConn) evict() {
	c.once.Do(func() { close(c.kick) })
}

// hub owns the single bus subscription and fans events out to connections.
// The bus has one detachable SubscribeAll handler for the server's lifetime;
// connections attach and detach here, never on the bus itself.
type hub struct {
	mu    sync.Mutex
	conns map[*streamConn]struct{}
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
	delete(h.conns, c)
}

// broadcast never blocks: a full queue evicts that client.
func (h *hub) broadcast(msg []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.conns {
		select {
		case c.out <- msg:
		default:
			c.evict()
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
		out:  make(chan []byte, connQueueDepth),
		kick: make(chan struct{}),
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
		default:
			s.sendError(conn, "unknown control message type: "+msg.Type)
		}
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
