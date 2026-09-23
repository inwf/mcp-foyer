package logging_test

import (
	"log/slog"
	"slices"
	"testing"
	"time"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/logging"
)

func entry(at time.Time, level slog.Level, msg, module, server string) logging.Entry {
	return logging.Entry{Time: at, Level: level, Message: msg, Module: module, Server: server}
}

func messages(entries []logging.Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Message
	}
	return out
}

func TestStoreKeepsRecordsInOrder(t *testing.T) {
	base := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	store := logging.NewStore(10)

	for i := 0; i < 3; i++ {
		store.Append(entry(base.Add(time.Duration(i)*time.Second),
			slog.LevelInfo, string(rune('a'+i)), logging.ModuleAPI, ""))
	}

	if got, want := messages(store.Query(logging.Query{})), []string{"a", "b", "c"}; !equal(got, want) {
		t.Errorf("Query = %v, want %v", got, want)
	}
}

// Once the ring is full the oldest record is the one that goes.
func TestStoreEvictsOldestWhenFull(t *testing.T) {
	base := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	store := logging.NewStore(3)

	for i := 0; i < 5; i++ {
		store.Append(entry(base.Add(time.Duration(i)*time.Second),
			slog.LevelInfo, string(rune('a'+i)), logging.ModuleAPI, ""))
	}

	got := messages(store.Query(logging.Query{}))
	if want := []string{"c", "d", "e"}; !equal(got, want) {
		t.Errorf("Query = %v, want %v", got, want)
	}
}

// One noisy server must not push another server's history out. That
// history is exactly what is wanted when the noisy one is misbehaving.
func TestOneServerCannotEvictAnother(t *testing.T) {
	base := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	store := logging.NewStore(5)

	store.Append(entry(base, slog.LevelInfo, "quiet server started", logging.ModuleUpstream, "quiet"))
	for i := 0; i < 50; i++ {
		store.Append(entry(base.Add(time.Duration(i+1)*time.Second),
			slog.LevelInfo, "chatter", logging.ModuleUpstream, "noisy"))
	}

	quiet := store.Query(logging.Query{Server: "quiet"})
	if len(quiet) != 1 || quiet[0].Message != "quiet server started" {
		t.Errorf("the quiet server's log is %v, want its single record intact", messages(quiet))
	}
}

func TestQueryFiltersByLevel(t *testing.T) {
	base := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	store := logging.NewStore(10)
	store.Append(entry(base, slog.LevelDebug, "debug", logging.ModuleAPI, ""))
	store.Append(entry(base.Add(time.Second), slog.LevelInfo, "info", logging.ModuleAPI, ""))
	store.Append(entry(base.Add(2*time.Second), slog.LevelError, "error", logging.ModuleAPI, ""))

	got := messages(store.Query(logging.Query{MinLevel: config.LevelInfo}))
	if want := []string{"info", "error"}; !equal(got, want) {
		t.Errorf("Query = %v, want %v", got, want)
	}
}

// slog.LevelInfo is zero, so a level field typed as slog.Level cannot
// tell "no filter" from "info and above" — an empty query would silently
// hide every debug record. The level is named rather than numeric for
// exactly this reason.
func TestEmptyQueryReturnsEveryLevel(t *testing.T) {
	base := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	store := logging.NewStore(10)
	store.Append(entry(base, slog.LevelDebug, "debug", logging.ModuleAPI, ""))
	store.Append(entry(base.Add(time.Second), slog.LevelInfo, "info", logging.ModuleAPI, ""))

	got := messages(store.Query(logging.Query{}))
	if want := []string{"debug", "info"}; !equal(got, want) {
		t.Errorf("Query = %v, want %v; debug records were dropped", got, want)
	}
}

// A malformed level in a query parameter must not read as an empty log.
func TestUnknownLevelFiltersNothing(t *testing.T) {
	store := logging.NewStore(10)
	store.Append(entry(time.Now(), slog.LevelDebug, "debug", logging.ModuleAPI, ""))
	store.Append(entry(time.Now(), slog.LevelError, "error", logging.ModuleAPI, ""))

	if got := store.Query(logging.Query{MinLevel: "loud"}); len(got) != 2 {
		t.Errorf("Query = %v, want every record", messages(got))
	}
}

