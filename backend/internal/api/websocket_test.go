package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"mcp-foyer/internal/api"
	"mcp-foyer/internal/config"
	"mcp-foyer/internal/events"
)

// watching starts an API with an event bus and returns both.
func watching(t *testing.T, adjust func(*api.Options)) (*harness, *events.Bus) {
	t.Helper()

	bus := events.NewBus()
	t.Cleanup(bus.Close)

	h := start(t, func(o *api.Options) {
		o.Bus = bus
		if adjust != nil {
			adjust(o)
		}
	})
	t.Cleanup(h.API.Close)

	return h, bus
}

// wsURL turns the test server's address into a websocket address.
func wsURL(h *harness) string {
	return "ws" + strings.TrimPrefix(h.URL, "http") + api.WSPath
}

// dialWS opens an event stream.
func dialWS(t *testing.T, h *harness) *websocket.Conn {
	t.Helper()

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(h), nil)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("dial the event stream (status %d): %v", status, err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// nextEvent reads until an event arrives, ignoring the replies to
// subscription changes.
func nextEvent(t *testing.T, conn *websocket.Conn, within time.Duration) events.Event {
	t.Helper()

	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(deadline)
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}

		var message struct {
			Type  string       `json:"type"`
			Event events.Event `json:"event"`
		}
		if err := json.Unmarshal(raw, &message); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		if message.Type == "event" {
			return message.Event
		}
	}
	t.Fatal("no event arrived in time")
	return events.Event{}
}

