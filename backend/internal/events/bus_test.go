package events_test

import (
	"sync"
	"testing"
	"time"

	"mcp-foyer/internal/events"
)

func receive(t *testing.T, ch <-chan events.Event) events.Event {
	t.Helper()
	select {
	case e, ok := <-ch:
		if !ok {
			t.Fatal("the channel was closed")
		}
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("no event arrived")
		return events.Event{}
	}
}

func expectNothing(t *testing.T, ch <-chan events.Event) {
	t.Helper()
	select {
	case e := <-ch:
		t.Fatalf("an unexpected event arrived: %+v", e)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestPublishReachesSubscribers(t *testing.T) {
	bus := events.NewBus()
	defer bus.Close()

	first, cancelFirst := bus.Subscribe()
	defer cancelFirst()
	second, cancelSecond := bus.Subscribe()
	defer cancelSecond()

	bus.PublishServer(events.ServerConnected, "files", nil)

	for i, ch := range []<-chan events.Event{first, second} {
		got := receive(t, ch)
		if got.Kind != events.ServerConnected {
			t.Errorf("subscriber %d got kind %q, want %q", i, got.Kind, events.ServerConnected)
		}
		if got.Server != "files" {
			t.Errorf("subscriber %d got server %q, want files", i, got.Server)
		}
	}
}

func TestPublishStampsTheTime(t *testing.T) {
	at := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	bus := events.NewBus()
	bus.Now = func() time.Time { return at }
	defer bus.Close()

	ch, cancel := bus.Subscribe()
	defer cancel()

	bus.Publish(events.Event{Kind: events.ConfigUpdated})

	if got := receive(t, ch).At; !got.Equal(at) {
		t.Errorf("at = %v, want %v", got, at)
	}
}

func TestSubscribeFiltersByKind(t *testing.T) {
	bus := events.NewBus()
	defer bus.Close()

	ch, cancel := bus.Subscribe(events.ToolsChanged)
	defer cancel()

	bus.PublishServer(events.ResourcesChanged, "files", nil)
	bus.PublishServer(events.ServerConnected, "files", nil)
	expectNothing(t, ch)

	bus.PublishServer(events.ToolsChanged, "files", nil)
	if got := receive(t, ch).Kind; got != events.ToolsChanged {
		t.Errorf("kind = %q, want %q", got, events.ToolsChanged)
	}
}

func TestSubscribeWithNoKindsGetsEverything(t *testing.T) {
	bus := events.NewBus()
	defer bus.Close()

	ch, cancel := bus.Subscribe()
	defer cancel()

	for _, kind := range []events.Kind{events.ToolsChanged, events.ConfigUpdated, events.ServerFailed} {
		bus.Publish(events.Event{Kind: kind})
		if got := receive(t, ch).Kind; got != kind {
			t.Errorf("kind = %q, want %q", got, kind)
		}
	}
}

func TestCancelStopsDelivery(t *testing.T) {
	bus := events.NewBus()
	defer bus.Close()

	ch, cancel := bus.Subscribe()
	cancel()
	// Cancelling twice must not panic on an already-closed channel.
	cancel()

	if _, open := <-ch; open {
		t.Error("the channel delivered a value after cancellation")
	}
	if got := bus.Subscribers(); got != 0 {
		t.Errorf("Subscribers = %d, want 0", got)
	}

	bus.PublishServer(events.ToolsChanged, "files", nil)
}

// The whole point of the bus: a subscriber that stops reading must not
// be able to hold up whatever is publishing. A log viewer nobody is
// watching cannot be allowed to stall a tool call.
func TestASlowSubscriberDoesNotBlockPublishing(t *testing.T) {
	bus := events.NewBus()
	defer bus.Close()

	stuck, cancelStuck := bus.Subscribe()
	defer cancelStuck()
	_ = stuck // deliberately never read

	attentive, cancelAttentive := bus.Subscribe()
	defer cancelAttentive()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			bus.Publish(events.Event{Kind: events.ToolsChanged})
			// Keep the attentive subscriber drained so only the stuck
			// one is behind.
			select {
			case <-attentive:
			default:
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publishing blocked on a subscriber that stopped reading")
	}

	if bus.Dropped() == 0 {
		t.Error("Dropped = 0, but a subscriber must have missed events")
	}
}

// Losses have to be countable, or a stuck subscriber is invisible.
func TestDroppedCountsMissedDeliveries(t *testing.T) {
	bus := events.NewBus()
	defer bus.Close()

	_, cancel := bus.Subscribe()
	defer cancel()

	if got := bus.Dropped(); got != 0 {
		t.Fatalf("Dropped = %d before anything was published", got)
	}
	for i := 0; i < 200; i++ {
		bus.Publish(events.Event{Kind: events.ToolsChanged})
	}
	if got := bus.Dropped(); got == 0 {
		t.Error("Dropped = 0 after overflowing an unread subscriber")
	}
}

func TestCloseEndsEverySubscription(t *testing.T) {
	bus := events.NewBus()

	first, cancelFirst := bus.Subscribe()
	defer cancelFirst()
	second, cancelSecond := bus.Subscribe()
	defer cancelSecond()

	bus.Close()

	for i, ch := range []<-chan events.Event{first, second} {
		if _, open := <-ch; open {
			t.Errorf("subscriber %d is still open after Close", i)
		}
	}
	if got := bus.Subscribers(); got != 0 {
		t.Errorf("Subscribers = %d after Close, want 0", got)
	}
}

// Subsystems shut down in some order, and the ones that shut down after
// the bus must not panic.
func TestPublishingAfterCloseIsHarmless(t *testing.T) {
	bus := events.NewBus()
	bus.Close()
	bus.Close() // also repeatable

	bus.PublishServer(events.ServerDisconnected, "files", nil)

	ch, cancel := bus.Subscribe()
	defer cancel()
	if _, open := <-ch; open {
		t.Error("Subscribe after Close returned an open channel")
	}
}

func TestConcurrentUse(t *testing.T) {
	bus := events.NewBus()
	defer bus.Close()

	var wg sync.WaitGroup

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 200; n++ {
				bus.PublishServer(events.ToolsChanged, "files", nil)
			}
		}()
	}

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, cancel := bus.Subscribe()
			defer cancel()
			for n := 0; n < 50; n++ {
				select {
				case <-ch:
				case <-time.After(50 * time.Millisecond):
					return
				}
			}
		}()
	}

	// Subscriptions coming and going while events flow is the normal
	// state of affairs once browsers connect and disconnect.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 50; n++ {
				_, cancel := bus.Subscribe()
				cancel()
			}
		}()
	}

	wg.Wait()
}
