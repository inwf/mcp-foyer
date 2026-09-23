package main

import (
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"mcp-foyer/internal/api"
	"mcp-foyer/internal/config"
	"mcp-foyer/internal/gateway"
	"mcp-foyer/internal/guide"
)

// A guide is worth testing not for its prose but for whether it is still
// true. Documentation drifts silently: a command gets renamed, a flag
// goes away, a default changes, and the guide keeps confidently telling
// people to type something that no longer works.
//
// So these check the guide against the program rather than against a
// fixed expected text — which would only assert that the file has not
// been edited, and would have to be updated every time it is.

func TestGuideIsPrinted(t *testing.T) {
	code, stdout, stderr := execute(t, "guide")

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	if len(stdout) < 500 {
		t.Errorf("the guide is %d bytes, which cannot be a guide", len(stdout))
	}
	if stderr != "" {
		t.Errorf("the guide wrote to standard error:\n%s", stderr)
	}
}

// The command and the gateway's foyer://guide resource serve one document.
// This pins the CLI half to the shared package; the gateway half is pinned
// in internal/gateway. What both are there to stop is a second embedded
// copy, which would read identically until the day one of them is edited.
func TestTheGuideCommandPrintsTheSharedDocument(t *testing.T) {
	code, stdout, stderr := execute(t, "guide")

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	if stdout != guide.Text() {
		t.Errorf("the command printed %d bytes and the package holds %d; "+
			"it is not printing the shared document", len(stdout), len(guide.Text()))
	}
}

// Every command the guide tells someone to run has to exist.
func TestGuideOnlyMentionsRealCommands(t *testing.T) {
	root := newRootCommand(nil, nil, nil)

	unknown, checked := checkCommandsIn(root, guide.Text())
	if checked == 0 {
		t.Fatal("no mcp-foyer commands were found in the guide, so this proves nothing")
	}
	for _, bad := range unknown {
		t.Errorf("the guide tells the reader to run %q, which is not a command; "+
			"the commands are: %s", bad, strings.Join(sorted(commandPaths(root)), ", "))
	}
}

// Every flag the guide tells someone to type has to exist on the command
// it is typed after.
//
// A renamed flag is the quietest kind of documentation rot: the command
// still runs, so nothing looks broken until a reader copies the line and
// gets "unknown flag".
func TestGuideOnlyMentionsRealFlags(t *testing.T) {
	root := newRootCommand(nil, nil, nil)

	unknown, checked := checkFlagsIn(root, guide.Text())
	if checked == 0 {
		t.Fatal("no flags were found in the guide, so this proves nothing")
	}
	for _, bad := range unknown {
		t.Errorf("the guide tells the reader to type %q, which is not a flag there", bad)
	}
}

// Every command has to be mentioned somewhere, or it is undiscoverable
// to anyone who reads the guide rather than the help text.
func TestGuideMentionsEveryCommand(t *testing.T) {
	// The shell-completion command is cobra's own and is not part of what
	// this program is for.
	skip := map[string]bool{"completion": true, "help": true}

	for path := range commandPaths(newRootCommand(nil, nil, nil)) {
		if skip[strings.Fields(path)[0]] {
			continue
		}
		if !strings.Contains(guide.Text(), "mcp-foyer "+path) {
			t.Errorf("the guide never mentions %q", "mcp-foyer "+path)
		}
	}
}

// The system tools are the part a model needs explained, so a new or
// renamed one that never reaches the guide is a real omission.
func TestGuideDocumentsEverySystemTool(t *testing.T) {
	section, ok := guide.Section("自带的系统工具")
	if !ok {
		t.Fatal("the guide has no system-tools section")
	}
	// Every registered tool needs an entry in the reference table;
	// mentioning it in a call example does not document its purpose.
	documented := map[string]bool{}
	for _, line := range strings.Split(section, "\n") {
		if rest, ok := strings.CutPrefix(line, "| `"); ok {
			name, _, _ := strings.Cut(rest, "`")
			if !gateway.IsSystemTool(name) {
				t.Errorf("the system-tools table documents unregistered tool %q", name)
			}
			if documented[name] {
				t.Errorf("the system-tools table repeats %q", name)
			}
			documented[name] = true
		}
	}
	for _, name := range gateway.SystemToolNames {
		if !documented[name] {
			t.Errorf("the system-tools table omits registered tool %q", name)
		}
	}
}

// The addresses, paths and names in the guide are copied from the code,
// and a change to either has to be a change to both.
func TestGuideAgreesWithTheProgramsConstants(t *testing.T) {
	defaults := config.Default()

	for _, fact := range []struct {
		what  string
		value string
	}{
		{"the MCP endpoint", api.MCPPath},
		{"the management API prefix", api.APIPrefix},
		{"the session mode header", gateway.SessionModeHeader},
		{"the data directory variable", config.DataDirEnv},
		{"the default port", strconv.Itoa(defaults.Listen.Port)},
		{"the default host", defaults.Listen.Host},
	} {
		if !strings.Contains(guide.Text(), fact.value) {
			t.Errorf("the guide does not mention %s (%q), so it cannot be describing "+
				"this build", fact.what, fact.value)
		}
	}
}

