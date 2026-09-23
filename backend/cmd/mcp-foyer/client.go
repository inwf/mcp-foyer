package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/spf13/pflag"

	"mcp-foyer/internal/api"
	"mcp-foyer/internal/config"
)

// Most of the commands are clients of a running gateway rather than
// things that work on their own: they ask the instance what it is
// currently doing, which a configuration file cannot answer.
//
// The alternative would have been for each command to load the
// configuration and connect to the upstream servers itself. That would
// report a second opinion rather than the truth — a server the running
// instance failed to reach would look healthy to a command that just
// spawned its own copy.

// clientTimeout bounds a management API call. These are local requests
// answered from memory, so the only thing this really guards against is
// a gateway that has wedged.
const clientTimeout = 10 * time.Second

// clientOptions is the shared configuration of every command that talks
// to a running gateway.
type clientOptions struct {
	global *globalOptions

	// address overrides where to look, for the cases the configuration
	// cannot answer: a gateway started with a different config, or one
	// listening on an operating-system assigned port.
	address string
}

func (o *clientOptions) bind(flags *pflag.FlagSet) {
	flags.StringVar(&o.address, "address", "",
		"host:port of the running instance (default: from the configuration)")
}

// gatewayClient reaches a running instance's management API.
type gatewayClient struct {
	base string
	http *http.Client
}

// connect works out where the gateway should be and prepares to call it.
// Nothing is dialled yet: a command that is about to fail on its
// arguments should not first report that the gateway is down.
func (o *clientOptions) connect() (*gatewayClient, error) {
	address, err := o.resolveAddress()
	if err != nil {
		return nil, err
	}
	return &gatewayClient{
		base: "http://" + address,
		http: &http.Client{Timeout: clientTimeout},
	}, nil
}

func (o *clientOptions) resolveAddress() (string, error) {
	if o.address != "" {
		return o.address, nil
	}

	paths, err := config.ResolveDataDir(o.global.DataDir)
	if err != nil {
		return "", err
	}
	cfgPath, err := resolveConfigPath(paths, o.global.ConfigPath)
	if err != nil {
		return "", err
	}
	cfg, err := config.LoadOrDefault(cfgPath)
	if err != nil {
		return "", err
	}

	// Port zero means the gateway asked the operating system for a free
	// one, so the configuration does not record where it ended up. It is
	// reported on startup, which is where the user has to read it from.
	if cfg.Listen.Port == 0 {
		return "", fmt.Errorf("%s sets listen.port to 0, so the port is chosen at startup "+
			"and cannot be read from the configuration; pass --address host:port", cfgPath)
	}

	return net.JoinHostPort(dialableHost(cfg.Listen.Host), strconv.Itoa(cfg.Listen.Port)), nil
}

// dialableHost turns a listen address into one a client can connect to.
//
// A gateway configured to listen on the wildcard address is reachable at
// the loopback address; connecting to 0.0.0.0 as a destination is not
// portable, and "" is not an address at all.
func dialableHost(host string) string {
	switch host {
	case "", "0.0.0.0":
		return "127.0.0.1"
	case "::", "[::]":
		return "::1"
	default:
		return host
	}
}

// get calls the management API and decodes the result into out.
func (c *gatewayClient) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

// post calls the management API with a JSON body. A nil body sends none,
// which is what the action endpoints expect.
func (c *gatewayClient) post(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPost, path, body, out)
}

func (c *gatewayClient) do(ctx context.Context, method, path string, body, out any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode the request: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(ctx, method, c.base+api.APIPrefix+path, payload)
	if err != nil {
		return fmt.Errorf("build the request: %w", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := c.http.Do(request)
	if err != nil {
		return c.explainTransportFailure(err)
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return c.explainRefusal(response)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(out); err != nil {
		return fmt.Errorf("mcp-foyer at %s sent a response this build cannot read: %w", c.base, err)
	}
	return nil
}

// explainTransportFailure turns a dial failure into the sentence a user
// needs.
//
// "connection refused" on its own is the single most common thing to see
// here, and on its own it does not say what was being connected to or
// why it might be down.
func (c *gatewayClient) explainTransportFailure(err error) error {
	var refused *net.OpError
	if errors.As(err, &refused) && !errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("no mcp-foyer is listening at %s — start one with \"mcp-foyer serve\", "+
			"or pass --address if it is somewhere else", c.base)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("mcp-foyer at %s did not answer within %s", c.base, clientTimeout)
	}
	// A URL error wraps the whole request; its message repeats the method
	// and address, which the sentence above already says better.
	var asURL *url.Error
	if errors.As(err, &asURL) {
		return fmt.Errorf("cannot reach mcp-foyer at %s: %w", c.base, asURL.Err)
	}
	return fmt.Errorf("cannot reach mcp-foyer at %s: %w", c.base, err)
}

// explainRefusal reports what the gateway said it objected to, which is
// far more useful than the status code.
func (c *gatewayClient) explainRefusal(response *http.Response) error {
	var envelope api.Envelope
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil || envelope.Error.Message == "" {
		return fmt.Errorf("mcp-foyer at %s answered %s", c.base, response.Status)
	}

	message := envelope.Error.Message
	for _, field := range envelope.Error.Fields {
		message += fmt.Sprintf("\n  %s: %s", field.Field, field.Message)
	}
	return errors.New(message)
}
