package embedding_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/embedding"
)

// answering serves one vector, so that a test asserts on what was asked
// rather than on parsing.
func answering(t *testing.T, body string) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func clientFor(url string) *embedding.Client {
	return embedding.New(config.Embedding{URL: url, Model: "bge-m3", Timeout: 5 * time.Second})
}

// The configured address may be a base address, the versioned base, or
// the endpoint itself. All of them have to reach the same place: guessing
// wrong in either direction is a 404 that reads like a missing service.
func TestAnyAddressShapeReachesTheEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"embedding":[0.25,0.5]}]}`)
	}))
	t.Cleanup(server.Close)

	for _, configured := range []string{
		server.URL,
		server.URL + "/",
		server.URL + "/v1",
		server.URL + "/v1/embeddings",
	} {
		vectors, err := clientFor(configured).Embed(context.Background(), []string{"read a file"})
		if err != nil {
			t.Errorf("address %q: %v", configured, err)
			continue
		}
		if len(vectors) != 1 || len(vectors[0]) != 2 {
			t.Errorf("address %q: got %v, want one vector of two", configured, vectors)
		}
	}
}

func TestEmbedSendsTheModelTheKeyAndTheTexts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Asserted here rather than recorded for later: this runs on the
		// server's goroutine, and testing.T is safe to report from there.
		if got := r.Header.Get("Authorization"); got != "Bearer sk-live" {
			t.Errorf("Authorization = %q, want the key as a bearer token", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}

		var sent struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("decode the request: %v", err)
		}
		if sent.Model != "bge-m3" {
			t.Errorf("model = %q, want bge-m3", sent.Model)
		}
		if len(sent.Input) != 1 || sent.Input[0] != "read a file 读取文件" {
			t.Errorf("input = %v, want the text as given", sent.Input)
		}

		_, _ = io.WriteString(w, `{"data":[{"embedding":[0.1,0.2]}]}`)
	}))
	t.Cleanup(server.Close)

	client := embedding.New(config.Embedding{
		URL: server.URL, APIKey: "sk-live", Model: "bge-m3", Timeout: 5 * time.Second,
	})
	if _, err := client.Embed(context.Background(), []string{"read a file 读取文件"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
}

// A service that wants no key must not be sent an empty bearer token.
func TestAnEmptyKeySendsNoAuthorizationHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want none", got)
		}
		_, _ = io.WriteString(w, `{"data":[{"embedding":[1]}]}`)
	}))
	t.Cleanup(server.Close)

	if _, err := clientFor(server.URL).Embed(context.Background(), []string{"x"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
}

// A refusal has to arrive as the service's own explanation, or the person
// reading it is left to guess between a wrong key and a wrong address.
func TestARefusalCarriesTheServiceExplanation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid api key","type":"invalid_request_error"}}`)
	}))
	t.Cleanup(server.Close)

	_, err := clientFor(server.URL).Embed(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("Embed succeeded against a service that refused")
	}
	if !strings.Contains(err.Error(), "invalid api key") {
		t.Errorf("error %q does not carry the service's explanation", err)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error %q does not say what the service answered", err)
	}
}

// A body that is not an envelope is still reported, bounded: a provider
// behind a broken proxy may answer with a page of HTML.
func TestANonEnvelopeFailureIsReportedAsText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "<html><body>upstream error</body></html>")
	}))
	t.Cleanup(server.Close)

	_, err := clientFor(server.URL).Embed(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("Embed succeeded against a broken service")
	}
	if !strings.Contains(err.Error(), "upstream error") {
		t.Errorf("error %q does not carry what the service said", err)
	}
}

// A count that does not match the request is a service answering the
// wrong question, and pairing vectors with texts from it would silently
// attach one tool's text to another tool's vector.
func TestAMismatchedVectorCountIsRefused(t *testing.T) {
	server := answering(t, `{"data":[{"embedding":[1]},{"embedding":[2]}]}`)

	_, err := clientFor(server.URL).Embed(context.Background(), []string{"one text"})
	if err == nil {
		t.Fatal("Embed accepted two vectors for one input")
	}
	if !strings.Contains(err.Error(), "2") || !strings.Contains(err.Error(), "1") {
		t.Errorf("error %q does not say how many arrived for how many were asked", err)
	}
}

func TestVectorsOfDifferingWidthsAreRefused(t *testing.T) {
	server := answering(t, `{"data":[{"embedding":[1,2]},{"embedding":[3]}]}`)

	_, err := clientFor(server.URL).Embed(context.Background(), []string{"a", "b"})
	if err == nil {
		t.Fatal("Embed accepted vectors of differing widths")
	}
	if !strings.Contains(err.Error(), "width") {
		t.Errorf("error %q does not name the problem", err)
	}
}

func TestAnUnreachableServiceReportsTheTransportError(t *testing.T) {
	server := answering(t, `{"data":[]}`)
	url := server.URL
	server.Close()

	if _, err := clientFor(url).Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("Embed succeeded against a closed port")
	}
}

// Cancelling means the caller stopped waiting; the error has to say so
// rather than look like a service that answered nothing.
func TestACancelledCallStops(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	t.Cleanup(func() { close(release); server.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := clientFor(server.URL).Embed(ctx, []string{"x"}); err == nil {
		t.Fatal("Embed succeeded on a cancelled context")
	}
}
