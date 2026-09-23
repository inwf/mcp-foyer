package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"mcp-foyer/internal/events"
)

// WSPath is the event stream browsers connect to.
//
// It sits outside the management API prefix because it is a long-lived
// connection: counting it against the concurrent-request limit would let
// a few open browser tabs exhaust that limit and wedge the API. Like the
// MCP endpoint, it is bounded by the connection limit instead.
const WSPath = "/ws"

// Timings for the liveness probe and for writes.
const (
	// pingInterval is how often the server probes a client. A connection
	// through a proxy that idles out gives no error until the next write,
	// so probing is what turns a silently dead client into a closed one.
	pingInterval = 30 * time.Second

	// pongTimeout is how long a client has to answer. It must exceed
	// pingInterval, or a client would be dropped before its answer to
	// the previous probe was due.
	pongTimeout = 70 * time.Second

	// writeTimeout bounds a single write. Without it, a client that has
	// stopped reading but not closed would hold the writer goroutine
	// until the operating system gave up, which can take minutes.
	writeTimeout = 10 * time.Second
)

// sendBuffer is how many events a client may fall behind by before it is
// dropped. Large enough to absorb a burst of upstream churn, small
// enough that a client which has stopped reading is noticed quickly and
// pins little memory.
const sendBuffer = 64

// clientMessage is what a client may send.
type clientMessage struct {
	// Action is "subscribe", "unsubscribe" or "ping".
	Action string `json:"action"`

	// Kinds names the event kinds the action applies to. An empty list
	// with "subscribe" means every kind.
	Kinds []events.Kind `json:"kinds,omitempty"`
}

// serverMessage is what the server sends.
type serverMessage struct {
	// Type is "event", "welcome" or "error".
	Type string `json:"type"`

	Event *events.Event `json:"event,omitempty"`

	// Kinds echoes the subscription after it changes, so a client never
	// has to guess what it is receiving.
	Kinds []events.Kind `json:"kinds,omitempty"`

	Message string `json:"message,omitempty"`
}

// hub fans events out to the connected browsers.
//
// The hub holds one subscription to the bus and filters per client,
// rather than one bus subscription each. That keeps the drop policy in
// one place: a client that stops reading is disconnected, which is the
// only way to stop it from either blocking the others or growing
// without bound.
type hub struct {
	log *slog.Logger

	mu      sync.RWMutex
	clients map[*wsClient]struct{}
	closed  bool

	stop func()
	done chan struct{}
}

func newHub(log *slog.Logger) *hub {
	return &hub{
		log:     log,
		clients: map[*wsClient]struct{}{},
		done:    make(chan struct{}),
	}
}

// watch forwards every event on the bus until the bus closes or the hub
// does.
func (h *hub) watch(bus *events.Bus) {
	if bus == nil {
		close(h.done)
		return
	}

	stream, cancel := bus.Subscribe()
	h.mu.Lock()
	h.stop = cancel
	h.mu.Unlock()

	go func() {
		defer close(h.done)
		defer cancel()
		for event := range stream {
			h.broadcast(event)
		}
	}()
}

// broadcast offers an event to every client that asked for its kind.
func (h *hub) broadcast(event events.Event) {
	encoded, err := json.Marshal(serverMessage{Type: "event", Event: &event})
	if err != nil {
		h.log.Warn("could not encode an event for the browser", "kind", event.Kind, "error", err)
		return
	}

	h.mu.RLock()
	recipients := make([]*wsClient, 0, len(h.clients))
	for client := range h.clients {
		if client.wants(event.Kind) {
			recipients = append(recipients, client)
		}
	}
	h.mu.RUnlock()

	for _, client := range recipients {
		// A client whose buffer is full has stopped reading. Dropping it
		// is what keeps one stalled browser tab from holding up every
		// other client and the subsystem that published the event.
		if !client.offer(encoded) {
			h.log.Warn("dropped a client that stopped reading events",
				"remoteAddr", client.remoteAddr, "buffered", sendBuffer)
			client.closeSlow()
		}
	}
}

