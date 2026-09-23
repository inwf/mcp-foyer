package api_test

import (
	"log/slog"
	"net/http"
	"net/url"
	"testing"
	"time"

	"mcp-foyer/internal/api"
	"mcp-foyer/internal/logging"
)

type logsResponse struct {
	Entries []api.LogEntry `json:"entries"`
	Servers []string       `json:"servers"`
}

// record puts one entry in the store directly, so a filter test states
// exactly what it is filtering rather than provoking log lines and
// hoping for the right ones.
func record(store *logging.Store, level slog.Level, module, server, message string) {
	store.Append(logging.Entry{
		Time:    time.Now(),
		Level:   level,
		Message: message,
		Module:  module,
		Server:  server,
	})
}

// seeded fills the store with entries covering every filter dimension.
func seeded(t *testing.T) *harness {
	t.Helper()

	h := start(t, nil)
	record(h.Logs, slog.LevelDebug, logging.ModuleUpstream, "files", "a debug line")
	record(h.Logs, slog.LevelInfo, logging.ModuleUpstream, "files", "an info line")
	record(h.Logs, slog.LevelWarn, logging.ModuleGateway, "", "a warning")
	record(h.Logs, slog.LevelError, logging.ModuleUpstream, "notes", "an error")
	return h
}

func TestQueryingLogs(t *testing.T) {
	h := seeded(t)

	var got logsResponse
	decode(t, h.get(t, "/api/logs"), http.StatusOK, &got)

	if len(got.Entries) == 0 {
		t.Fatal("no entries were returned")
	}
	// The level is a name, not slog's number: a client should not have to
	// know that info happens to be zero.
	for _, entry := range got.Entries {
		switch entry.Level {
		case "debug", "info", "warn", "error":
		default:
			t.Errorf("level = %q, want a level name", entry.Level)
		}
	}
}

func TestFilteringLogsByLevel(t *testing.T) {
	h := seeded(t)

	var got logsResponse
	decode(t, h.get(t, "/api/logs?level=warn"), http.StatusOK, &got)

	for _, entry := range got.Entries {
		if entry.Level == "debug" || entry.Level == "info" {
			t.Errorf("a %s entry survived a warn filter: %q", entry.Level, entry.Message)
		}
	}
	if !hasMessage(got.Entries, "a warning") {
		t.Error("the warning is missing")
	}
	if hasMessage(got.Entries, "a debug line") {
		t.Error("a debug line survived a warn filter")
	}
}

func TestFilteringLogsByServer(t *testing.T) {
	h := seeded(t)

	var got logsResponse
	decode(t, h.get(t, "/api/logs?server=files"), http.StatusOK, &got)

	for _, entry := range got.Entries {
		if entry.Server != "files" {
			t.Errorf("entry from %q survived a filter for files: %q", entry.Server, entry.Message)
		}
	}
	if !hasMessage(got.Entries, "an info line") {
		t.Error("an entry for the requested server is missing")
	}
}

func TestFilteringLogsByModule(t *testing.T) {
	h := seeded(t)

	var got logsResponse
	decode(t, h.get(t, "/api/logs?module="+logging.ModuleGateway), http.StatusOK, &got)

	for _, entry := range got.Entries {
		if entry.Module != logging.ModuleGateway {
			t.Errorf("entry from %q survived a gateway filter: %q", entry.Module, entry.Message)
		}
	}
}

// Filters combine, or a user narrowing a busy view gets no narrowing at
// all.
func TestLogFiltersCombine(t *testing.T) {
	h := seeded(t)

	var got logsResponse
	decode(t, h.get(t, "/api/logs?server=files&level=info"), http.StatusOK, &got)

	for _, entry := range got.Entries {
		if entry.Server != "files" {
			t.Errorf("entry from %q survived: %q", entry.Server, entry.Message)
		}
		if entry.Level == "debug" {
			t.Errorf("a debug entry survived: %q", entry.Message)
		}
	}
	if hasMessage(got.Entries, "an error") {
		t.Error("an entry from another server survived")
	}
}

func TestLimitingLogs(t *testing.T) {
	h := start(t, nil)
	for i := 0; i < 50; i++ {
		record(h.Logs, slog.LevelInfo, logging.ModuleAPI, "", "line")
	}

	var got logsResponse
	decode(t, h.get(t, "/api/logs?limit=5"), http.StatusOK, &got)

	if len(got.Entries) != 5 {
		t.Errorf("returned %d entries, want 5", len(got.Entries))
	}
}

// A timestamp filter is what a follow-mode view uses to fetch only what
// it has not seen.
func TestFilteringLogsSinceATime(t *testing.T) {
	h := start(t, nil)

	record(h.Logs, slog.LevelInfo, logging.ModuleAPI, "", "before the mark")
	time.Sleep(10 * time.Millisecond)
	mark := time.Now()
	time.Sleep(10 * time.Millisecond)
	record(h.Logs, slog.LevelInfo, logging.ModuleAPI, "", "after the mark")

	var got logsResponse
	decode(t, h.get(t, "/api/logs?since="+url.QueryEscape(mark.Format(time.RFC3339Nano))), http.StatusOK, &got)

	if hasMessage(got.Entries, "before the mark") {
		t.Error("an entry older than the mark was returned")
	}
	if !hasMessage(got.Entries, "after the mark") {
		t.Error("the entry newer than the mark is missing")
	}
}

// The store knows which servers it holds records for, which is what
// populates the filter menu.
func TestQueryingLogsListsTheServers(t *testing.T) {
	h := seeded(t)

	var got logsResponse
	decode(t, h.get(t, "/api/logs"), http.StatusOK, &got)

	if !containsString(got.Servers, "files") || !containsString(got.Servers, "notes") {
		t.Errorf("servers = %v, want both to appear", got.Servers)
	}
}

// ===== rejected queries =====

// A filter value the store does not understand would silently return
// everything, which reads as "there is nothing wrong".
func TestRejectedLogQueries(t *testing.T) {
	h := seeded(t)

	cases := []string{
		"/api/logs?level=shouting",
		"/api/logs?module=not-a-module",
		"/api/logs?limit=many",
		"/api/logs?limit=0",
		"/api/logs?limit=999999",
		"/api/logs?since=yesterday",
	}
	for _, path := range cases {
		resp := h.get(t, path)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", path, resp.StatusCode)
		}
	}
}

// ===== clearing =====

func TestClearingLogs(t *testing.T) {
	h := seeded(t)

	if resp := h.do(t, http.MethodDelete, "/api/logs", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}

	var got logsResponse
	decode(t, h.get(t, "/api/logs"), http.StatusOK, &got)

	// The request that did the clearing is itself logged, so the view is
	// not empty — but nothing that predates it survives.
	if hasMessage(got.Entries, "an info line") {
		t.Error("an entry from before the clearing survived")
	}
}

func TestClearingOneServersLogs(t *testing.T) {
	h := seeded(t)

	if resp := h.do(t, http.MethodDelete, "/api/logs?server=files", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}

	var got logsResponse
	decode(t, h.get(t, "/api/logs"), http.StatusOK, &got)

	if hasMessage(got.Entries, "an info line") {
		t.Error("the cleared server's entries survived")
	}
	if !hasMessage(got.Entries, "an error") {
		t.Error("clearing one server removed another server's entries")
	}
}

func hasMessage(entries []api.LogEntry, message string) bool {
	for _, entry := range entries {
		if entry.Message == message {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
