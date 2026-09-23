// Package logging builds the loggers mcp-foyer writes through.
//
// A single logger fans out to up to three destinations: standard output,
// a rotating file under the data directory, and an in-memory store that
// the management API queries. The in-memory store is why this package
// exists at all rather than calling [log/slog] directly.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"mcp-foyer/internal/config"
)

// Format values accepted by [Options.Format].
const (
	FormatConsole = "console"
	FormatJSON    = "json"
)

// Options describes one logger. It deliberately mirrors the relevant
// configuration rather than taking [config.Config], so that tests can
// build a logger without a whole configuration and can inject a clock.
type Options struct {
	Level  slog.Level
	Format string

	// FilePath is the log file to write. Empty means do not write to a
	// file at all, which is what tests and one-shot commands want.
	FilePath  string
	MaxAge    time.Duration
	MaxSizeMB int

	// Stdout receives a copy of every record. Nil means do not write to
	// standard output.
	Stdout io.Writer

	// Store receives every record for later querying by the management
	// API. Nil means do not retain records in memory.
	Store *Store

	// ModuleLevels lowers the threshold for particular modules, so that
	// one subsystem can be followed in detail without the rest of the
	// program drowning it out. A module not named here uses Level.
	ModuleLevels map[string]slog.Level

	// HideTraceContext leaves the correlation identifiers out of the
	// console and file output. The store keeps them either way: a shorter
	// line to read is a display choice, not a reason to lose the data the
	// log viewer filters on.
	HideTraceContext bool

	// Now overrides the clock, which is how rotation and retention are
	// tested without waiting.
	Now func() time.Time
}

// TraceAttrs are the keys that tie a record to one request or one
// client session, which is what [Options.HideTraceContext] hides.
var TraceAttrs = []string{"requestId", "session"}

// OptionsFrom derives logger options from the configuration.
func OptionsFrom(cfg config.Logging, paths config.Paths, store *Store) (Options, error) {
	level, err := ParseLevel(cfg.Level)
	if err != nil {
		return Options{}, err
	}

	var moduleLevels map[string]slog.Level
	if cfg.GatewayDebug {
		moduleLevels = map[string]slog.Level{ModuleGateway: slog.LevelDebug}
	}

	return Options{
		Level:            level,
		Format:           cfg.Format,
		FilePath:         paths.LogFile(),
		MaxAge:           cfg.MaxAge,
		MaxSizeMB:        cfg.MaxSizeMB,
		Stdout:           os.Stdout,
		Store:            store,
		ModuleLevels:     moduleLevels,
		HideTraceContext: !cfg.ShowTraceContext,
	}, nil
}

// ParseLevel converts a configured level name to a slog level.
func ParseLevel(level config.LogLevel) (slog.Level, error) {
	switch level {
	case config.LevelDebug:
		return slog.LevelDebug, nil
	case config.LevelInfo:
		return slog.LevelInfo, nil
	case config.LevelWarn:
		return slog.LevelWarn, nil
	case config.LevelError:
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown log level %q", level)
	}
}

// LevelName converts a slog level to a configured level name. It is the
// inverse of [ParseLevel], for reporting a stored record to a client
// that should not have to know slog's numbering.
//
// Levels between the named ones are reported as the nearest named level
// at or below them, which is how slog itself describes them.
func LevelName(level slog.Level) config.LogLevel {
	switch {
	case level < slog.LevelInfo:
		return config.LevelDebug
	case level < slog.LevelWarn:
		return config.LevelInfo
	case level < slog.LevelError:
		return config.LevelWarn
	default:
		return config.LevelError
	}
}

// Logger is a configured logger together with the resources it owns.
type Logger struct {
	*slog.Logger

	file io.Closer
}

