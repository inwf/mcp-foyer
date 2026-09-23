package main

import (
	"strings"
	"testing"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/gateway"
	"mcp-foyer/internal/testmcp"
)

// withTestServer starts a gateway with the test MCP server attached under
// the given name and waits until its tools have been listed.
func withTestServer(t *testing.T, name string) (address string, cleanup func()) {
	t.Helper()

	base, stop, done := running(t, func(cfg *config.Config) {
		upstream, err := testmcp.ServerConfig(testmcp.ModeFull)
		if err != nil {
			t.Fatalf("build the upstream configuration: %v", err)
		}
		cfg.MCPServers = map[string]config.MCPServer{name: upstream}
	})

	address = hostPort(t, base)
	waitForState(t, address, name, "connected")
	return address, func() { stop(); <-done }
}

// The list is only what is exposed, and on an ordinary installation that
// is a small part of what exists. Presenting it as the whole would send
// someone looking for a tool that is there all along.
func TestToolsListSaysWhatItLeftOut(t *testing.T) {
	address, cleanup := exposingOneTool(t)
	defer cleanup()

	code, stdout, stderr := execute(t, "tools", "list", "--address", address)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}

	if !strings.Contains(stdout, "probe_echo") {
		t.Errorf("the exposed tool is missing:\n%s", stdout)
	}
	if !strings.Contains(stdout, "not exposed") {
		t.Errorf("nothing says the list is partial:\n%s", stdout)
	}
	// And it says how to reach them, rather than leaving the reader stuck.
	if !strings.Contains(stdout, "call_tool") {
		t.Errorf("no way out is offered:\n%s", stdout)
	}
	// And where to see what they are, which is a different question from
	// how to call one.
	if !strings.Contains(stdout, "tools list --all") {
		t.Errorf("the command that lists them is not named:\n%s", stdout)
	}
}

// The list that decides what to expose has to show what there is to
// choose from. Without this the CLI could report a count of hidden tools
// and offer no way at all to find out what they were.
func TestToolsListAllShowsTheUnexposedOnes(t *testing.T) {
	address, cleanup := exposingOneTool(t)
	defer cleanup()

	code, stdout, stderr := execute(t, "tools", "list", "--all", "--address", address)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}

	// Every tool the server has, whether or not it is on offer. "grown" is
	// not among them: that one only exists after "grow" has been called.
	for _, name := range []string{"echo", "sleep", "grow", "fail"} {
		if !strings.Contains(stdout, name) {
			t.Errorf("%q is missing from the full list:\n%s", name, stdout)
		}
	}

	// The exposed one carries the name a client would call it by, and the
	// rest say plainly that they have none.
	if row := rowFor(t, stdout, "echo"); !strings.Contains(row, "probe_echo") {
		t.Errorf("the exposed name is missing from the row: %q", row)
	}
	if row := rowFor(t, stdout, "sleep"); !strings.Contains(row, "-") {
		t.Errorf("an unexposed tool does not say so: %q", row)
	}
}

// A dash in a column reads as missing data unless something says what it
// means, and what it means here is a decision someone can reverse.
func TestToolsListAllExplainsWhatIsNotExposed(t *testing.T) {
	address, cleanup := exposingOneTool(t)
	defer cleanup()

	_, stdout, _ := execute(t, "tools", "list", "--all", "--address", address)

	if !strings.Contains(stdout, "not exposed") {
		t.Errorf("nothing explains the dashes:\n%s", stdout)
	}
	if !strings.Contains(stdout, "call_tool") {
		t.Errorf("no way to reach them is offered:\n%s", stdout)
	}
	// The count of hidden tools belongs to the exposed-only list; here they
	// are all on screen, so repeating "N more are not listed" would be false.
	if strings.Contains(stdout, "not in the list above") {
		t.Errorf("the full list claims to have left something out:\n%s", stdout)
	}
}

// The two flags answer different questions and have to compose.
func TestToolsListAllStillNarrowsToOneServer(t *testing.T) {
	base, stop, done := running(t, func(cfg *config.Config) {
		server, err := testmcp.ServerConfig(testmcp.ModeFull)
		if err != nil {
			t.Fatalf("build the upstream configuration: %v", err)
		}
		server.ExposedTools = nil
		cfg.MCPServers = map[string]config.MCPServer{"one": server, "two": server}
	})
	defer func() { stop(); <-done }()

	address := hostPort(t, base)
	waitForState(t, address, "one", "connected")
	waitForState(t, address, "two", "connected")

	code, stdout, stderr := execute(t, "tools", "list", "--all",
		"--server", "one", "--address", address)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}

	if !strings.Contains(stdout, "one") {
		t.Errorf("the server that was asked for is missing:\n%s", stdout)
	}
	if strings.Contains(stdout, "two") {
		t.Errorf("a tool from another server survived the filter:\n%s", stdout)
	}
}

