// Package stderr wraps errors with a contextual message, a captured
// stack trace, an application-level error code, and an HTTP status. It builds
// on gorontalo/pkg/tracer for stack capture and has no third-party dependencies.
//
// A standard error preserves the original error chain, so errors.Is and
// errors.As keep working. The stack trace is captured once, at the point an
// ordinary error is first wrapped; wrapping an already-wrapped error reuses the
// original trace instead of capturing a new one.
//
// The code and status travel with the error so transport layers can report
// them without a separate lookup. The application error code is distinct from
// the HTTP status: the code identifies the failure to the application and its
// clients (e.g. "USER_NOT_FOUND"), while the status is the HTTP status an API
// should return (e.g. 404). Use errors.As with *StdErr to read them back.
//
// Construction uses a single New constructor that takes the cause to wrap as
// its first argument and an Options struct as its second, so every call site
// names the fields it sets and the zero value of Options yields sensible
// defaults.
//
// When New wraps another *StdErr, any of Message, Code, and Status left empty on
// Options is inherited from the nearest wrapped *StdErr that sets it. This
// means re-wrapping only needs to set the fields that change: adding a message
// leaves the original code and status in place.
//
// # Usage
//
//	if err := db.Load(ctx, id); err != nil {
//	    return stderr.New(err, stderr.Options{
//	        Message: "load user",
//	        Code:    "USER_NOT_FOUND",
//	        Status:  http.StatusNotFound,
//	    })
//	}
//
// To originate a new error chain rather than wrap an existing one, pass a nil
// cause:
//
//	return stderr.New(nil, stderr.Options{Message: "invalid state"})
//
// Reading the metadata back:
//
//	var se *stderr.StdErr
//	if errors.As(err, &se) {
//	    log.Println(se.Code(), se.Status(), se.StackTrace())
//	}
package stderr

import (
	"errors"
	"fmt"
	"sync"

	"gorontalo/pkg/tracer"
)

const (
	// defaultSkip makes the first captured frame the caller of New. The trace
	// is taken inside tracer.Trace, called from a closure passed to
	// sync.Once.Do, inside StdErr.StackTrace, inside New. tracer's own default
	// already skips tracer.Trace and runtime.Callers; this value skips the
	// remaining intermediate frames (closure, sync.Once.Do, StackTrace, New).
	defaultSkip = 7

	// defaultMaxDepth caps the number of frames captured by default.
	defaultMaxDepth = 32
)

// Options configures a StdErr created by New. The zero value is valid: it
// produces an error with no code, no status, and the default trace settings.
// Only Message is normally required.
type Options struct {
	// Message is the contextual message for this error. When empty and the
	// cause passed to New unwraps to another *StdErr, it is inherited from the
	// nearest ancestor *StdErr that has one.
	Message string

	// Code is the application-level error code (e.g. "USER_NOT_FOUND"). Empty
	// means no code is set at this level; when the cause unwraps to another *StdErr,
	// an empty Code is inherited from the nearest ancestor *StdErr that sets one.
	Code string

	// Status is the HTTP status an API should return (e.g. 404). Values outside
	// 100–599 are ignored, leaving no status set at this level; when the cause
	// unwraps to another *StdErr, an unset Status is inherited from the nearest
	// ancestor *StdErr that sets one.
	Status int

	// SkipFrames adds to the number of leading stack frames skipped when the
	// trace is captured. Use it from helpers that call New so their own frames
	// do not appear. Negative values are ignored.
	SkipFrames int

	// TraceDepth caps the number of stack frames captured. Values < 1 keep the
	// default depth.
	TraceDepth int
}

// StdErr is an error carrying a message, an optional cause, a captured stack
// trace, an application-level error code, and an HTTP status. Create one with
// New and inspect it with errors.As.
type StdErr struct {
	msg    string
	cause  error
	code   string
	status int

	once   sync.Once
	traces []tracer.Frame

	skip     int
	maxDepth int
}

