package api_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mcp-foyer/internal/api"
	"mcp-foyer/internal/config"
	"mcp-foyer/internal/logging"
)

// listening starts an API behind a connection-limited listener.
func listening(t *testing.T, maxConnections int) (*httptest.Server, *logging.Store) {
	t.Helper()

	store := logging.NewStore(200)
	log, err := logging.New(logging.Options{Level: slog.LevelDebug, Store: store})
	if err != nil {
		t.Fatalf("build the logger: %v", err)
	}
	t.Cleanup(func() { log.Close() })

	security := config.Default().Security
	security.MaxConnections = maxConnections

	built, err := api.New(api.Options{
		Version:  "test",
		Logger:   log.For(logging.ModuleAPI),
		Security: security,
		Configs:  configs(t, nil),
		Logs:     store,
	})
	if err != nil {
		t.Fatalf("build the api: %v", err)
	}

	server := httptest.NewUnstartedServer(built.Handler())
	server.Listener = built.Listen(server.Listener)
	server.Start()
	t.Cleanup(server.Close)

	return server, store
}

// hold opens a connection, completes one request on it and leaves it
// open. Completing a request is what makes it certain the server has
// accepted and counted the connection.
func hold(t *testing.T, server *httptest.Server) (net.Conn, *bufio.Reader) {
	t.Helper()

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	reader := bufio.NewReader(conn)
	if status := request(t, conn, reader); status != http.StatusOK {
		t.Fatalf("the holding request was answered with %d, want 200", status)
	}
	return conn, reader
}

// request sends a keep-alive request on conn and returns the status, or
// 0 if the connection was closed instead of answered.
func request(t *testing.T, conn net.Conn, reader *bufio.Reader) int {
	t.Helper()

	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := fmt.Fprint(conn, "GET /api/health HTTP/1.1\r\nHost: localhost\r\n\r\n"); err != nil {
		return 0
	}

	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		return 0
	}
	// The body has to be drained, or the next request on this connection
	// would read the leftovers as its response.
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

// A connection beyond the limit is closed at once rather than left in
// the backlog, so a client learns the server is busy instead of waiting
// for a timeout it cannot distinguish from an unreachable host.
func TestAConnectionOverTheLimitIsRefused(t *testing.T) {
	server, store := listening(t, 2)

	hold(t, server)
	hold(t, server)

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		// A refusal at the TCP layer is an equally clear answer.
		return
	}
	defer conn.Close()

	if status := request(t, conn, bufio.NewReader(conn)); status != 0 {
		t.Errorf("a third connection was answered with %d, want it refused", status)
	}
	if text := logText(store); !contains(text, "limit is reached") {
		t.Errorf("the refusal was not logged:\n%s", text)
	}
}

// This is the drift case: a connection that closes has to give its slot
// back, or the effective limit ratchets down to zero and the server
// stops answering.
func TestASlotIsReturnedWhenAConnectionCloses(t *testing.T) {
	server, _ := listening(t, 2)

	first, _ := hold(t, server)
	second, _ := hold(t, server)

	first.Close()
	second.Close()

	// The server notices the close asynchronously.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("tcp", server.Listener.Addr().String())
		if err != nil {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		status := request(t, conn, bufio.NewReader(conn))
		conn.Close()
		if status == http.StatusOK {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the server never accepted a connection again; closed connections did not free their slots")
}

// Serving many requests in sequence must not exhaust the limit, which it
// would if a slot were taken per request rather than per connection.
func TestManyRequestsOnOneConnectionUseOneSlot(t *testing.T) {
	server, _ := listening(t, 1)

	conn, reader := hold(t, server)
	for i := 0; i < 20; i++ {
		if status := request(t, conn, reader); status != http.StatusOK {
			t.Fatalf("request %d on the same connection was answered with %d, want 200", i, status)
		}
	}
}

// The count is what the health endpoint reports, and an operator uses it
// to tell a busy server from a wedged one.
func TestHealthReportsTheOpenConnections(t *testing.T) {
	server, _ := listening(t, 10)

	hold(t, server)
	hold(t, server)

	resp, err := server.Client().Get(server.URL + "/api/health")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	var health api.Health
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Two held connections plus this request's own.
	if health.Connections < 3 {
		t.Errorf("connections = %d, want at least 3", health.Connections)
	}
}

// A configuration with no limit must not impose one.
func TestNoConnectionLimitMeansNoLimit(t *testing.T) {
	server, _ := listening(t, 0)

	for i := 0; i < 12; i++ {
		hold(t, server)
	}
	if resp, err := server.Client().Get(server.URL + "/api/health"); err != nil {
		t.Errorf("GET after 12 held connections: %v", err)
	} else {
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
	}
}
