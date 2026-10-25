// Package logger provides structured logging built entirely on the standard
// library's log/slog. It has no third-party dependencies.
//
// It supports multiple output destinations (stdout and a daily-rotated file)
// with a selectable format (JSON or text), a fixed level chosen at
// construction, and default fields attached to every entry.
//
// # Instances
//
// A [Logger] is an independent logging instance created with [New]. There is no
// package-level default: construct a Logger and inject it into the components
// that need it. This keeps logging an explicit dependency rather than ambient
// global state.
//
//	log, err := logger.New(logger.Config{
//	    Level:         logger.LevelInfo,
//	    Writers:       []logger.Writer{logger.WriterStdout},
//	    Format:        logger.FormatJSON,
//	    DefaultFields: map[string]any{"app_name": "myapp"},
//	})
//	if err != nil {
//	    return err
//	}
//	defer log.Close()
//	svc := NewService(log)
//
// # Levels and context
//
// The level is fixed at construction and does not change at runtime. To raise
// or lower verbosity for a specific request or scope, attach a level to the
// context; it takes precedence over the configured level for logs made with
// that context:
//
//	ctx = logger.WithContextLevel(ctx, logger.LevelDebug)
//	log.Debug(ctx, "detailed trace", logger.Attr{Key: "step", Value: 1})
//
// # Usage
//
// Every logging method takes a context as its first argument. Pass
// context.Background() when there is no request context:
//
//	log.Info(ctx, "request processed", logger.Attr{Key: "user_id", Value: 123})
//	log.Error(ctx, err, logger.Attr{Key: "op", Value: "save"})
//
// Error takes no message string: the record message is the error's text and any
// additional context is supplied as attributes.
package logger

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// Logger is an independent structured-logging instance. Create one with [New]
// and inject it where logging is needed. Its zero value is not usable; create a
// Logger with New.
//
// A Logger is safe for concurrent use by multiple goroutines.
type Logger struct {
	logger       *slog.Logger
	defaultAttrs []slog.Attr
	file         *fileWriter
}

// New builds a Logger from cfg. A zero-value Config selects the defaults (Info
// level, JSON to stdout). Each Logger is independent of every other instance.
//
// When the configuration selects the file writer, New opens the log file and
// starts its background flusher; call [Logger.Close] (or [Logger.Sync] to flush
// without closing) to release it.
//
// The inner handlers are created to accept every level (Level: LevelDebug); the
// wrapping levelHandler is the sole authority on filtering, so it can honor a
// context level that is lower than the global level.
func New(cfg Config) (*Logger, error) {
	rc := cfg.resolved()

	var (
		handlers []slog.Handler
		fw       *fileWriter
	)

	for _, w := range rc.writers {
		switch w.normalize() {
		case WriterStdout:
			handlers = append(handlers, handlerFor(rc.format, os.Stdout))
		case WriterFile:
			writer, err := newFileWriter(
				filepath.Join(rc.fileDir, rc.filePrefix+".log"),
				rc.fileBufKB,
				rc.fileFlushInterval,
			)
			if err != nil {
				return nil, err
			}
			fw = writer
			handlers = append(handlers, handlerFor(rc.format, writer))
		default:
			return nil, fmt.Errorf("logger: unsupported writer %q", string(w))
		}
	}

	for _, cw := range rc.customWriters {
		handlers = append(handlers, handlerFor(rc.format, cw))
	}

	if len(handlers) == 0 {
		return nil, fmt.Errorf("logger: no output writers configured")
	}

	var inner slog.Handler
	if len(handlers) == 1 {
		inner = handlers[0]
	} else {
		inner = newFanoutHandler(handlers)
	}

	// The level handler enforces the fixed global level and any per-context
	// override; the inner handlers accept every level.
	handler := newLevelHandler(rc.level.slog(), inner)

	defaults := make([]slog.Attr, 0, len(rc.defaultAttrs))
	for k, v := range rc.defaultAttrs {
		defaults = append(defaults, slog.Any(k, v))
	}

	return &Logger{
		logger:       slog.New(handler),
		defaultAttrs: defaults,
		file:         fw,
	}, nil
}

// Debug logs a message at debug level, honoring any context-attached level.
func (l *Logger) Debug(ctx context.Context, msg string, attrs ...Attr) {
	l.log(ctx, slog.LevelDebug, msg, nil, attrs)
}

// Info logs a message at info level, honoring any context-attached level.
func (l *Logger) Info(ctx context.Context, msg string, attrs ...Attr) {
	l.log(ctx, slog.LevelInfo, msg, nil, attrs)
}

// Warn logs a message at warn level, honoring any context-attached level.
func (l *Logger) Warn(ctx context.Context, msg string, attrs ...Attr) {
	l.log(ctx, slog.LevelWarn, msg, nil, attrs)
}

// Error logs err at error level, honoring any context-attached level. The
// record message is err's text and err is attached under the "error" key;
// additional context is supplied as attributes. A nil err logs an empty message
// with no error key.
func (l *Logger) Error(ctx context.Context, err error, attrs ...Attr) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	l.log(ctx, slog.LevelError, msg, err, attrs)
}

// Sync flushes any buffered output (currently the file writer). It is safe to
// call even when logging to stdout only.
func (l *Logger) Sync() error {
	if l.file != nil {
		return l.file.Flush()
	}
	return nil
}

// Close flushes and releases resources held by the Logger, stopping the file
// writer's background flusher when one is configured. It is safe to call even
// when logging to stdout only. A Logger must not be used after Close.
func (l *Logger) Close() error {
	if l.file != nil {
		return l.file.Close()
	}
	return nil
}

func (l *Logger) log(ctx context.Context, level slog.Level, msg string, err error, attrs []Attr) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !l.logger.Enabled(ctx, level) {
		return
	}

	all := make([]slog.Attr, 0, len(l.defaultAttrs)+len(attrs)+1)
	all = append(all, l.defaultAttrs...)
	for _, a := range attrs {
		all = append(all, a.slogAttr())
	}
	if err != nil {
		all = append(all, slog.Any("error", err))
	}

	l.logger.LogAttrs(ctx, level, msg, all...)
}
