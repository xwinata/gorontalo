package logger

import "log/slog"

// Attr is a single structured key/value pair attached to a log entry.
// Construct one as a struct literal:
//
//	log.Info(ctx, "processed", logger.Attr{Key: "user_id", Value: 123})
//
// Passing an ordered slice of Attrs (rather than a map) keeps attribute order
// deterministic and avoids a per-call map allocation. The implementation
// converts each Attr to its log/slog form internally, so callers never touch
// slog directly.
type Attr struct {
	Key   string
	Value any
}

// slogAttr converts a to its log/slog representation. slog.Any selects the
// appropriate typed value for common kinds, falling back to an any value.
func (a Attr) slogAttr() slog.Attr {
	return slog.Any(a.Key, a.Value)
}
