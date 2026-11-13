// Package stderr provides errors that carry a captured stack trace, an
// application-level error code, and an HTTP status. It builds on
// gorontalo/pkg/tracer for stack capture and has no third-party dependencies.
//
// There are three constructors:
//
//   - New(msg) creates an error with the given message and records the stack
//     trace at the call site.
//   - Wrap(err) records a stack trace on an existing error without changing
//     its message.
//   - Opaque(err, msg) reports only msg from Error() but keeps err reachable
//     through errors.Is, errors.As, and Unwrap, so internal causes are not
//     leaked in messages shown to clients.
//
// Messages are plain strings; format them with fmt.Sprintf, or with
// fmt.Errorf and %w before passing the result to Wrap, as needed.
//
// A *StdErr preserves the original error chain, so errors.Is and errors.As
// keep working. The stack trace is captured once, where an error first enters
// this package; wrapping an error that already carries one reuses the original
// trace instead of capturing a new one. By default only the call-site frame is
// recorded; set Options.TraceDepth to capture more of the stack.
//
// The code and status travel with the error so transport layers can report
// them without a separate lookup. The application error code is distinct from
// the HTTP status: the code identifies the failure to the application and its
// clients (e.g. "USER_NOT_FOUND"), while the status is the HTTP status an API
// should return (e.g. 404). Both are looked up down the chain, so an outer
// error that sets neither reports the nearest inner values.
//
// Code, HTTP status, and trace settings are supplied through the Options
// parameter struct, passed to any constructor as WithOptions(opts). The zero
// value of Options is valid.
//
// # Usage
//
//	// A new error with a captured stack trace.
//	return stderr.New("invalid state")
//
//	// Attach a code and HTTP status.
//	return stderr.Wrap(
//	    fmt.Errorf("load user %d: %w", id, err),
//	    stderr.WithOptions(stderr.Options{
//	        Code:       "USER_NOT_FOUND",
//	        HTTPStatus: http.StatusNotFound,
//	    }),
//	)
//
//	// Record a stack trace only; the message is unchanged.
//	return stderr.Wrap(err)
//
//	// Hide the cause from the message but keep it for errors.Is/As and logs.
//	return stderr.Opaque(err, "internal error", stderr.WithOptions(
//	    stderr.Options{HTTPStatus: http.StatusInternalServerError},
//	))
//
// Reading the metadata back:
//
//	var se *stderr.StdErr
//	if errors.As(err, &se) {
//	    log.Println(se.Code(), se.HTTPStatus(), se.StackTrace())
//	}
//
// Wrap and Opaque return a nil *StdErr when err is nil. Calling a method on a
// nil *StdErr panics, so check the result before using it. Beware Go's
// typed-nil rule: a nil *StdErr stored in an error interface is not equal to
// nil. Check err before wrapping rather than returning stderr.Wrap(err)
// unconditionally:
//
//	if err != nil {
//	    return stderr.Wrap(err)
//	}
//	return nil
package stderr

import (
	"errors"

	"github.com/xwinata/gorontalo/pkg/tracer"
)

// StdErr is an error carrying a message, an optional cause, a captured stack
// trace, an application-level error code, and an HTTP status. Create one with
// New, Wrap, or Opaque and inspect it with errors.As.
type StdErr struct {
	msg        string
	cause      error
	opaque     bool
	code       string
	httpStatus int

	traces []tracer.Frame
}

// New returns a *StdErr with the given message and a stack trace captured at
// this call. The message is used verbatim: compose it with fmt.Sprintf first
// if it needs formatting. The result unwraps to errors.New(msg). To attach a
// cause, use Wrap or Opaque. Pass
// WithOptions to set a code, HTTP status, or trace settings.
func New(msg string, opts ...Option) *StdErr {
	return build(opts, msg, nil, false)
}

// Wrap returns err annotated with a stack trace, leaving its message
// unchanged. It returns a nil *StdErr when err is nil. If err already carries a stack
// trace, that trace is reused. To add context to the message, wrap it first
// with fmt.Errorf and %w.
func Wrap(err error, opts ...Option) *StdErr {
	if err == nil {
		return nil
	}
	return build(opts, "", err, false)
}

// Opaque returns an error whose Error() is msg alone, hiding err's message,
// while err stays reachable through Unwrap, errors.Is, and errors.As and its
// stack trace, code, and status are recorded. It returns a nil *StdErr when
// err is nil.
func Opaque(err error, msg string, opts ...Option) *StdErr {
	if err == nil {
		return nil
	}
	return build(opts, msg, err, true)
}

// build assembles a StdErr. It is called directly by every exported
// constructor so the trace skip depth (buildDepth) is constant.
func build(opts []Option, msg string, cause error, opaque bool) *StdErr {
	rc := resolve(opts)
	// An error made by New has no cause of its own; give it a plain root so
	// every *StdErr unwraps to the error it annotates.
	if cause == nil {
		cause = errors.New(msg)
	}
	e := &StdErr{
		msg:        msg,
		cause:      cause,
		opaque:     opaque,
		code:       rc.code,
		httpStatus: rc.httpStatus,
	}
	// Reuse the nearest wrapped trace so it points at the origin; otherwise
	// capture one here, so it reflects the call site.
	var parent *StdErr
	if errors.As(cause, &parent) {
		e.traces = parent.traces
	} else {
		e.traces = tracer.Trace(tracer.Config{
			Skip:     rc.skip,
			MaxDepth: rc.maxDepth,
		})
	}
	return e
}

// Error implements the error interface. For an opaque error it is the message
// alone; otherwise it is the cause's message.
func (e *StdErr) Error() string {
	if e.opaque {
		return e.msg
	}
	return e.cause.Error()
}

// Unwrap returns the wrapped cause, enabling errors.Is and errors.As to walk
// the chain. This holds for opaque errors too.
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

// HTTPStatus returns the HTTP status. When this error does not set one, HTTPStatus
// looks down the chain for the nearest *StdErr that does and returns its status;
// it returns 0 when none is found.
func (e *StdErr) HTTPStatus() int {
	if e.httpStatus != 0 {
		return e.httpStatus
	}
	var next *StdErr
	if errors.As(e.cause, &next) {
		return next.HTTPStatus()
	}
	return 0
}

// StackTrace returns the frames captured when the error (or the innermost
// wrapped *StdErr) was created. By default this is only the call-site frame;
// set Options.TraceDepth to capture more.
func (e *StdErr) StackTrace() []tracer.Frame {
	return e.traces
}

// Code returns the application error code carried by the nearest *StdErr in
// err's chain, or "" when none is present or it is a nil *StdErr.
func Code(err error) string {
	var e *StdErr
	if errors.As(err, &e) && e != nil {
		return e.Code()
	}
	return ""
}

// HTTPStatus returns the HTTP status carried by the nearest *StdErr in err's chain,
// or 0 when none is present or it is a nil *StdErr.
func HTTPStatus(err error) int {
	var e *StdErr
	if errors.As(err, &e) && e != nil {
		return e.HTTPStatus()
	}
	return 0
}

// StackTrace returns the stack trace carried by the nearest *StdErr in err's
// chain, or nil when none is present or it is a nil *StdErr.
func StackTrace(err error) []tracer.Frame {
	var e *StdErr
	if errors.As(err, &e) && e != nil {
		return e.StackTrace()
	}
	return nil
}