func (h *hub) add(client *wsClient) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return false
	}
	h.clients[client] = struct{}{}
	return true
}

func (h *hub) remove(client *wsClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, client)
}

// count is how many clients are connected.
func (h *hub) count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// close disconnects every client and stops watching the bus.
func (h *hub) close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	stop := h.stop
	clients := make([]*wsClient, 0, len(h.clients))
	for client := range h.clients {
		clients = append(clients, client)
	}
	h.clients = map[*wsClient]struct{}{}
	h.mu.Unlock()

	if stop != nil {
		stop()
	}
	for _, client := range clients {
		client.closeSlow()
	}
}

// wsClient is one connected browser.
type wsClient struct {
	conn       *websocket.Conn
	remoteAddr string

	// send carries queued messages. It is never closed: closing a
	// channel while another goroutine may be sending on it is a data
	// race, and both the hub and the reader can decide this client is
	// finished. done is what signals that instead.
	send chan []byte

	// done is closed once when this client is finished with.
	done chan struct{}

	closeOnce sync.Once

	mu sync.RWMutex
	// kinds is what this client asked for. A nil map means every kind,
	// which is what a client that never subscribes receives.
	kinds map[events.Kind]bool
}

// newWSClient prepares a client's queues.
func newWSClient(conn *websocket.Conn, remoteAddr string) *wsClient {
	return &wsClient{
		conn:       conn,
		remoteAddr: remoteAddr,
		send:       make(chan []byte, sendBuffer),
		done:       make(chan struct{}),
	}
}

func (c *wsClient) wants(kind events.Kind) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.kinds == nil || c.kinds[kind]
}

// offer queues a message, reporting false if the client is too far
// behind to take it or has already gone.
func (c *wsClient) offer(message []byte) bool {
	select {
	case <-c.done:
		return false
	default:
	}

	select {
	case c.send <- message:
		return true
	default:
		// The buffer is full, which means this client has stopped
		// reading. Reporting it is what lets the hub drop it instead of
		// waiting.
		return false
	}
}

// closeSlow signals that this client is finished, which ends its writer
// and closes its connection.
//
// The underlying connection's write deadline is also brought forward.
// Signalling alone is not enough when the writer is blocked mid-write on
// a client that stopped reading — which is exactly the case this is
// called for. The writer would then sit there until its own deadline
// expired, holding a goroutine and a socket for a client that has
// already been dropped.
//
// It has to be the underlying connection: the websocket wrapper's own
// SetWriteDeadline records a value to apply at the start of the next
// write, so it does not disturb one already in flight.
func (c *wsClient) closeSlow() {
	c.closeOnce.Do(func() {
		close(c.done)
		if c.conn != nil {
			// Deadline methods on a net.Conn are safe to call while a
			// write is in flight; that is what makes this work at all.
			_ = c.conn.NetConn().SetWriteDeadline(time.Now())
		}
	})
}

// subscribe replaces the client's interests.
func (c *wsClient) subscribe(kinds []events.Kind) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(kinds) == 0 {
		c.kinds = nil // every kind
		return
	}
	c.kinds = make(map[events.Kind]bool, len(kinds))
	for _, kind := range kinds {
		c.kinds[kind] = true
	}
}

// unsubscribe removes kinds from the client's interests.
func (c *wsClient) unsubscribe(kinds []events.Kind) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.kinds == nil {
		// It was receiving everything; start from that and subtract.
		c.kinds = make(map[events.Kind]bool, len(events.Kinds))
		for _, kind := range events.Kinds {
			c.kinds[kind] = true
		}
	}
	for _, kind := range kinds {
		delete(c.kinds, kind)
	}
}

// current lists what the client is subscribed to.
func (c *wsClient) current() []events.Kind {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.kinds == nil {
		return events.Kinds
	}
	out := make([]events.Kind, 0, len(c.kinds))
	for _, kind := range events.Kinds {
		if c.kinds[kind] {
			out = append(out, kind)
		}
	}
	return out
}