// The transports the guide describes have to be the ones that exist —
// this is what would have caught the guide still describing a transport
// after it was removed.
func TestGuideDescribesTheRealTransports(t *testing.T) {
	for _, transport := range []config.Transport{
		config.TransportStdio,
		config.TransportStreamableHTTP,
	} {
		if !strings.Contains(guide.Text(), string(transport)) {
			t.Errorf("the guide never mentions the %q transport", transport)
		}
	}
}

func TestGuideCanPrintOneSection(t *testing.T) {
	code, stdout, stderr := execute(t, "guide", "--section", "会话模式")

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	if !strings.HasPrefix(stdout, "## ") {
		t.Errorf("the output does not start at a heading:\n%s", stdout)
	}
	if strings.Count(stdout, "\n## ") != 0 {
		t.Errorf("more than one section was printed:\n%s", stdout)
	}
	if len(stdout) >= len(guide.Text()) {
		t.Error("the whole guide was printed instead of one section")
	}
}

func TestGuideNamesTheSectionsWhenOneIsNotFound(t *testing.T) {
	code, _, stderr := execute(t, "guide", "--section", "quantum tunnelling")

	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "启动") {
		t.Errorf("the message does not list the sections that do exist:\n%s", stderr)
	}
}

// ===== helpers =====

// commandPaths collects every command in the tree as the space-separated
// path a user would type.
func commandPaths(root *cobra.Command) map[string]bool {
	paths := map[string]bool{}

	var walk func(cmd *cobra.Command, prefix string)
	walk = func(cmd *cobra.Command, prefix string) {
		for _, child := range cmd.Commands() {
			path := strings.TrimSpace(prefix + " " + child.Name())
			paths[path] = true
			walk(child, path)
		}
	}
	walk(root, "")
	return paths
}

// checkCommandsIn walks every "mcp-foyer ..." line of the guide against the
// command tree, and reports the words that name a command which does not
// exist.
//
// The walk descends only while the command it has reached still has
// subcommands. That is what separates a subcommand from an argument
// without having to know how many arguments each command takes: in
// "servers add files", "add" is a child of "servers" so it is consumed,
// and "add" has no children of its own, so "files" is an argument rather
// than a command that has gone missing.
func checkCommandsIn(root *cobra.Command, document string) (unknown []string, checked int) {
	for _, line := range strings.Split(document, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "mcp-foyer ")
		if !ok {
			continue
		}
		checked++

		cmd := root
		var path []string
		for _, word := range strings.Fields(rest) {
			if !cmd.HasSubCommands() || !isCommandWord(word) {
				break
			}
			child := childNamed(cmd, word)
			if child == nil {
				unknown = append(unknown, "mcp-foyer "+strings.Join(append(path, word), " "))
				break
			}
			cmd = child
			path = append(path, word)
		}
	}
	return unknown, checked
}

// checkFlagsIn walks every "mcp-foyer ..." line of the guide and reports the
// --flags that the command on that line does not have.
//
// The command is resolved the same way checkCommandsIn resolves it, so a
// flag is judged against the command it was written after rather than
// against the whole program: --verbose exists, but not on `tools list`.
func checkFlagsIn(root *cobra.Command, document string) (unknown []string, checked int) {
	for _, line := range strings.Split(document, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "mcp-foyer ")
		if !ok {
			continue
		}

		cmd := root
		var path []string
		for _, word := range strings.Fields(rest) {
			// The guide shows optional arguments in brackets and quotes some
			// values, neither of which is part of what a user types.
			word = strings.Trim(word, "[]`\"")

			// A bare "--" ends mcp-foyer's own arguments: what follows is the
			// upstream command line, whose flags belong to that program.
			if word == "--" {
				break
			}

			if name, isFlag := strings.CutPrefix(word, "--"); isFlag {
				name, _, _ = strings.Cut(name, "=")
				checked++
				if lookupFlag(cmd, name) == nil {
					unknown = append(unknown,
						strings.TrimSpace("mcp-foyer "+strings.Join(path, " ")+" --"+name))
				}
				continue
			}
			if !cmd.HasSubCommands() || !isCommandWord(word) {
				continue
			}
			if child := childNamed(cmd, word); child != nil {
				cmd = child
				path = append(path, word)
			}
		}
	}
	return unknown, checked
}

func lookupFlag(cmd *cobra.Command, name string) *pflag.Flag {
	if flag := cmd.Flags().Lookup(name); flag != nil {
		return flag
	}
	// The shared flags are declared once on the root, so a subcommand only
	// sees them as inherited ones.
	return cmd.InheritedFlags().Lookup(name)
}

func childNamed(cmd *cobra.Command, name string) *cobra.Command {
	for _, child := range cmd.Commands() {
		if child.Name() == name {
			return child
		}
	}
	return nil
}

func isCommandWord(word string) bool {
	if word == "" {
		return false
	}
	for _, r := range word {
		if (r < 'a' || r > 'z') && r != '-' {
			return false
		}
	}
	return true
}

func sorted(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	// A stable order, so a failure message reads the same twice.
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
