package stderr

const (
	// buildDepth is the number of runtime.Callers frames between the trace
	// capture inside build and the caller of the exported constructor:
	// runtime.Callers, tracer.Trace, build, and the exported entry point. Every
	// exported constructor calls build directly so this stays constant.
	buildDepth = 4

	// defaultMaxDepth captures only the call site by default: the single
	// frame where the error entered this package. Callers wanting the full
	// stack opt in with Options.TraceDepth.
	defaultMaxDepth = 1
)

// Option customizes an error built by New, Wrap, or Opaque. Create one with
// WithOptions.
type Option func(*resolvedOptions)

// WithOptions applies the code, HTTP status, and trace settings in opts. If
// several are passed to one constructor, the last one wins as a whole.
func WithOptions(opts Options) Option {
	return func(rc *resolvedOptions) { *rc = opts.resolved() }
}

// resolve returns the settings for a constructor call, starting from the
// defaults and applying each non-nil option in order.
func resolve(opts []Option) resolvedOptions {
	rc := Options{}.resolved()
	for _, opt := range opts {
		if opt != nil {
			opt(&rc)
		}
	}
	return rc
}

// Options carries the application metadata and trace settings for an error.
// It is a parameter struct: set only the fields you need and pass it to
// WithOptions. The zero value is valid and selects the defaults.
type Options struct {
	// Code is the application-level error code (e.g. "USER_NOT_FOUND"). Empty
	// means none is set at this level; Code then reports the code of the
	// nearest wrapped *StdErr that has one.
	Code string

	// HTTPStatus is the HTTP status an API should return (e.g. 404). Values outside
	// 100–599 are ignored; HTTPStatus then reports the status of the nearest
	// wrapped *StdErr that has one.
	HTTPStatus int

	// SkipFrames adds to the number of leading stack frames skipped when the
	// trace is captured. Use it from helpers that call into this package so
	// their own frames do not appear. Negative values are ignored.
	SkipFrames int

	// TraceDepth is the number of stack frames captured, starting at the call
	// site. Values < 1 keep the default of 1, which records only the call
	// site.
	TraceDepth int
}

// resolvedOptions is Options after defaulting and validation.
type resolvedOptions struct {
	code       string
	httpStatus int
	skip       int
	maxDepth   int
}

// resolved applies defaults and clamps out-of-range values.
func (o Options) resolved() resolvedOptions {
	rc := resolvedOptions{
		code:     o.Code,
		skip:     buildDepth,
		maxDepth: defaultMaxDepth,
	}
	if o.HTTPStatus >= 100 && o.HTTPStatus <= 599 {
		rc.httpStatus = o.HTTPStatus
	}
	if o.SkipFrames > 0 {
		rc.skip += o.SkipFrames
	}
	if o.TraceDepth >= 1 {
		rc.maxDepth = o.TraceDepth
	}
	return rc
}