// handleWS upgrades a request and serves the client until it goes away.
func (a *API) handleWS(c *gin.Context) {
	upgrader := websocket.Upgrader{
		HandshakeTimeout: 10 * time.Second,
		CheckOrigin:      a.allowedOrigin,
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		// Upgrade has already written its own response.
		a.log.Warn("a websocket upgrade failed",
			"requestId", RequestID(c), "clientIp", c.ClientIP(), "error", err)
		return
	}

	client := newWSClient(conn, c.Request.RemoteAddr)

	if !a.hub.add(client) {
		conn.Close()
		return
	}

	a.log.Info("a client is watching events",
		"requestId", RequestID(c), "clientIp", c.ClientIP(), "clients", a.hub.count())

	go a.writeTo(client)
	a.readFrom(client)
}

// allowedOrigin decides whether a browser page may open this stream.
//
// A WebSocket handshake is not subject to the cross-origin checks that
// guard an ordinary request, so without this any page the user visits
// could open a connection to a gateway on their own machine and read
// every event: server names, tool names, configuration changes. The
// address allowlist does not help, because the browser connects from
// loopback on the user's behalf.
//
// A request with no Origin is not from a browser — a command-line client
// or a script — and has no ambient authority to abuse, so it is allowed.
func (a *API) allowedOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}

	// Compare hosts rather than whole URLs: the scheme differs between
	// the page (http) and the socket (ws), and the port is what actually
	// identifies this listener.
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if strings.EqualFold(parsed.Host, r.Host) {
		return true
	}

	for _, allowed := range a.opts.Security.AllowedOrigins {
		if strings.EqualFold(allowed, origin) {
			return true
		}
	}

	a.log.Warn("refused a websocket from another origin",
		"origin", origin, "host", r.Host)
	return false
}

// readFrom handles what the client sends, and notices when it goes away.
func (a *API) readFrom(client *wsClient) {
	defer func() {
		a.hub.remove(client)
		client.closeSlow()
		a.log.Info("a client stopped watching events", "clients", a.hub.count())
	}()

	client.conn.SetReadLimit(4 << 10)
	_ = client.conn.SetReadDeadline(time.Now().Add(pongTimeout))
	client.conn.SetPongHandler(func(string) error {
		return client.conn.SetReadDeadline(time.Now().Add(pongTimeout))
	})

	for {
		_, raw, err := client.conn.ReadMessage()
		if err != nil {
			return
		}

		var message clientMessage
		if err := json.Unmarshal(raw, &message); err != nil {
			client.offer(mustEncode(serverMessage{
				Type: "error", Message: "the message is not valid JSON",
			}))
			continue
		}

		switch message.Action {
		case "subscribe":
			client.subscribe(message.Kinds)
		case "unsubscribe":
			client.unsubscribe(message.Kinds)
		case "ping":
			// Answered below with the current subscription, which
			// doubles as a liveness check a client can drive itself.
		default:
			client.offer(mustEncode(serverMessage{
				Type:    "error",
				Message: "unknown action " + message.Action,
			}))
			continue
		}

		client.offer(mustEncode(serverMessage{Type: "welcome", Kinds: client.current()}))
	}
}

// writeTo sends queued messages and keeps the connection alive.
//
// Every write happens here, on one goroutine: a websocket connection
// does not permit concurrent writes, and the liveness probe is a write
// like any other.
func (a *API) writeTo(client *wsClient) {
	ticker := time.NewTicker(pingInterval)
	defer func() {
		ticker.Stop()
		client.conn.Close()
	}()

	for {
		select {
		case <-client.done:
			// The hub or the reader has finished with this client. The
			// close frame is best effort: the connection may already be
			// unusable, which is often why we are here.
			_ = client.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			_ = client.conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseGoingAway, ""))
			return

		case message := <-client.send:
			_ = client.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := client.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}

		case <-ticker.C:
			_ = client.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := client.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func mustEncode(message serverMessage) []byte {
	encoded, err := json.Marshal(message)
	if err != nil {
		return []byte(`{"type":"error","message":"could not encode the reply"}`)
	}
	return encoded
}