func TestQueryFiltersByModule(t *testing.T) {
	base := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	store := logging.NewStore(10)
	store.Append(entry(base, slog.LevelInfo, "from api", logging.ModuleAPI, ""))
	store.Append(entry(base.Add(time.Second), slog.LevelInfo, "from gateway", logging.ModuleGateway, ""))

	got := messages(store.Query(logging.Query{Module: logging.ModuleGateway}))
	if want := []string{"from gateway"}; !equal(got, want) {
		t.Errorf("Query = %v, want %v", got, want)
	}
}

func TestQueryFiltersBySince(t *testing.T) {
	base := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	store := logging.NewStore(10)
	for i := 0; i < 4; i++ {
		store.Append(entry(base.Add(time.Duration(i)*time.Minute),
			slog.LevelInfo, string(rune('a'+i)), logging.ModuleAPI, ""))
	}

	got := messages(store.Query(logging.Query{Since: base.Add(time.Minute)}))
	if want := []string{"c", "d"}; !equal(got, want) {
		t.Errorf("Query = %v, want %v", got, want)
	}
}

// A log view shows the tail, so capping must keep the newest records.
func TestQueryLimitKeepsTheNewest(t *testing.T) {
	base := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	store := logging.NewStore(10)
	for i := 0; i < 5; i++ {
		store.Append(entry(base.Add(time.Duration(i)*time.Second),
			slog.LevelInfo, string(rune('a'+i)), logging.ModuleAPI, ""))
	}

	got := messages(store.Query(logging.Query{Limit: 2}))
	if want := []string{"d", "e"}; !equal(got, want) {
		t.Errorf("Query = %v, want %v", got, want)
	}
}

func TestQueryForAnUnknownServerIsEmpty(t *testing.T) {
	store := logging.NewStore(10)
	store.Append(entry(time.Now(), slog.LevelInfo, "hello", logging.ModuleAPI, "known"))

	if got := store.Query(logging.Query{Server: "unknown"}); len(got) != 0 {
		t.Errorf("Query = %v, want empty", messages(got))
	}
}

// A server-scoped record belongs in both that server's view and the
// combined one.
func TestServerRecordsAlsoAppearGlobally(t *testing.T) {
	store := logging.NewStore(10)
	store.Append(entry(time.Now(), slog.LevelInfo, "scoped", logging.ModuleUpstream, "files"))

	if got := store.Query(logging.Query{}); len(got) != 1 {
		t.Errorf("global query = %v, want the record", messages(got))
	}
	if got := store.Query(logging.Query{Server: "files"}); len(got) != 1 {
		t.Errorf("server query = %v, want the record", messages(got))
	}
}

func TestClear(t *testing.T) {
	store := logging.NewStore(10)
	store.Append(entry(time.Now(), slog.LevelInfo, "a", logging.ModuleUpstream, "one"))
	store.Append(entry(time.Now(), slog.LevelInfo, "b", logging.ModuleUpstream, "two"))

	t.Run("clearing one server leaves the others", func(t *testing.T) {
		store.Clear("one")
		if got := store.Query(logging.Query{Server: "one"}); len(got) != 0 {
			t.Errorf("cleared server still has %v", messages(got))
		}
		if got := store.Query(logging.Query{Server: "two"}); len(got) != 1 {
			t.Errorf("other server lost its records: %v", messages(got))
		}
	})

	t.Run("clearing everything empties the global view too", func(t *testing.T) {
		store.Clear("")
		if got := store.Query(logging.Query{}); len(got) != 0 {
			t.Errorf("global view still has %v", messages(got))
		}
	})
}

// Clearing one server has to remove its records from the shared view as
// well as from its own. If they lingered there, clearing a server's log
// in the UI would look like it had done nothing the moment the view was
// switched back to all servers.
func TestClearingOneServerAlsoClearsItFromTheGlobalView(t *testing.T) {
	store := logging.NewStore(10)
	store.Append(entry(time.Now(), slog.LevelInfo, "from one", logging.ModuleUpstream, "one"))
	store.Append(entry(time.Now(), slog.LevelInfo, "from two", logging.ModuleUpstream, "two"))
	store.Append(entry(time.Now(), slog.LevelInfo, "from nowhere", logging.ModuleAPI, ""))

	store.Clear("one")

	got := messages(store.Query(logging.Query{}))
	for _, unwanted := range []string{"from one"} {
		if slices.Contains(got, unwanted) {
			t.Errorf("the global view still has %q: %v", unwanted, got)
		}
	}
	// Everything else survives, including records tied to no server.
	for _, wanted := range []string{"from two", "from nowhere"} {
		if !slices.Contains(got, wanted) {
			t.Errorf("clearing one server also dropped %q: %v", wanted, got)
		}
	}
}

