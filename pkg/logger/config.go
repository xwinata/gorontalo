package logger

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// Level is the minimum severity a logger will emit. It is a string so a value
// read from configuration or an environment variable can be passed straight
// into Config.Level. Matching is case-insensitive and surrounding whitespace is
// ignored; the empty value (the zero value) means LevelInfo.
type Level string

const (
	// LevelDebug logs everything.
	LevelDebug Level = "debug"
	// LevelInfo logs info, warn, and error. The empty (zero) value resolves to
	// this.
	LevelInfo Level = "info"
	// LevelWarn logs warn and error.
	LevelWarn Level = "warn"
	// LevelError logs only error.
	LevelError Level = "error"
)

// normalize resolves l to one of the canonical Level constants, applying
// case-insensitive matching, trimming whitespace, and accepting common aliases.
// The empty value and any unrecognized value resolve to LevelInfo, so the zero
// value is a safe default and bad input never panics.
func (l Level) normalize() Level {
	switch strings.ToLower(strings.TrimSpace(string(l))) {
	case "debug", "dbg":
		return LevelDebug
	case "warn", "wrn", "warning":
		return LevelWarn
	case "error", "err":
		return LevelError
	default:
		// "", "info", "inf", and anything unrecognized.
		return LevelInfo
	}
}

// String returns the canonical lowercase label for l (e.g. "info"), resolving
// aliases, case, and the empty value. It satisfies fmt.Stringer so %v and %s
// print the canonical form.
func (l Level) String() string {
	return string(l.normalize())
}

func (l Level) slog() slog.Level {
	switch l.normalize() {
	case LevelDebug:
		return slog.LevelDebug
	case LevelWarn:
		return slog.LevelWarn
	case LevelError:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// fromSlog maps a slog.Level back to the nearest Level.
func fromSlog(l slog.Level) Level {
	switch {
	case l <= slog.LevelDebug:
		return LevelDebug
	case l < slog.LevelWarn:
		return LevelInfo
	case l < slog.LevelError:
		return LevelWarn
	default:
		return LevelError
	}
}

// Writer selects a built-in output destination. It is a string so values from
// configuration can be passed straight into Config.Writers; matching is
// case-insensitive and surrounding whitespace is ignored.
type Writer string

const (
	// WriterStdout writes to stdout. Set Config.Format to choose JSON (default,
	// for containers) or text (human-readable, for development).
	WriterStdout Writer = "stdout"
	// WriterFile writes to a daily-rotated file using the configured format.
	WriterFile Writer = "file"
)

// normalize lowercases and trims w so matching is case-insensitive. It returns
// the canonical form; an unrecognized value is returned trimmed/lowercased so
// the caller can report it.
func (w Writer) normalize() Writer {
	return Writer(strings.ToLower(strings.TrimSpace(string(w))))
}

// String returns the canonical lowercase label for w. It satisfies fmt.Stringer
// so %v and %s print the canonical form.
func (w Writer) String() string {
	return string(w.normalize())
}

// Format selects how log records are encoded.
type Format int

const (
	// FormatJSON encodes each record as a JSON object (one per line). Default.
	FormatJSON Format = iota
	// FormatText encodes each record as human-readable key=value text.
	FormatText
)

// config is the resolved configuration produced by applying Options.
type config struct {
	level         Level
	format        Format
	writers       []Writer
	customWriters []io.Writer
	defaultAttrs  map[string]any

	fileDir           string
	filePrefix        string
	fileBufKB         int
	fileFlushInterval int
}

func defaultConfig() config {
	prefix := "app"
	if host, _ := os.Hostname(); host != "" {
		prefix = "app-" + host
	}
	return config{
		level:             LevelInfo,
		format:            FormatJSON,
		writers:           []Writer{WriterStdout},
		fileDir:           "./logs/",
		filePrefix:        prefix,
		fileBufKB:         64,
		fileFlushInterval: 1,
	}
}

// Config customizes a Logger built with [New]. The zero value is valid and
// yields the defaults: Info level, JSON format, output to stdout only, and the
// default daily-rotated file settings (used only when WriterFile is selected).
type Config struct {
	// Level is the minimum log level. It accepts a value from configuration or
	// an environment variable directly; matching is case-insensitive and the
	// zero value ("") resolves to LevelInfo.
	Level Level

	// Format is the encoding used by all writers. The zero value is FormatJSON.
	Format Format

	// Writers selects the built-in output destinations by name, matched
	// case-insensitively (e.g. WriterStdout, WriterFile). When nil, the default
	// (WriterStdout) is used. To emit to custom writers only, leave this nil
	// and set DisableBuiltinWriters. An unrecognized name makes New return an
	// error.
	Writers []Writer

	// DisableBuiltinWriters drops the built-in destinations entirely, so output
	// goes only to CustomWriters. Use it when routing logs solely to a custom
	// sink (or a buffer in tests). It takes precedence over Writers.
	DisableBuiltinWriters bool

	// CustomWriters are arbitrary io.Writer destinations that receive output in
	// the configured format, alongside any built-in Writers. Nil entries are
	// ignored.
	CustomWriters []io.Writer

	// DefaultFields are attached to every log entry. The zero value adds none.
	DefaultFields map[string]any

	// FileDir is the directory for the daily-rotated file writer. Empty keeps
	// the default ("./logs/"). Used only when WriterFile is selected.
	FileDir string

	// FilePrefix is the base file name for the file writer (a date and .log
	// suffix are appended). Empty keeps the default ("app" or "app-<host>").
	FilePrefix string

	// FileBufferKB tunes the file writer's buffer size in KB. Values <= 0 keep
	// the default (64).
	FileBufferKB int

	// FileFlushSeconds tunes the file writer's flush interval in seconds.
	// Values <= 0 keep the default (1).
	FileFlushSeconds int
}

// resolved folds c into the internal config, applying defaults and the
// defensive validation the constructor relies on (ignore empty/nil, clamp
// non-positive file settings, drop nil custom writers).
func (c Config) resolved() config {
	cfg := defaultConfig()

	cfg.level = c.Level.normalize()
	cfg.format = c.Format
	cfg.defaultAttrs = c.DefaultFields

	switch {
	case c.DisableBuiltinWriters:
		cfg.writers = nil
	case c.Writers != nil:
		// Copy and normalize so matching is case-insensitive and the caller's
		// slice is not retained.
		cfg.writers = make([]Writer, len(c.Writers))
		for i, w := range c.Writers {
			cfg.writers[i] = w.normalize()
		}
	}

	for _, w := range c.CustomWriters {
		if w != nil {
			cfg.customWriters = append(cfg.customWriters, w)
		}
	}

	if c.FileDir != "" {
		cfg.fileDir = c.FileDir
	}
	if c.FilePrefix != "" {
		cfg.filePrefix = c.FilePrefix
	}
	if c.FileBufferKB > 0 {
		cfg.fileBufKB = c.FileBufferKB
	}
	if c.FileFlushSeconds > 0 {
		cfg.fileFlushInterval = c.FileFlushSeconds
	}

	return cfg
}

// handlerFor builds a slog handler for the given format writing to w. Inner
// handlers accept every level; the wrapping levelHandler enforces filtering.
func handlerFor(format Format, w io.Writer) slog.Handler {
	opts := &slog.HandlerOptions{Level: slog.LevelDebug}
	if format == FormatText {
		return slog.NewTextHandler(w, opts)
	}
	return slog.NewJSONHandler(w, opts)
}
