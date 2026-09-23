package upstream

import (
	"fmt"
	"net/http"
	"net/url"

	"mcp-foyer/internal/config"
)

// httpClientFor builds the HTTP client used to reach one streamable HTTP
// server.
//
// Both of the settings this handles — custom headers and an outbound
// proxy — end up here rather than on the transport because the MCP SDK's
// streamable client takes an *http.Client and nothing else. Everything
// that has to vary per server therefore has to be expressed as part of
// that client.
func httpClientFor(cfg config.MCPServer) (*http.Client, error) {
	// Cloning the default rather than building a fresh http.Transport
	// keeps the standard connection pooling and, importantly, the
	// standard proxy behaviour: with no proxy configured here, the usual
	// HTTP_PROXY/HTTPS_PROXY/NO_PROXY variables still apply. An explicit
	// proxy in the configuration overrides them for this server alone.
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("the default HTTP transport is %T, not *http.Transport",
			http.DefaultTransport)
	}
	transport = transport.Clone()

	if cfg.Proxy != "" {
		proxy, err := url.Parse(cfg.Proxy)
		if err != nil {
			return nil, fmt.Errorf("parse proxy %q: %w", cfg.Proxy, err)
		}
		transport.Proxy = http.ProxyURL(proxy)
	}

	var rt http.RoundTripper = transport
	if len(cfg.Headers) > 0 {
		rt = &headerRoundTripper{next: rt, headers: cfg.Headers}
	}

	// Deliberately no Client.Timeout. It bounds the whole exchange
	// including reading the body, and the streamable HTTP transport keeps
	// a long-lived GET open to receive server-initiated messages — a
	// client timeout would sever that stream on a fixed schedule and make
	// list-changed notifications stop arriving. Per-request deadlines
	// come from the context at each call site, which is where the
	// server's configured timeout is applied.
	return &http.Client{Transport: rt}, nil
}

// headerRoundTripper adds the configured headers to every request, which
// is how an upstream that wants an API key or a tenant header is reached.
//
// The headers are applied last and overwrite what the SDK set, because
// the point of configuring one is to control what gets sent.
type headerRoundTripper struct {
	next    http.RoundTripper
	headers map[string]string
}

func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// A RoundTripper must not modify the request it is given: the caller
	// still owns it, and the transport may retry with the original.
	clone := req.Clone(req.Context())
	for name, value := range h.headers {
		clone.Header.Set(name, value)
	}
	return h.next.RoundTrip(clone)
}