// send writes a client message.
func send(t *testing.T, conn *websocket.Conn, message any) {
	t.Helper()
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteJSON(message); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// ===== step 66: connecting and receiving =====

func TestAClientReceivesEvents(t *testing.T) {
	h, bus := watching(t, nil)
	conn := dialWS(t, h)

	// The hub has to have registered the client before an event can
	// reach it.
	eventuallyClients(t, h, 1)
	bus.Publish(events.Event{Kind: events.ServerConnected, Server: "files"})

	got := nextEvent(t, conn, 10*time.Second)
	if got.Kind != events.ServerConnected {
		t.Errorf("kind = %q, want %q", got.Kind, events.ServerConnected)
	}
	if got.Server != "files" {
		t.Errorf("server = %q, want files", got.Server)
	}
	if got.At.IsZero() {
		t.Error("the event carries no timestamp")
	}
}

func TestSeveralClientsAllReceiveAnEvent(t *testing.T) {
	h, bus := watching(t, nil)

	conns := make([]*websocket.Conn, 3)
	for i := range conns {
		conns[i] = dialWS(t, h)
	}
	eventuallyClients(t, h, 3)

	bus.Publish(events.Event{Kind: events.ToolsChanged, Server: "files"})

	for i, conn := range conns {
		if got := nextEvent(t, conn, 10*time.Second); got.Kind != events.ToolsChanged {
			t.Errorf("client %d received %q, want %q", i, got.Kind, events.ToolsChanged)
		}
	}
}

// A client that goes away has to be forgotten, or the hub grows for the
// life of the process and keeps writing to closed sockets.
func TestADisconnectedClientIsForgotten(t *testing.T) {
	h, _ := watching(t, nil)

	conn := dialWS(t, h)
	eventuallyClients(t, h, 1)

	conn.Close()
	eventuallyClients(t, h, 0)
}

// A websocket hijacks its connection, which takes it out of the HTTP
// server's bookkeeping. The connection limiter counts it anyway, and has
// to get the slot back when the socket finally closes.
func TestAClosedWebsocketReturnsItsConnectionSlot(t *testing.T) {
	bus := events.NewBus()
	t.Cleanup(bus.Close)

	h := start(t, func(o *api.Options) {
		o.Bus = bus
		o.Security = config.Default().Security
		o.Security.MaxConnections = 3
	})
	t.Cleanup(h.API.Close)

	conn := dialWS(t, h)
	eventuallyClients(t, h, 1)
	conn.Close()
	eventuallyClients(t, h, 0)

	// The listener still accepts, which it would not if the hijacked
	// connection had kept its slot after closing.
	for i := 0; i < 5; i++ {
		again := dialWS(t, h)
		eventuallyClients(t, h, 1)
		again.Close()
		eventuallyClients(t, h, 0)
	}
}

// ===== step 67: subscription filtering =====

// A client that asked for one kind must not be sent another: a log
// viewer receiving every tool call would have to filter them itself, and
// would pay to receive them first.
func TestSubscribingNarrowsWhatArrives(t *testing.T) {
	h, bus := watching(t, nil)
	conn := dialWS(t, h)
	eventuallyClients(t, h, 1)

	send(t, conn, map[string]any{
		"action": "subscribe",
		"kinds":  []events.Kind{events.ToolsChanged},
	})
	awaitSubscription(t, conn, events.ToolsChanged)

	// The unwanted one is published first, so a hub that ignored the
	// subscription would deliver it first too.
	bus.Publish(events.Event{Kind: events.ServerConnected, Server: "ignored"})
	bus.Publish(events.Event{Kind: events.ToolsChanged, Server: "wanted"})

	got := nextEvent(t, conn, 10*time.Second)
	if got.Kind != events.ToolsChanged {
		t.Fatalf("received %q first, want only the subscribed kind", got.Kind)
	}
	if got.Server != "wanted" {
		t.Errorf("server = %q, want wanted", got.Server)
	}
}

func TestSubscribingToSeveralKinds(t *testing.T) {
	h, bus := watching(t, nil)
	conn := dialWS(t, h)
	eventuallyClients(t, h, 1)

	send(t, conn, map[string]any{
		"action": "subscribe",
		"kinds":  []events.Kind{events.ToolsChanged, events.ServerFailed},
	})
	awaitSubscription(t, conn, events.ServerFailed)

	bus.Publish(events.Event{Kind: events.ConfigUpdated})
	bus.Publish(events.Event{Kind: events.ServerFailed, Server: "a"})
	bus.Publish(events.Event{Kind: events.ToolsChanged, Server: "b"})

	for _, want := range []events.Kind{events.ServerFailed, events.ToolsChanged} {
		if got := nextEvent(t, conn, 10*time.Second); got.Kind != want {
			t.Errorf("received %q, want %q", got.Kind, want)
		}
	}
}

// A client that never subscribes receives everything, which is what
// makes the stream useful without a round trip first.
func TestAClientThatNeverSubscribesReceivesEverything(t *testing.T) {
	h, bus := watching(t, nil)
	conn := dialWS(t, h)
	eventuallyClients(t, h, 1)

	for _, kind := range []events.Kind{events.ConfigUpdated, events.ToolsChanged} {
		bus.Publish(events.Event{Kind: kind})
		if got := nextEvent(t, conn, 10*time.Second); got.Kind != kind {
			t.Errorf("received %q, want %q", got.Kind, kind)
		}
	}
}

func TestUnsubscribingStopsAKind(t *testing.T) {
	h, bus := watching(t, nil)
	conn := dialWS(t, h)
	eventuallyClients(t, h, 1)

	send(t, conn, map[string]any{
		"action": "unsubscribe",
		"kinds":  []events.Kind{events.ConfigUpdated},
	})
	// The reply lists what remains, which must no longer include it.
	kinds := awaitReply(t, conn)
	for _, kind := range kinds {
		if kind == events.ConfigUpdated {
			t.Fatalf("the subscription still includes %q: %v", kind, kinds)
		}
	}

	bus.Publish(events.Event{Kind: events.ConfigUpdated})
	bus.Publish(events.Event{Kind: events.ToolsChanged})

	if got := nextEvent(t, conn, 10*time.Second); got.Kind != events.ToolsChanged {
		t.Errorf("received %q, want the unsubscribed kind to have been skipped", got.Kind)
	}
}

// A client has to be able to tell what it is receiving without keeping
// its own tally.
func TestTheSubscriptionIsEchoedBack(t *testing.T) {
	h, _ := watching(t, nil)
	conn := dialWS(t, h)

	send(t, conn, map[string]any{
		"action": "subscribe",
		"kinds":  []events.Kind{events.ServerStatus},
	})

	kinds := awaitReply(t, conn)
	if len(kinds) != 1 || kinds[0] != events.ServerStatus {
		t.Errorf("the reply lists %v, want just the subscribed kind", kinds)
	}
}

func TestAMalformedClientMessageIsReported(t *testing.T) {
	h, _ := watching(t, nil)
	conn := dialWS(t, h)

	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, []byte("{not json")); err != nil {
		t.Fatalf("write: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var message struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	json.Unmarshal(raw, &message)
	if message.Type != "error" {
		t.Errorf("type = %q, want error", message.Type)
	}
	// The connection stays usable: one bad message is not a reason to
	// make the client reconnect and resubscribe.
	send(t, conn, map[string]any{"action": "ping"})
	awaitReply(t, conn)
}

func TestAnUnknownActionIsReported(t *testing.T) {
	h, _ := watching(t, nil)
	conn := dialWS(t, h)

	send(t, conn, map[string]any{"action": "demolish"})

	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(raw), "demolish") {
		t.Errorf("the reply %s does not name the unknown action", raw)
	}
}

// ===== origin =====

// A websocket handshake escapes the cross-origin checks that guard an
// ordinary request, so any page the user visits could otherwise open a
// connection to a gateway on their own machine and read every event.
func TestAWebsocketFromAnotherOriginIsRefused(t *testing.T) {
	h, _ := watching(t, nil)

	header := http.Header{}
	header.Set("Origin", "https://evil.example.com")

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(h), header)
	if err == nil {
		conn.Close()
		t.Fatal("a websocket from another origin was accepted")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Errorf("status = %d, want 403", status)
	}
}

