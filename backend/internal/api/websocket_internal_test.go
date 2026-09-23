package api

import (
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"mcp-foyer/internal/events"
)

// stalled is a client whose buffer is already full, standing in for one
// that has stopped reading.
func stalled() *wsClient {
	client := newWSClient(nil, "stalled")
	for i := 0; i < sendBuffer; i++ {
		client.send <- []byte("backlog")
	}
	return client
}

// reading is a client that drains everything sent to it, and reports how
// much it received.
func reading(t *testing.T) (*wsClient, func() int) {
	t.Helper()

	client := newWSClient(nil, "reading")

	var mu sync.Mutex
	var count int
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-client.done:
				return
			case <-client.send:
				mu.Lock()
				count++
				mu.Unlock()
			}
		}
	}()
	t.Cleanup(func() {
		client.closeSlow()
		<-stopped
	})

	return client, func() int {
		mu.Lock()
		defer mu.Unlock()
		return count
	}
}

// This is the property the whole hub design rests on: one browser tab
// that has stopped reading must not hold up every other client, nor the
// subsystem that published the event. The alternative — waiting for it —
// would let one stalled client stall a tool call.
func TestAStalledClientDoesNotHoldUpTheOthers(t *testing.T) {
	h := newHub(slog.New(discardHandler{}))

	slow := stalled()
	fast, received := reading(t)
	h.add(slow)
	h.add(fast)

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for i := 0; i < 10; i++ {
			h.broadcast(events.Event{Kind: events.ToolsChanged, At: time.Unix(0, 0)})
		}
	}()

	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("broadcasting blocked on the client that stopped reading")
	}

	// The reader got everything.
	deadline := time.Now().Add(10 * time.Second)
	for received() < 10 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := received(); got != 10 {
		t.Errorf("the reading client received %d of 10 events", got)
	}
}

// A client too far behind is disconnected rather than kept. Keeping it
// would mean either growing its buffer without bound or silently
// dropping events, and a client that has silently missed events cannot
// tell that its view is stale.
func TestAStalledClientIsDropped(t *testing.T) {
	h := newHub(slog.New(discardHandler{}))

	slow := stalled()
	h.add(slow)

	h.broadcast(events.Event{Kind: events.ToolsChanged, At: time.Unix(0, 0)})

	// Signalling done is what ends the writer and closes the connection.
	select {
	case <-slow.done:
	case <-time.After(5 * time.Second):
		t.Error("the stalled client was not disconnected")
	}

	// Nothing more is accepted for it, so the hub stops paying to encode
	// events it will never deliver.
	if slow.offer([]byte("later")) {
		t.Error("a dropped client still accepted an event")
	}
}

// Dropping a client must not happen twice: the reader ending and the hub
// giving up can both decide to, and closing a closed channel panics.
func TestDroppingAClientTwiceIsSafe(t *testing.T) {
	client := newWSClient(nil, "test")

	client.closeSlow()
	client.closeSlow() // would panic without the guard
}

// Offering to a client that is already going away loses the event, which
// is correct, but must not panic on a closed channel.
func TestOfferingToAClosedClientIsSafe(t *testing.T) {
	client := newWSClient(nil, "test")
	client.closeSlow()

	if client.offer([]byte("late")) {
		t.Error("offering to a closed client reported success")
	}
}

// The hub is written to by every subsystem that publishes and read by
// every connected browser, so it is under concurrent use by definition.
func TestTheHubIsSafeUnderConcurrency(t *testing.T) {
	h := newHub(slog.New(discardHandler{}))

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 50; n++ {
				client, _ := reading(t)
				h.add(client)
				h.remove(client)
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 50; n++ {
				h.broadcast(events.Event{Kind: events.ServerStatus, At: time.Unix(0, 0)})
				_ = h.count()
			}
		}()
	}
	wg.Wait()
}

// ===== subscription bookkeeping =====

// A client that unsubscribes from one kind keeps the rest, which is what
// "unsubscribe" means as against "subscribe to this instead".
func TestUnsubscribingFromEverythingLeavesNothing(t *testing.T) {
	client := newWSClient(nil, "test")

	client.unsubscribe(events.Kinds)

	for _, kind := range events.Kinds {
		if client.wants(kind) {
			t.Errorf("still wants %q after unsubscribing from every kind", kind)
		}
	}
}

func TestUnsubscribingFromOneKindKeepsTheRest(t *testing.T) {
	client := newWSClient(nil, "test")

	client.unsubscribe([]events.Kind{events.ConfigUpdated})

	if client.wants(events.ConfigUpdated) {
		t.Error("still wants the kind it unsubscribed from")
	}
	if !client.wants(events.ToolsChanged) {
		t.Error("unsubscribing from one kind dropped another")
	}
}

// Subscribing with no kinds means everything, which is how a client
// undoes a narrow subscription without reconnecting.
func TestSubscribingToNothingMeansEverything(t *testing.T) {
	client := newWSClient(nil, "test")

	client.subscribe([]events.Kind{events.ToolsChanged})
	if client.wants(events.ConfigUpdated) {
		t.Fatal("a narrow subscription did not take effect")
	}

	client.subscribe(nil)
	for _, kind := range events.Kinds {
		if !client.wants(kind) {
			t.Errorf("does not want %q after subscribing to everything", kind)
		}
	}
}

// ===== encoding =====

// The browser reads these, so the shape has to be what the frontend's
// types describe.
func TestAnEventIsEncodedForTheBrowser(t *testing.T) {
	h := newHub(slog.New(discardHandler{}))
	client, _ := reading(t)
	h.add(client)

	// Take the message directly rather than through the reader.
	h.remove(client)
	direct := newWSClient(nil, "direct")
	h.add(direct)

	at := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	h.broadcast(events.Event{
		Kind: events.ServerFailed, Server: "files", At: at,
		Data: map[string]any{"error": "the process exited"},
	})

	var message struct {
		Type  string `json:"type"`
		Event struct {
			Kind   events.Kind    `json:"kind"`
			Server string         `json:"server"`
			At     time.Time      `json:"at"`
			Data   map[string]any `json:"data"`
		} `json:"event"`
	}
	if err := json.Unmarshal(<-direct.send, &message); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if message.Type != "event" {
		t.Errorf("type = %q, want event", message.Type)
	}
	if message.Event.Kind != events.ServerFailed {
		t.Errorf("kind = %q, want %q", message.Event.Kind, events.ServerFailed)
	}
	if message.Event.Server != "files" {
		t.Errorf("server = %q, want files", message.Event.Server)
	}
	if !message.Event.At.Equal(at) {
		t.Errorf("at = %v, want %v", message.Event.At, at)
	}
	if message.Event.Data["error"] != "the process exited" {
		t.Errorf("data = %+v, want the detail to survive", message.Event.Data)
	}
}
