// Package stdwrapper adapts application handlers to a standard HTTP contract.
// It wraps a handler that returns a value and an error, reads standard request
// headers, writes a standard JSON response envelope, logs failures, and
// recovers from panics. It depends only on the standard library plus the
// in-repo gorontalo/pkg/logger, gorontalo/pkg/tracer, and gorontalo/pkg/stderr
// packages.
//
// A wrapped handler has the signature HandlerFunc: it returns the data to send
// on success and an error on failure, instead of writing to the
// http.ResponseWriter itself. Build a Wrapper once with New, passing the
// service-wide Config, then call its Wrap method for each route. The wrapper
// turns a HandlerFunc into an *http.Handler and takes care of the
// cross-cutting concerns:
//
//   - Request ID: it reads the request-ID header (HeaderRequestID by default,
//     overridable with Config.RequestIDHeader), generating a random one when
//     absent, stores it on the request context (retrievable with
//     RequestIDFromContext), and echoes it back on the same response header.
//   - Response envelope: successful responses carry {"data": ...,
//     "message": ...}; failed responses carry {"message": ...} with the error
//     message. The envelope never includes the application error code.
//   - Error code: the application-level error code (from
//     gorontalo/pkg/stderr) is sent on the error-code response header
//     (HeaderErrorCode by default, overridable with Config.ErrorCodeHeader),
//     separate from the HTTP status.
//   - Status mapping: the HTTP status comes from the error's Status when set
//     (see gorontalo/pkg/stderr), falling back to 500 for errors and 200
//     for success.
//   - Logging: when a [logger.Logger] is supplied via Config.Logger, failures
//     and recovered panics are logged at error level, including the request ID
//     and (for standard errors) the captured stack trace. With no logger
//     configured, request handling is unchanged but nothing is logged.
//   - Panic recovery: a panic in the handler is recovered, logged, and turned
//     into a 500 response so the server stays up.
//   - Body limit: the request body is capped with http.MaxBytesReader
//     (DefaultMaxBodyBytes by default, overridable with Config.MaxBodyBytes, or
//     disabled with a negative value) before the handler runs, so any body read
//     is bounded against unbounded client input.
//
// Config is service-wide: the header names, the body limit, and the default
// response messages are set once in New and shared by every route. RouteConfig
// carries the per-route overrides (currently the success and error messages);
// an empty field inherits the global Config value.
//
// # Usage
//
//	std := stdwrapper.New(stdwrapper.Config{
//	    RequestIDHeader: "X-Trace-Id", // service-wide
//	    MaxBodyBytes:    2 << 20,
//	})
//
//	mux := http.NewServeMux()
//	mux.Handle("GET /users/{id}", std.Wrap(
//	    func(w http.ResponseWriter, r *http.Request) (any, error) {
//	        u, err := store.Load(r.Context(), r.PathValue("id"))
//	        if err != nil {
//	            return nil, stderr.New(err, stderr.Options{
//	                Message: "user not found",
//	                Code:    "USER_NOT_FOUND",
//	                Status:  http.StatusNotFound,
//	            })
//	        }
//	        return u, nil
//	    },
//	    stdwrapper.RouteConfig{SuccessMessage: "user found"}, // per-route
//	))
//
// Set service-wide behavior with the Config passed to New; override per-route
// messages with the RouteConfig passed to Wrap. Both zero values select
// sensible defaults.
package stdwrapper

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"runtime/debug"

	"gorontalo/pkg/logger"
	"gorontalo/pkg/stderr"
)

// HeaderRequestID is the default request/response header carrying the request
// ID. Override it per wrapper with Config.RequestIDHeader.
const HeaderRequestID = "X-Request-ID"

// HeaderErrorCode is the default response header carrying the application-level
// error code, which is distinct from the HTTP status. Override it per wrapper
// with Config.ErrorCodeHeader.
const HeaderErrorCode = "X-Error-Code"

// defaultSuccessMessage is the envelope message used for successful responses
// when Config.SuccessMessage is empty.
const defaultSuccessMessage = "ok"

// defaultErrorMessage is the envelope message used for failed responses when
// the error carries no client-facing message of its own.
const defaultErrorMessage = "internal server error"

// DefaultMaxBodyBytes is the request body size limit applied when
// Config.MaxBodyBytes is zero. It guards against unbounded reads from a client.
// Set Config.MaxBodyBytes to a negative value to disable the cap.
const DefaultMaxBodyBytes int64 = 1 << 20 // 1 MiB

// HandlerFunc is the contract for a wrapped handler. It returns the data to
// send on success and an error on failure, rather than writing the response
// itself. A nil error means success; the returned data is placed in the
// response envelope's "data" field (which is omitted when data is nil).
type HandlerFunc func(w http.ResponseWriter, r *http.Request) (data any, err error)

// envelope is the standard JSON response body. Data is omitted on failures and
// whenever it is nil; Message is always present.
type envelope struct {
	Data    any    `json:"data,omitempty"`
	Message string `json:"message"`
}

// New creates a Wrapper carrying the service-wide Config. Build one at startup
// and reuse it across routes; the zero-value Config selects standard defaults.
// Per-route settings are supplied to Wrap via RouteConfig.
func New(cfg Config) *Wrapper {
	return &Wrapper{cfg: cfg.resolved()}
}