// A page served from this listener is the normal case and must work.
func TestAWebsocketFromTheSameOriginIsAccepted(t *testing.T) {
	h, _ := watching(t, nil)

	header := http.Header{}
	header.Set("Origin", h.URL)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(h), header)
	if err != nil {
		t.Fatalf("a same-origin websocket was refused: %v", err)
	}
	conn.Close()
}

// Running the frontend from a development server is the case that needs
// an explicit entry.
func TestAConfiguredOriginIsAccepted(t *testing.T) {
	h, _ := watching(t, func(o *api.Options) {
		o.Security = config.Default().Security
		o.Security.AllowedOrigins = []string{"http://localhost:5173"}
	})

	header := http.Header{}
	header.Set("Origin", "http://localhost:5173")

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(h), header)
	if err != nil {
		t.Fatalf("a configured origin was refused: %v", err)
	}
	conn.Close()
}

// A client with no Origin is not a browser — a script or the CLI — and
// has no ambient authority to abuse.
func TestAWebsocketWithNoOriginIsAccepted(t *testing.T) {
	h, _ := watching(t, nil)

	// websocket.Dialer sends no Origin unless one is set.
	conn := dialWS(t, h)
	conn.Close()
}

// ===== step 68: backpressure =====

// flood publishes steadily enough to overflow a client that is not
// reading, without overflowing the bus itself.
//
// The bus has its own drop policy for a subscriber that falls behind,
// and the hub is a single subscriber. Publishing in a tight loop would
// make the bus discard most of the events before the hub ever saw them,
// which would test the bus rather than the hub.
func flood(bus *events.Bus, count int) {
	payload := strings.Repeat("x", 8<<10)
	for i := 0; i < count; i++ {
		bus.Publish(events.Event{
			Kind: events.ServerStatus, Server: "noisy",
			Data: map[string]any{"filler": payload},
		})
		if i%16 == 0 {
			// Let the hub keep up, so what is published reaches a client.
			time.Sleep(time.Millisecond)
		}
	}
}

// The end-to-end form of the drop policy: a real client that stops
// reading its socket must not stop a real client that is still reading.
//
// The internal tests pin the policy; this one confirms it survives the
// kernel's own buffering, which absorbs a good deal before a write to
// the stalled client blocks at all.
func TestARealClientThatStopsReadingDoesNotStopTheOthers(t *testing.T) {
	h, bus := watching(t, nil)

	// This one is dialled and then never read from.
	stalled := dialWS(t, h)
	defer stalled.Close()

	attentive := dialWS(t, h)
	eventuallyClients(t, h, 2)

	// The attentive client reads continuously, as a browser would.
	received := make(chan struct{}, 1)
	go func() {
		for {
			attentive.SetReadDeadline(time.Now().Add(60 * time.Second))
			if _, _, err := attentive.ReadMessage(); err != nil {
				return
			}
			select {
			case received <- struct{}{}:
			default:
			}
		}
	}()

	flood(bus, 4000)

	// The stalled client is gone, and the attentive one is still here.
	eventuallyClients(t, h, 1)

	// Drain whatever is in flight, then confirm delivery still works.
	for len(received) > 0 {
		<-received
	}
	bus.Publish(events.Event{Kind: events.ConfigUpdated, Server: "marker"})

	select {
	case <-received:
	case <-time.After(30 * time.Second):
		t.Fatal("the attentive client stopped receiving after a stalled client was dropped")
	}
}

// The stalled client is disconnected rather than kept indefinitely,
// which is what stops it pinning memory and a goroutine for the life of
// the process.
func TestARealClientThatStopsReadingIsDropped(t *testing.T) {
	h, bus := watching(t, nil)

	stalled := dialWS(t, h)
	defer stalled.Close()
	eventuallyClients(t, h, 1)

	flood(bus, 4000)
	eventuallyClients(t, h, 0)
}

// ===== helpers =====

func eventuallyClients(t *testing.T, h *harness, want int) {
	t.Helper()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if got := h.API.WatchingClients(); got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("watching clients = %d, want %d", h.API.WatchingClients(), want)
}

// awaitReply reads the next subscription reply and returns the kinds it
// lists.
func awaitReply(t *testing.T, conn *websocket.Conn) []events.Kind {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(deadline)
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var message struct {
			Type  string        `json:"type"`
			Kinds []events.Kind `json:"kinds"`
		}
		if err := json.Unmarshal(raw, &message); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		if message.Type == "welcome" {
			return message.Kinds
		}
	}
	t.Fatal("no reply arrived in time")
	return nil
}

// awaitSubscription reads replies until the subscription includes want.
func awaitSubscription(t *testing.T, conn *websocket.Conn, want events.Kind) {
	t.Helper()

	for _, kind := range awaitReply(t, conn) {
		if kind == want {
			return
		}
	}
	t.Fatalf("the subscription does not include %q", want)
}