// exposingOneTool starts a gateway whose only server offers everything and
// exposes one thing, which is the ordinary state of an installation someone
// has begun to configure.
func exposingOneTool(t *testing.T) (address string, cleanup func()) {
	t.Helper()

	base, stop, done := running(t, func(cfg *config.Config) {
		server, err := testmcp.ServerConfig(testmcp.ModeFull)
		if err != nil {
			t.Fatalf("build the upstream configuration: %v", err)
		}
		server.ExposedTools = []string{"echo"}
		cfg.MCPServers = map[string]config.MCPServer{"probe": server}
	})

	address = hostPort(t, base)
	waitForState(t, address, "probe", "connected")
	return address, func() { stop(); <-done }
}

// With everything exposed there is nothing left out, and a note saying so
// would be noise.
func TestToolsListIsQuietWhenNothingIsHidden(t *testing.T) {
	address, cleanup := withTestServer(t, "probe")
	defer cleanup()

	_, stdout, _ := execute(t, "tools", "list", "--address", address)

	if strings.Contains(stdout, "not exposed") {
		t.Errorf("a note about hidden tools appears with none hidden:\n%s", stdout)
	}
}

func TestToolsListShowsTheExposedNames(t *testing.T) {
	address, cleanup := withTestServer(t, "probe")
	defer cleanup()

	code, stdout, stderr := execute(t, "tools", "list", "--address", address)

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	for _, want := range []string{"NAME", "SERVER", "probe_echo", "probe"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output does not contain %q:\n%s", want, stdout)
		}
	}
	// The exposed name is what a client calls, so the bare name alone
	// would be the wrong thing to print.
	row := rowFor(t, stdout, "probe_echo")
	if !strings.Contains(row, "returns its argument") {
		t.Errorf("the description is missing from the row: %q", row)
	}
}

func TestToolsListCanFilterByServer(t *testing.T) {
	address, cleanup := withTestServer(t, "probe")
	defer cleanup()

	code, stdout, _ := execute(t, "tools", "list", "--server", "nowhere", "--address", address)

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
	if !strings.Contains(stdout, "nowhere") {
		t.Errorf("output does not name the server that has nothing:\n%s", stdout)
	}
	if strings.Contains(stdout, "probe_echo") {
		t.Errorf("a tool from another server survived the filter:\n%s", stdout)
	}
}

func TestToolsListSearchRanksMatches(t *testing.T) {
	address, cleanup := withTestServer(t, "probe")
	defer cleanup()

	code, stdout, stderr := execute(t, "tools", "list", "--search", "echo", "--address", address)

	if code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "probe_echo") {
		t.Errorf("the matching tool is missing:\n%s", stdout)
	}
}

func TestToolsCallReturnsWhatTheToolSaid(t *testing.T) {
	address, cleanup := withTestServer(t, "probe")
	defer cleanup()

	code, stdout, stderr := execute(t, "tools", "call", "probe_echo",
		"--arg", "message=hello from the cli", "--address", address)

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	if !strings.Contains(stdout, "hello from the cli") {
		t.Errorf("the tool's answer is missing:\n%s", stdout)
	}
}

// The bare tool name is a convenience, and it has to reach the same tool
// as the exposed name.
func TestToolsCallAcceptsTheBareToolName(t *testing.T) {
	address, cleanup := withTestServer(t, "probe")
	defer cleanup()

	code, stdout, stderr := execute(t, "tools", "call", "echo",
		"--arg", "message=bare", "--address", address)

	if code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "bare") {
		t.Errorf("the tool's answer is missing:\n%s", stdout)
	}
}

// Exposure decides what a client is offered, not what exists. This is the
// CLI's half of that: a tool nothing has exposed is still callable, which
// is what makes "expose nothing by default" a sane default rather than a
// wall.
func TestToolsCallReachesAnUnexposedTool(t *testing.T) {
	base, stop, done := running(t, func(cfg *config.Config) {
		server, err := testmcp.ServerConfig(testmcp.ModeFull)
		if err != nil {
			t.Fatalf("build the upstream configuration: %v", err)
		}
		server.ExposedTools = nil
		cfg.MCPServers = map[string]config.MCPServer{"probe": server}
	})
	defer func() { stop(); <-done }()

	address := hostPort(t, base)
	waitForState(t, address, "probe", "connected")

	code, stdout, stderr := execute(t, "tools", "call", "probe/echo",
		"--arg", "message=through the back door", "--address", address)

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	if !strings.Contains(stdout, "through the back door") {
		t.Errorf("the tool's answer is missing:\n%s", stdout)
	}
}

