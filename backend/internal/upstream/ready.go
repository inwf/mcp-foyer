package upstream

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
	"time"

	"mcp-foyer/internal/config"
)

// readyProbe waits for a child process to say it is ready.
//
// Some servers cannot answer the moment they are started: they install
// themselves, open a port, or load a model first, and only then print a
// line saying so. Handing such a server a handshake immediately means the
// handshake's timeout is really a startup timeout, and a server that
// takes a minute to install looks broken rather than slow.
//
// So the process is started, its standard error is read until one of the
// configured patterns matches, and only then does the handshake begin.
type readyProbe struct {
	patterns []*regexp.Regexp
	timeout  time.Duration

	once    sync.Once
	done    chan struct{}
	mu      sync.Mutex
	matched string
}

// newReadyProbe returns nil when the server declares no patterns, which
// is the ordinary case: nothing is waited for and nothing is watched.
//
// The patterns are compiled during validation as well, so a failure here
// is not expected; it is still reported rather than dropped, because
// silently not waiting would look exactly like waiting and finding
// nothing.
func newReadyProbe(cfg config.MCPServer) (*readyProbe, error) {
	if len(cfg.ReadyPatterns) == 0 {
		return nil, nil
	}

	patterns := make([]*regexp.Regexp, 0, len(cfg.ReadyPatterns))
	for _, pattern := range cfg.ReadyPatterns {
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("readyPatterns: %q: %w", pattern, err)
		}
		patterns = append(patterns, compiled)
	}

	timeout := cfg.ReadyTimeout
	if timeout <= 0 {
		timeout = config.DefaultReadyTimeout
	}
	return &readyProbe{patterns: patterns, timeout: timeout, done: make(chan struct{})}, nil
}

// observe is handed every line the child writes to standard error.
func (p *readyProbe) observe(line string) {
	if p == nil {
		return
	}
	for _, pattern := range p.patterns {
		if !pattern.MatchString(line) {
			continue
		}
		p.mu.Lock()
		p.matched = line
		p.mu.Unlock()
		p.once.Do(func() { close(p.done) })
		return
	}
}

// wait blocks until a pattern matches, the timeout expires, or ctx ends.
//
// Running out of time is not an error. A pattern that never matches is
// usually a typo or a message the server stopped printing, and treating
// that as a failure would turn one wrong character in the configuration
// into a server that can never connect. Going ahead with the handshake
// leaves the existing retry policy to decide whether the server is really
// unreachable — the two mechanisms are in series, not in competition.
func (p *readyProbe) wait(ctx context.Context, log *slog.Logger, server string) {
	if p == nil {
		return
	}

	started := time.Now()
	timer := time.NewTimer(p.timeout)
	defer timer.Stop()

	select {
	case <-p.done:
		p.mu.Lock()
		line := p.matched
		p.mu.Unlock()
		log.Debug("the server reported that it is ready",
			"server", server, "after", time.Since(started), "line", line)

	case <-timer.C:
		log.Warn("none of the ready patterns matched before the timeout; "+
			"starting the handshake anyway",
			"server", server, "waited", p.timeout, "patterns", len(p.patterns))

	case <-ctx.Done():
	}
}