// New builds a logger from opts. Call [Logger.Close] to release the log
// file when shutting down.
func New(opts Options) (*Logger, error) {
	if opts.Format == "" {
		opts.Format = FormatConsole
	}

	// The destinations are built at the lowest level anything asks for,
	// because a record they reject is a record the module gate above them
	// never gets to allow. That gate is then the only one that decides.
	floor := opts.Level
	for _, level := range opts.ModuleLevels {
		if level < floor {
			floor = level
		}
	}

	var handlers []slog.Handler
	var file io.Closer

	if opts.Stdout != nil {
		handlers = append(handlers, newTextOrJSON(opts.Stdout, opts, floor))
	}

	if opts.FilePath != "" {
		w, err := newRotatingFile(rotateOptions{
			Path:      opts.FilePath,
			MaxSizeMB: opts.MaxSizeMB,
			MaxAge:    opts.MaxAge,
			Now:       opts.Now,
		})
		if err != nil {
			return nil, err
		}
		file = w
		handlers = append(handlers, newTextOrJSON(w, opts, floor))
	}

	if opts.Store != nil {
		handlers = append(handlers, newStoreHandler(opts.Store, floor, opts.Now))
	}

	var handler slog.Handler = fanout(handlers)
	if len(opts.ModuleLevels) > 0 {
		handler = &moduleGate{
			next:    handler,
			base:    opts.Level,
			byModul: opts.ModuleLevels,
		}
	}
	return &Logger{Logger: slog.New(handler), file: file}, nil
}

// Close releases the log file, if one was opened.
func (l *Logger) Close() error {
	if l.file == nil {
		return nil
	}
	return l.file.Close()
}

func newTextOrJSON(w io.Writer, opts Options, level slog.Level) slog.Handler {
	handlerOpts := &slog.HandlerOptions{Level: level}
	if opts.HideTraceContext {
		handlerOpts.ReplaceAttr = dropTraceAttrs
	}
	if opts.Format == FormatJSON {
		return slog.NewJSONHandler(w, handlerOpts)
	}
	return slog.NewTextHandler(w, handlerOpts)
}

// dropTraceAttrs removes the correlation identifiers from a formatted
// line. An empty Attr is how slog is told to leave one out.
func dropTraceAttrs(groups []string, a slog.Attr) slog.Attr {
	if len(groups) > 0 {
		return a
	}
	for _, key := range TraceAttrs {
		if a.Key == key {
			return slog.Attr{}
		}
	}
	return a
}

// moduleGate decides whether a record passes, per module.
//
// It has to be a handler rather than a check at the call site because
// slog asks Enabled before it builds a record, and the answer depends on
// which module is logging. The module arrives through WithAttrs — which
// is what [Logger.For] does — so each derived handler knows its own
// module and can answer for itself.
type moduleGate struct {
	next    slog.Handler
	base    slog.Level
	byModul map[string]slog.Level
	module  string
}

func (g *moduleGate) threshold() slog.Level {
	if level, named := g.byModul[g.module]; named {
		return level
	}
	return g.base
}

func (g *moduleGate) Enabled(_ context.Context, level slog.Level) bool {
	return level >= g.threshold()
}

func (g *moduleGate) Handle(ctx context.Context, record slog.Record) error {
	return g.next.Handle(ctx, record)
}

func (g *moduleGate) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := &moduleGate{next: g.next.WithAttrs(attrs), base: g.base, byModul: g.byModul, module: g.module}
	for _, attr := range attrs {
		if attr.Key == AttrModule {
			out.module = attr.Value.String()
		}
	}
	return out
}

func (g *moduleGate) WithGroup(name string) slog.Handler {
	return &moduleGate{next: g.next.WithGroup(name), base: g.base, byModul: g.byModul, module: g.module}
}

// fanout delivers each record to every handler. A handler that fails
// does not stop the others: losing one destination should not silence
// the rest.
type fanout []slog.Handler

func (f fanout) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range f {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (f fanout) Handle(ctx context.Context, record slog.Record) error {
	var firstErr error
	for _, h := range f {
		if !h.Enabled(ctx, record.Level) {
			continue
		}
		// Each handler gets its own copy: a handler is allowed to retain
		// the record, and Record contains a shared attribute backing
		// array.
		if err := h.Handle(ctx, record.Clone()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (f fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (f fanout) WithGroup(name string) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithGroup(name)
	}
	return out
}