// A suggestion has to be something that can be typed back in. An unexposed
// tool has no exposed name, so listing those names would offer blanks.
func TestToolsCallNamesTheAmbiguousCandidatesUsably(t *testing.T) {
	base, stop, done := running(t, func(cfg *config.Config) {
		server, err := testmcp.ServerConfig(testmcp.ModeFull)
		if err != nil {
			t.Fatalf("build the upstream configuration: %v", err)
		}
		server.ExposedTools = nil
		cfg.MCPServers = map[string]config.MCPServer{"one": server, "two": server}
	})
	defer func() { stop(); <-done }()

	address := hostPort(t, base)
	waitForState(t, address, "one", "connected")
	waitForState(t, address, "two", "connected")

	code, _, stderr := execute(t, "tools", "call", "echo",
		"--arg", "message=x", "--address", address)

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	for _, want := range []string{"one/echo", "two/echo"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the message does not offer %q:\n%s", want, stderr)
		}
	}
}

// Two servers offering the same tool make a bare name ambiguous, and
// guessing would silently call the wrong one.
func TestToolsCallRefusesAnAmbiguousBareName(t *testing.T) {
	base, stop, done := running(t, func(cfg *config.Config) {
		upstream, err := testmcp.ServerConfig(testmcp.ModeFull)
		if err != nil {
			t.Fatalf("build the upstream configuration: %v", err)
		}
		cfg.MCPServers = map[string]config.MCPServer{"one": upstream, "two": upstream}
	})
	defer func() { stop(); <-done }()

	address := hostPort(t, base)
	waitForState(t, address, "one", "connected")
	waitForState(t, address, "two", "connected")

	code, _, stderr := execute(t, "tools", "call", "echo",
		"--arg", "message=x", "--address", address)

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	for _, want := range []string{"one_echo", "two_echo"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the message does not offer %q:\n%s", want, stderr)
		}
	}
}

func TestToolsCallSuggestsNamesForATypo(t *testing.T) {
	address, cleanup := withTestServer(t, "probe")
	defer cleanup()

	code, _, stderr := execute(t, "tools", "call", "ech", "--address", address)

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "probe_echo") {
		t.Errorf("no suggestion was offered:\n%s", stderr)
	}
}

// A tool that ran and reported a problem is a failed command: a script
// that ignored this would treat the error text as an answer.
func TestToolsCallFailsWhenTheToolReportsAnError(t *testing.T) {
	address, cleanup := withTestServer(t, "probe")
	defer cleanup()

	code, stdout, _ := execute(t, "tools", "call", "probe_fail", "--address", address)

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	// What the tool said still has to be shown; the exit code alone does
	// not tell anyone what went wrong.
	if !strings.Contains(stdout, "this tool always fails") {
		t.Errorf("the tool's message was not printed:\n%s", stdout)
	}
}

func TestToolsCallAcceptsAJSONObject(t *testing.T) {
	address, cleanup := withTestServer(t, "probe")
	defer cleanup()

	code, stdout, stderr := execute(t, "tools", "call", "probe_echo",
		"--args", `{"message":"from json"}`, "--address", address)

	if code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "from json") {
		t.Errorf("the tool's answer is missing:\n%s", stdout)
	}
}

// Both ways of giving arguments can be used at once, and the explicit
// pair is the more specific instruction.
func TestToolsCallLetsAPairOverrideTheJSON(t *testing.T) {
	address, cleanup := withTestServer(t, "probe")
	defer cleanup()

	code, stdout, stderr := execute(t, "tools", "call", "probe_echo",
		"--args", `{"message":"from json"}`,
		"--arg", "message=from the pair",
		"--address", address)

	if code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "from the pair") {
		t.Errorf("the pair did not override the JSON:\n%s", stdout)
	}
	if strings.Contains(stdout, "from json") {
		t.Errorf("the overridden value was sent as well:\n%s", stdout)
	}
}

