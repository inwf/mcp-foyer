// Package testmcp provides a real MCP server for tests to talk to.
//
// Tests that exercise the stdio transport need a genuine child process
// on the other end of a pipe. Rather than depending on npx or a
// checked-in binary, a test binary re-executes itself: [ServeIfRequested]
// notices the environment variable and serves MCP instead of running
// tests, and [ServerConfig] produces a configuration pointing back at the
// same binary.
//
// Tests that exercise the streamable HTTP transport need the same server
// behind an HTTP endpoint instead, which [Handler] provides. It serves
// the identical set of tools and resources, so a test can assert that
// both transports reach the same server rather than two lookalikes.
//
// This keeps the tests hermetic — no server on the public internet, no
// toolchain outside Go — and fast, since there is nothing to build.
package testmcp

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-foyer/internal/config"
)

// ModeEnv names the variable that turns a test binary into an MCP
// server.
const ModeEnv = "MCP_FOYER_TEST_MCP_SERVER_MODE"

// The behaviours a test can ask for.
const (
	// ModeFull declares and serves tools, resources and logging.
	ModeFull = "full"

	// ModeToolsOnly serves tools and declares nothing else, which is
	// what capability-aware refreshing has to cope with.
	ModeToolsOnly = "tools-only"

	// ModeDiscovery adds tools whose names overlap gateway system tools,
	// and whose schema, arguments and results contain business tags.
	ModeDiscovery = "discovery"

	// ModeNoisyStderr writes to standard error before serving.
	ModeNoisyStderr = "noisy-stderr"

	// ModeSlowReady waits before announcing itself, which is the shape of
	// a server that installs or loads something before it can answer.
	ModeSlowReady = "slow-ready"

	// ModeCrash complains on standard error and exits without speaking
	// MCP at all.
	ModeCrash = "crash"
)

// ReadyLine is what [ModeSlowReady] prints once it is about to serve, and
// SlowReadyDelay is how long it takes to get there.
const (
	ReadyLine      = "ready: accepting requests"
	SlowReadyDelay = 400 * time.Millisecond
)

// ServerName and ServerVersion are what the test server reports during
// the handshake.
const (
	ServerName    = "test-server"
	ServerVersion = "9.9.9"
)

// GreetingURI is the resource ModeFull offers.
const GreetingURI = "test://greeting"

// GreetingText is what reading [GreetingURI] returns.
const GreetingText = "hello"

// ServeIfRequested runs the MCP server if the environment asks for it,
// and reports whether it did along with the exit code to use.
//
// Call it from TestMain:
//
//	func TestMain(m *testing.M) {
//		if served, code := testmcp.ServeIfRequested(); served {
//			os.Exit(code)
//		}
//		os.Exit(m.Run())
//	}
func ServeIfRequested() (served bool, code int) {
	mode := os.Getenv(ModeEnv)
	if mode == "" {
		return false, 0
	}
	return true, serve(mode)
}

// ToolNames are the tools this server offers, including the one "grow"
// adds at runtime.
//
// They are named here because a gateway exposes nothing it has not been
// told to expose, so a test that wants these tools forwarded has to list
// them in the configuration. Deriving the list at run time would mean
// asking the server, which is the thing under test.
var ToolNames = []string{"echo", "sleep", "grow", "grown", "fail"}

// ServerConfig returns a stdio server configuration that runs this test
// binary in the given mode, with the common tools exposed. Discovery
// mode's extra tools stay hidden so tests exercise discovery and routing.
func ServerConfig(mode string) (config.MCPServer, error) {
	self, err := os.Executable()
	if err != nil {
		return config.MCPServer{}, fmt.Errorf("locate the test binary: %w", err)
	}
	return config.MCPServer{
		Transport:    config.TransportStdio,
		Command:      self,
		Env:          map[string]string{ModeEnv: mode},
		Enabled:      true,
		Timeout:      20 * time.Second,
		ExposedTools: slices.Clone(ToolNames),
	}, nil
}

// Handler serves the same MCP server over the streamable HTTP transport,
// for tests that put it behind an [net/http/httptest.Server].
//
// One server instance backs every session, which is what a real
// deployment looks like and what makes a tool added at runtime by the
// "grow" tool visible to the client that asked for it.
func Handler(mode string) http.Handler {
	server := newServer(mode)
	return mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server }, nil)
}

func serve(mode string) int {
	if mode == ModeCrash {
		fmt.Fprintln(os.Stderr, "cannot start: the flux capacitor is missing")
		return 3
	}
	if mode == ModeNoisyStderr {
		fmt.Fprintln(os.Stderr, "warming up")
		fmt.Fprintln(os.Stderr, "listening on stdio")
		// A trailing line with no newline is the common shape of output
		// from a process that is interrupted.
		fmt.Fprint(os.Stderr, "no newline at the end")
	}
	if mode == ModeSlowReady {
		fmt.Fprintln(os.Stderr, "starting up, this may take a moment")
		time.Sleep(SlowReadyDelay)
		fmt.Fprintln(os.Stderr, ReadyLine)
	}

	// Run returns once the client closes the connection, which is the
	// normal way this process ends rather than a failure.
	if err := newServer(mode).Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintf(os.Stderr, "test server stopped: %v\n", err)
	}
	return 0
}

// newServer builds the server both transports serve, so that neither can
// drift into testing a different thing from the other.
func newServer(mode string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    ServerName,
		Version: ServerVersion,
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "echo",
		Description: "returns its argument",
	}, echoTool)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "sleep",
		Description: "blocks for a while, to provoke a timeout",
	}, sleepTool)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "grow",
		Description: "adds another tool, which makes the server report a changed tool list",
	}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		mcp.AddTool(server, &mcp.Tool{Name: "grown", Description: "added at runtime"}, echoTool)
		return TextResult("grown"), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "fail",
		Description: "always reports an error, to exercise failure handling",
	}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: "this tool always fails"}},
		}, nil, nil
	})

	if mode == ModeDiscovery {
		for _, name := range []string{"search_tools", "call_tool"} {
			mcp.AddTool(server, &mcp.Tool{
				Name:        name,
				Title:       "Business metadata",
				Description: "returns business metadata unchanged",
				Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
			}, func(_ context.Context, _ *mcp.CallToolRequest, in businessMetadata) (*mcp.CallToolResult, businessMetadata, error) {
				return nil, in, nil
			})
		}
	}

	if mode == ModeFull {
		server.AddResource(
			&mcp.Resource{URI: GreetingURI, Name: "greeting", MIMEType: "text/plain"},
			func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				return &mcp.ReadResourceResult{
					Contents: []*mcp.ResourceContents{
						{URI: GreetingURI, MIMEType: "text/plain", Text: GreetingText},
					},
				}, nil
			})
	}

	return server
}

// EchoInput is the argument shape of the echo tool.
type EchoInput struct {
	Message string `json:"message"`
}

type businessMetadata struct {
	Tags []string `json:"tags"`
}

func echoTool(_ context.Context, _ *mcp.CallToolRequest, in EchoInput) (*mcp.CallToolResult, any, error) {
	return TextResult(in.Message), nil, nil
}

type sleepInput struct {
	Seconds int `json:"seconds"`
}

func sleepTool(ctx context.Context, _ *mcp.CallToolRequest, in sleepInput) (*mcp.CallToolResult, any, error) {
	select {
	case <-time.After(time.Duration(in.Seconds) * time.Second):
		return TextResult("slept"), nil, nil
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

// TextResult builds a tool result carrying one piece of text.
func TextResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}