// The order of what remains is what a log view reads top to bottom.
func TestClearingOneServerKeepsTheOrderOfTheRest(t *testing.T) {
	store := logging.NewStore(10)
	base := time.Now()
	store.Append(entry(base, slog.LevelInfo, "first", logging.ModuleAPI, ""))
	store.Append(entry(base.Add(time.Millisecond), slog.LevelInfo, "noise", logging.ModuleUpstream, "chatty"))
	store.Append(entry(base.Add(2*time.Millisecond), slog.LevelInfo, "second", logging.ModuleAPI, ""))

	store.Clear("chatty")

	if got := messages(store.Query(logging.Query{})); !equal(got, []string{"first", "second"}) {
		t.Errorf("remaining entries = %v, want them in their original order", got)
	}
}

func TestServersLists(t *testing.T) {
	store := logging.NewStore(10)
	store.Append(entry(time.Now(), slog.LevelInfo, "x", logging.ModuleUpstream, "zulu"))
	store.Append(entry(time.Now(), slog.LevelInfo, "y", logging.ModuleUpstream, "alpha"))
	store.Append(entry(time.Now(), slog.LevelInfo, "z", logging.ModuleAPI, ""))

	got := store.Servers()
	if want := []string{"alpha", "zulu"}; !equal(got, want) {
		t.Errorf("Servers = %v, want %v", got, want)
	}
}

// Attributes bound with With must survive alongside those passed at the
// call site.
func TestStoreCapturesAttributesFromBothSources(t *testing.T) {
	store := logging.NewStore(10)
	log, err := logging.New(logging.Options{Level: slog.LevelInfo, Store: store})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer log.Close()

	log.For(logging.ModuleUpstream).With("bound", "1").Info("hello", "inline", "2")

	entries := store.Query(logging.Query{})
	if len(entries) != 1 {
		t.Fatalf("retained %d entries, want 1", len(entries))
	}
	got := entries[0]
	if got.Module != logging.ModuleUpstream {
		t.Errorf("module = %q, want upstream", got.Module)
	}
	if got.Attrs["bound"] != "1" {
		t.Errorf("bound attribute = %q, want 1", got.Attrs["bound"])
	}
	if got.Attrs["inline"] != "2" {
		t.Errorf("inline attribute = %q, want 2", got.Attrs["inline"])
	}
}

// Deriving two loggers from one must not let their attributes bleed into
// each other, which is easy to get wrong when a slice is shared.
func TestDerivedLoggersDoNotShareAttributes(t *testing.T) {
	store := logging.NewStore(10)
	log, err := logging.New(logging.Options{Level: slog.LevelInfo, Store: store})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer log.Close()

	base := log.For(logging.ModuleUpstream)
	first := base.With("only_in_first", "yes")
	second := base.With("only_in_second", "yes")

	first.Info("first")
	second.Info("second")

	entries := store.Query(logging.Query{})
	if len(entries) != 2 {
		t.Fatalf("retained %d entries, want 2", len(entries))
	}
	if _, leaked := entries[0].Attrs["only_in_second"]; leaked {
		t.Errorf("the first record carries the second logger's attribute: %v", entries[0].Attrs)
	}
	if _, leaked := entries[1].Attrs["only_in_first"]; leaked {
		t.Errorf("the second record carries the first logger's attribute: %v", entries[1].Attrs)
	}
}

func TestNewStoreRejectsANonPositiveCapacity(t *testing.T) {
	for _, capacity := range []int{0, -1} {
		store := logging.NewStore(capacity)
		store.Append(entry(time.Now(), slog.LevelInfo, "survives", logging.ModuleAPI, ""))
		if got := store.Query(logging.Query{}); len(got) != 1 {
			t.Errorf("NewStore(%d) retained %d entries, want 1", capacity, len(got))
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