func TestToolsCallRejectsMalformedArguments(t *testing.T) {
	address, cleanup := withTestServer(t, "probe")
	defer cleanup()

	for _, badArgs := range []struct {
		name  string
		flags []string
	}{
		{"not JSON", []string{"--args", "{oops"}},
		{"not an object", []string{"--args", `["a"]`}},
		{"no equals sign", []string{"--arg", "message"}},
	} {
		t.Run(badArgs.name, func(t *testing.T) {
			args := append([]string{"tools", "call", "probe_echo"}, badArgs.flags...)
			code, _, stderr := execute(t, append(args, "--address", address)...)

			if code == exitOK {
				t.Errorf("the command succeeded with %s arguments", badArgs.name)
			}
			if stderr == "" {
				t.Error("nothing was written to standard error")
			}
		})
	}
}

// ===== argument typing =====

// Everything on a command line is a string, but a tool that wants a
// number and is handed "5" rejects it. These cover the conversion that
// the declared schema type drives.
// ===== the gateway's own tools =====

// The gateway's own tools are offered to every client, so a list that
// omits them is not a list of what is on offer. They were missing from
// this command for the whole of the refactor, which is what this covers.
func TestToolsListShowsTheGatewaysOwnTools(t *testing.T) {
	address, cleanup := withTestServer(t, "probe")
	defer cleanup()

	code, stdout, stderr := execute(t, "tools", "list", "--address", address)

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	for _, name := range gateway.SystemToolNames {
		if !strings.Contains(stdout, name) {
			t.Errorf("the gateway tool %q is missing from the list:\n%s", name, stdout)
		}
	}
}

// They are shown apart from the forwarded ones: they belong to no server,
// and a reader looking for "what can I call to find my way around" should
// not have to pick them out of a list of everything.
func TestTheGatewaysOwnToolsAreListedApart(t *testing.T) {
	address, cleanup := withTestServer(t, "probe")
	defer cleanup()

	_, stdout, _ := execute(t, "tools", "list", "--address", address)

	gatewayGroup := strings.Index(stdout, "GATEWAY TOOLS")
	serverGroup := strings.Index(stdout, "SERVER TOOLS")
	switch {
	case gatewayGroup < 0:
		t.Fatalf("there is no group for the gateway's own tools:\n%s", stdout)
	case serverGroup < 0:
		t.Fatalf("there is no group for the forwarded tools:\n%s", stdout)
	case gatewayGroup > serverGroup:
		t.Errorf("the gateway's own tools come second; they are how a reader finds the rest:\n%s", stdout)
	}

	// The forwarded tool has to be under the group that names its server,
	// not under the gateway's.
	if row := rowFor(t, stdout, "probe_echo"); !strings.Contains(row, "probe") {
		t.Errorf("the forwarded tool lost its server: %q", row)
	}
}

// A server filter asks about one server. The gateway's own tools are on
// no server, so including them would be answering a different question.
func TestAServerFilterLeavesOutTheGatewaysOwnTools(t *testing.T) {
	address, cleanup := withTestServer(t, "probe")
	defer cleanup()

	_, stdout, _ := execute(t, "tools", "list", "--server", "probe", "--address", address)

	if strings.Contains(stdout, gateway.ToolListServers) {
		t.Errorf("a gateway tool was listed as belonging to a server:\n%s", stdout)
	}
	if !strings.Contains(stdout, "probe_echo") {
		t.Errorf("the server's own tool is missing:\n%s", stdout)
	}
}

// A search has to reach both groups, or it silently answers only half the
// question.
func TestASearchReachesTheGatewaysOwnTools(t *testing.T) {
	address, cleanup := withTestServer(t, "probe")
	defer cleanup()

	code, stdout, stderr := execute(t, "tools", "list", "--search", "servers", "--address", address)

	if code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, gateway.ToolListServers) {
		t.Errorf("searching for \"servers\" did not find %s:\n%s", gateway.ToolListServers, stdout)
	}
}

// Calling one used to report that it was not being offered, which was
// false: it was being served to every connected client at the time.
func TestAGatewayToolCanBeCalled(t *testing.T) {
	address, cleanup := withTestServer(t, "probe")
	defer cleanup()

	code, stdout, stderr := execute(t, "tools", "call", gateway.ToolListServers, "--address", address)

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	if !strings.Contains(stdout, "probe") {
		t.Errorf("%s did not report the configured server:\n%s", gateway.ToolListServers, stdout)
	}
}

// Arguments have to reach it, converted using its schema like any other
// tool's.
func TestAGatewayToolReceivesItsArguments(t *testing.T) {
	address, cleanup := withTestServer(t, "probe")
	defer cleanup()

	code, stdout, stderr := execute(t, "tools", "call", gateway.ToolSearchTools,
		"--arg", "server=probe", "--address", address)

	if code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "echo") {
		t.Errorf("%s did not report the server's tools:\n%s", gateway.ToolSearchTools, stdout)
	}
}