// Wrapper adapts handlers to the standard contract using a shared, service-wide
// Config. Create it with New and call Wrap for each route. A Wrapper is safe
// for concurrent use; its resolved config is immutable after New.
type Wrapper struct {
	cfg resolvedConfig
}

// Wrap adapts h to the standard contract and returns an http.Handler. The
// RouteConfig applies per-route overrides (message text) on top of the
// Wrapper's global Config; its zero value changes nothing. See the package
// documentation for the full list of behaviors Wrap adds.
func (wp *Wrapper) Wrap(h HandlerFunc, rc RouteConfig) http.Handler {
	return &wrapper{handler: h, cfg: wp.cfg.withRoute(rc)}
}

// wrapper is the http.Handler produced by Wrapper.Wrap.
type wrapper struct {
	handler HandlerFunc
	cfg     resolvedConfig
}

// ServeHTTP implements http.Handler. It wires in the request ID, recovers from
// panics, invokes the wrapped handler, and writes the standard response.
func (wr *wrapper) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID := r.Header.Get(wr.cfg.requestIDHeader)
	if requestID == "" {
		requestID = newRequestID()
	}
	w.Header().Set(wr.cfg.requestIDHeader, requestID)

	ctx := withRequestID(r.Context(), requestID)
	// Record the body cap on the context (even when disabled) so downstream
	// readers like httpkit.DecodeJSON defer to the wrapper's limit instead of
	// imposing their own default.
	ctx = withBodyLimit(ctx, wr.cfg.maxBodyBytes)
	r = r.WithContext(ctx)

	// Cap the request body so a handler reading it (e.g. via
	// httpkit.DecodeJSON) cannot be forced into an unbounded allocation. A
	// resolved limit of 0 means the cap is disabled.
	if wr.cfg.maxBodyBytes > 0 && r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, wr.cfg.maxBodyBytes)
	}

	defer wr.recoverPanic(ctx, w, requestID)

	data, err := wr.handler(w, r)
	if err != nil {
		wr.writeError(ctx, w, requestID, err)
		return
	}
	wr.writeSuccess(w, data)
}

// writeSuccess writes a 200 response with the data envelope.
func (wr *wrapper) writeSuccess(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, envelope{
		Data:    data,
		Message: wr.cfg.successMessage,
	})
}

// writeError logs the failure and writes an error response. The HTTP status
// comes from the error when it carries one (else 500); the application error
// code, when present, is sent on the configured error-code header.
func (wr *wrapper) writeError(
	ctx context.Context,
	w http.ResponseWriter,
	requestID string,
	err error,
) {
	status := stderr.Status(err)
	if status == 0 {
		status = http.StatusInternalServerError
	}

	if code := stderr.Code(err); code != "" {
		w.Header().Set(wr.cfg.errorCodeHeader, code)
	}

	if log := wr.cfg.logger; log != nil {
		log.Error(ctx, err,
			logger.Attr{Key: "event", Value: "request failed"},
			logger.Attr{Key: "request_id", Value: requestID},
			logger.Attr{Key: "status", Value: status},
			logger.Attr{Key: "error_code", Value: stderr.Code(err)},
			logger.Attr{Key: "stack", Value: stderr.StackTrace(err)},
		)
	}

	writeJSON(w, status, envelope{Message: wr.errorMessage(err)})
}

// errorMessage chooses the message shown to the client for err. To avoid
// leaking internal detail from wrapped causes, it surfaces a message only for a
// *stderr.StdErr (the application's own, deliberately client-facing
// message); any other error yields the configured generic default.
func (wr *wrapper) errorMessage(err error) string {
	var se *stderr.StdErr
	if errors.As(err, &se) {
		if msg := se.Error(); msg != "" {
			return msg
		}
	}
	return wr.cfg.errorMessage
}

// recoverPanic recovers a panic from the wrapped handler, logs it with the
// panic's stack, and writes a 500 response. It is a no-op when there is no
// panic.
//
// The stack is captured with runtime/debug.Stack rather than from a
// stderr. A stderr created here would capture the stack of the
// recovery site (this deferred function), since the panicking frames have begun
// to unwind; debug.Stack instead records the full goroutine stack, which still
// includes the panic's origin while this deferred function runs.
func (wr *wrapper) recoverPanic(ctx context.Context, w http.ResponseWriter, requestID string) {
	rec := recover()
	if rec == nil {
		return
	}

	err := stderr.New(nil, stderr.Options{
		Message: "panic recovered",
		Status:  http.StatusInternalServerError,
	})

	if log := wr.cfg.logger; log != nil {
		log.Error(ctx, err,
			logger.Attr{Key: "event", Value: "request panicked"},
			logger.Attr{Key: "request_id", Value: requestID},
			logger.Attr{Key: "panic", Value: rec},
			logger.Attr{Key: "stack", Value: string(debug.Stack())},
		)
	}

	writeJSON(w, http.StatusInternalServerError, envelope{Message: wr.cfg.errorMessage})
}

// newRequestID returns a random 128-bit hex identifier. It falls back to a
// fixed sentinel only if the system entropy source fails, which should not
// happen in practice.
func newRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b)
}

// writeJSON writes body as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, body envelope) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
