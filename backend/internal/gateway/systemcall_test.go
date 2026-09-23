package gateway_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"mcp-foyer/internal/gateway"
)

// The gateway's own tools belong to no upstream server, so the management
// API cannot reach them the way it reaches a forwarded one. Calling them
// goes through the gateway's own MCP server instead, which is what makes
// the answer the same one a model would get.

func TestASystemToolCanBeCalled(t *testing.T) {
	_, g := gatewayOn(t, twoServers(), nil)

	result, err := g.CallSystemTool(t.Context(), gateway.ToolListServers, nil)
	if err != nil {
		t.Fatalf("call %s: %v", gateway.ToolListServers, err)
	}
	if result.IsError {
		t.Fatalf("%s reported an error: %s", gateway.ToolListServers, resultText(result))
	}

	// The fixture has a connected server and a failed one, and a working
	// call reports both.
	answer := resultText(result) + fmt.Sprint(result.StructuredContent)
	for _, want := range []string{"files", "broken"} {
		if !strings.Contains(answer, want) {
			t.Errorf("the answer does not mention %q:\n%s", want, answer)
		}
	}
}

// Arguments have to reach the tool, typed as its schema declares.
func TestASystemToolReceivesItsArguments(t *testing.T) {
	_, g := gatewayOn(t, twoServers(), nil)

	result, err := g.CallSystemTool(t.Context(), gateway.ToolSearchTools,
		map[string]any{"server": "files"})
	if err != nil {
		t.Fatalf("call %s: %v", gateway.ToolSearchTools, err)
	}
	if result.IsError {
		t.Fatalf("%s reported an error: %s", gateway.ToolSearchTools, resultText(result))
	}

	answer := resultText(result) + fmt.Sprint(result.StructuredContent)
	for _, want := range []string{"read", "write"} {
		if !strings.Contains(answer, want) {
			t.Errorf("the answer omits the tool %q:\n%s", want, answer)
		}
	}
}

// A tool that ran and reported a problem is a successful call with a
// failed result: the caller needs the message, not a transport error.
func TestASystemToolsOwnErrorComesBackAsAResult(t *testing.T) {
	_, g := gatewayOn(t, twoServers(), nil)

	result, err := g.CallSystemTool(t.Context(), gateway.ToolSearchTools,
		map[string]any{"server": "nowhere"})
	if err != nil {
		t.Fatalf("call %s: %v", gateway.ToolSearchTools, err)
	}
	if !result.IsError {
		t.Fatal("asking about a server that does not exist was reported as a success")
	}
	if message := resultText(result); !strings.Contains(message, "nowhere") {
		t.Errorf("the message does not name the server that was asked for: %q", message)
	}
}

// An argument the schema rejects has to be refused rather than passed on
// as whatever it happened to be, which is the reason for routing the call
// through the server instead of at the Go function behind the tool.
func TestASystemToolRejectsAnArgumentOfTheWrongType(t *testing.T) {
	_, g := gatewayOn(t, twoServers(), nil)

	result, err := g.CallSystemTool(t.Context(), gateway.ToolSearchTools,
		map[string]any{"server": 42})

	// The SDK may refuse this as a protocol error or as a failed result,
	// and either is a refusal. Silently accepting it is not.
	switch {
	case err != nil:
	case result.IsError:
	default:
		t.Errorf("a number was accepted where the schema asks for a string: %s", resultText(result))
	}
}

// Only the gateway's own tools go through this path. A forwarded tool
// reaching it would bypass the exposure rules the configuration sets.
func TestOnlyTheGatewaysOwnToolsAreCallableThisWay(t *testing.T) {
	_, g := gatewayOn(t, twoServers(), nil)

	_, err := g.CallSystemTool(t.Context(), "files_read", nil)
	if err == nil {
		t.Fatal("a forwarded tool was accepted as one of the gateway's own")
	}
	// The message has to say what is callable, or the caller is left
	// guessing at the difference between the two kinds of tool.
	if !strings.Contains(err.Error(), gateway.ToolListServers) {
		t.Errorf("the message does not say what is callable: %v", err)
	}
}

// ===== the gateway talking to itself is not a client =====

// Reading the tools back and calling one both open a session to the
// gateway's own server. Neither may be reported as a connected client:
// that list is where an operator looks to see who is connected, and a
// gateway that counts itself makes the answer wrong.
func TestTheGatewayTalkingToItselfIsNotReportedAsAClient(t *testing.T) {
	_, g := gatewayOn(t, twoServers(), nil)

	if before := len(g.Sessions()); before != 0 {
		t.Fatalf("%d sessions before anything connected", before)
	}

	g.SystemTools()
	if after := len(g.Sessions()); after != 0 {
		t.Errorf("%d sessions after reading the tools back", after)
	}

	if _, err := g.CallSystemTool(t.Context(), gateway.ToolListServers, nil); err != nil {
		t.Fatalf("call %s: %v", gateway.ToolListServers, err)
	}
	if after := len(g.Sessions()); after != 0 {
		t.Errorf("%d sessions after calling one of the gateway's own tools", after)
	}
}

// The check above only looks before and after. This one reads the session
// list while calls are in flight, which is the only time a session the
// gateway opened to itself exists on the server at all.
func TestAnInternalSessionIsInvisibleWhileItIsOpen(t *testing.T) {
	url, g := gatewayOn(t, twoServers(), nil)

	stop := make(chan struct{})
	peak := make(chan int, 1)

	go func() {
		worst := 0
		for {
			select {
			case <-stop:
				peak <- worst
				return
			default:
			}
			if count := len(g.Sessions()); count > worst {
				worst = count
			}
		}
	}()

	// Many calls, because what is being looked for is the moment between
	// the SDK registering a session and the gateway marking it as its own.
	// One call would probably miss it.
	for range 200 {
		if _, err := g.CallSystemTool(t.Context(), gateway.ToolListServers, nil); err != nil {
			close(stop)
			<-peak
			t.Fatalf("call %s: %v", gateway.ToolListServers, err)
		}
	}
	close(stop)

	if worst := <-peak; worst != 0 {
		t.Errorf("the session list reached %d while calls were in flight;"+
			" a session the gateway opened to itself was reported as a client", worst)
	}

	// Zero would also be what a broken probe reports, so the same reading
	// has to be shown to see a session that really is one.
	clientOn(t, url, nil)
	waitFor(t, func() bool { return len(g.Sessions()) > 0 },
		"a connected client never appeared in the session list, so the check above proves nothing")
}

// waitFor polls until the condition holds, and fails with why if it never
// does.
func waitFor(t *testing.T, condition func() bool, why string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-t.Context().Done():
			t.Fatal(context.Cause(t.Context()))
		}
	}
	t.Fatal(why)
}