func TestArgumentsAreConvertedUsingTheSchema(t *testing.T) {
	schema := map[string]any{
		"properties": map[string]any{
			"count":   map[string]any{"type": "integer"},
			"ratio":   map[string]any{"type": "number"},
			"enabled": map[string]any{"type": "boolean"},
			"label":   map[string]any{"type": "string"},
			"items":   map[string]any{"type": "array"},
		},
	}

	arguments, err := buildArguments("", []string{
		"count=5", "ratio=1.5", "enabled=true", "label=5", "items=[1,2]",
	}, schema)
	if err != nil {
		t.Fatalf("buildArguments: %v", err)
	}

	if got, want := arguments["count"], int64(5); got != want {
		t.Errorf("count = %#v, want %#v", got, want)
	}
	if got, want := arguments["ratio"], 1.5; got != want {
		t.Errorf("ratio = %#v, want %#v", got, want)
	}
	if got := arguments["enabled"]; got != true {
		t.Errorf("enabled = %#v, want true", got)
	}
	// The same text, declared as a string, must stay a string.
	if got, want := arguments["label"], "5"; got != want {
		t.Errorf("label = %#v, want %#v — a declared string must not become a number", got, want)
	}
	if _, ok := arguments["items"].([]any); !ok {
		t.Errorf("items = %#v, want a list", arguments["items"])
	}
}

func TestAValueThatContradictsTheSchemaIsRejected(t *testing.T) {
	schema := map[string]any{
		"properties": map[string]any{"count": map[string]any{"type": "integer"}},
	}

	_, err := buildArguments("", []string{"count=lots"}, schema)
	if err == nil {
		t.Fatal("buildArguments accepted a word where the schema asks for an integer")
	}
	if !strings.Contains(err.Error(), "count") {
		t.Errorf("the error does not name the argument: %v", err)
	}
}

// With nothing declared, a value that looks like JSON is read as JSON and
// anything else stays text. Otherwise every bare word would be an error.
func TestAnUndeclaredArgumentFallsBackToJSONThenText(t *testing.T) {
	arguments, err := buildArguments("", []string{"n=7", "word=hello", "flag=false"}, nil)
	if err != nil {
		t.Fatalf("buildArguments: %v", err)
	}

	if got, want := arguments["n"], float64(7); got != want {
		t.Errorf("n = %#v, want %#v", got, want)
	}
	if got, want := arguments["word"], "hello"; got != want {
		t.Errorf("word = %#v, want %#v", got, want)
	}
	if got := arguments["flag"]; got != false {
		t.Errorf("flag = %#v, want false", got)
	}
}

// A value containing an equals sign belongs to the value, not to the
// split: query strings and base64 both routinely contain one.
func TestAnArgumentValueMayContainAnEqualsSign(t *testing.T) {
	arguments, err := buildArguments("", []string{"query=a=1&b=2"}, nil)
	if err != nil {
		t.Fatalf("buildArguments: %v", err)
	}
	if got, want := arguments["query"], "a=1&b=2"; got != want {
		t.Errorf("query = %#v, want %#v", got, want)
	}
}

// Real servers write long descriptions — the official filesystem server's
// run past 300 characters — and an untruncated column wraps across
// several terminal lines each, which destroys the list as a list.
func TestToolDescriptionsAreShortenedForTheList(t *testing.T) {
	long := strings.Repeat("很长的说明文字。", 60)

	got := summarise(long)
	if len([]rune(got)) > descriptionWidth+1 {
		t.Errorf("summarise returned %d runes, want at most %d plus the ellipsis",
			len([]rune(got)), descriptionWidth)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a shortened description does not say it was shortened: %q", got)
	}
	// Cutting by bytes would leave a replacement character at the end.
	if strings.ContainsRune(got, '�') {
		t.Errorf("a multi-byte character was cut in half: %q", got)
	}
}

func TestAShortDescriptionIsLeftAlone(t *testing.T) {
	if got := summarise("returns its argument"); got != "returns its argument" {
		t.Errorf("summarise changed a short description to %q", got)
	}
}

func TestAMultiLineDescriptionKeepsOnlyItsFirstLine(t *testing.T) {
	got := summarise("first line\nsecond line")
	if strings.Contains(got, "second") {
		t.Errorf("the second line survived: %q", got)
	}
	if !strings.Contains(got, "first line") {
		t.Errorf("the first line was lost: %q", got)
	}
}