// New creates a *StdErr wrapping err. When err is nil the result originates a
// new error chain; otherwise it wraps err, preserving the chain for errors.Is
// and errors.As. The stack trace is captured at this call unless the wrapped
// error already carries one, in which case it is reused.
//
// When err unwraps to another *StdErr, any of Message, Code, and Status left
// empty on opts are inherited from the nearest ancestor *StdErr that sets them.
// This lets a caller re-wrap a standard error to add context to one field (say,
// a message) while the code and status flow up unchanged.
func New(err error, opts Options) *StdErr {
	skip := defaultSkip
	if opts.SkipFrames > 0 {
		skip += opts.SkipFrames
	}
	maxDepth := defaultMaxDepth
	if opts.TraceDepth >= 1 {
		maxDepth = opts.TraceDepth
	}

	status := 0
	if opts.Status >= 100 && opts.Status <= 599 {
		status = opts.Status
	}

	msg := opts.Message
	code := opts.Code

	// Inherit empty fields from the nearest wrapped *StdErr so re-wrapping only
	// needs to set the fields that change.
	var parent *StdErr
	if errors.As(err, &parent) {
		if msg == "" {
			msg = parent.messageValue()
		}
		if code == "" {
			code = parent.Code()
		}
		if status == 0 {
			status = parent.Status()
		}
	}

	e := &StdErr{
		msg:      msg,
		cause:    err,
		code:     code,
		status:   status,
		skip:     skip,
		maxDepth: maxDepth,
	}
	// Capture the trace now (or inherit the cause's) so it reflects the call
	// site rather than wherever StackTrace is first read.
	e.StackTrace()
	return e
}

// messageValue returns the message carried by this error, or, when this error
// has none of its own, the nearest ancestor *StdErr's message. It is used when
// inheriting a message into a newly wrapped error and never walks into
// non-*StdErr causes, so it reflects only messages set through this package.
func (e *StdErr) messageValue() string {
	if e.msg != "" {
		return e.msg
	}
	var next *StdErr
	if errors.As(e.cause, &next) {
		return next.messageValue()
	}
	return ""
}

// Error implements the error interface. It renders the message followed by the
// cause, if any, so the chain reads from outermost to innermost. When the
// message was inherited from the cause (so it already equals the cause's
// rendered form), it is not repeated.
func (e *StdErr) Error() string {
	if e.cause != nil {
		causeMsg := e.cause.Error()
		if e.msg == "" || e.msg == causeMsg {
			return causeMsg
		}
		return fmt.Sprintf("%s: %v", e.msg, causeMsg)
	}
	return e.msg
}

// Unwrap returns the wrapped cause, enabling errors.Is and errors.As to walk
// the chain.
func (e *StdErr) Unwrap() error {
	return e.cause
}

// Code returns the application-level error code. When this error does not set
// one, Code looks down the chain for the nearest *StdErr that does and returns
// its code; it returns "" when none is found.
func (e *StdErr) Code() string {
	if e.code != "" {
		return e.code
	}
	var next *StdErr
	if errors.As(e.cause, &next) {
		return next.Code()
	}
	return ""
}

// Status returns the HTTP status. When this error does not set one, Status
// looks down the chain for the nearest *StdErr that does and returns its status;
// it returns 0 when none is found.
func (e *StdErr) Status() int {
	if e.status != 0 {
		return e.status
	}
	var next *StdErr
	if errors.As(e.cause, &next) {
		return next.Status()
	}
	return 0
}

// StackTrace returns the captured call stack. If this error wraps another
// *StdErr, it returns that error's trace so the origin frames are preserved.
// Otherwise it captures the trace on first call and caches it.
func (e *StdErr) StackTrace() []tracer.Frame {
	var next *StdErr
	if errors.As(e.cause, &next) {
		return next.StackTrace()
	}
	e.once.Do(func() {
		e.traces = tracer.Trace(tracer.Config{
			Skip:     e.skip,
			MaxDepth: e.maxDepth,
		})
	})
	return e.traces
}

// Code returns the application error code carried by the nearest *StdErr in
// err's chain, or "" when none is present.
func Code(err error) string {
	var e *StdErr
	if errors.As(err, &e) {
		return e.Code()
	}
	return ""
}

// Status returns the HTTP status carried by the nearest *StdErr in err's chain,
// or 0 when none is present.
func Status(err error) int {
	var e *StdErr
	if errors.As(err, &e) {
		return e.Status()
	}
	return 0
}

// StackTrace returns the stack trace carried by the nearest *StdErr in err's
// chain, or nil when none is present.
func StackTrace(err error) []tracer.Frame {
	var e *StdErr
	if errors.As(err, &e) {
		return e.StackTrace()
	}
	return nil
}
