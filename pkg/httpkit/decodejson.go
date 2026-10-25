package httpkit

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"gorontalo/pkg/httpkit/stdwrapper"
	"gorontalo/pkg/stderr"
)

// DecodeJSON reads r's body and decodes it into a value of type T. It is the
// request-oriented entry point: closing the body is left to the server, so
// callers get a bounded, single-shot decode. For an explicit, caller-chosen
// limit use DecodeJSONMax.
//
// The applied body limit depends on context. When r was handled by stdwrapper,
// the wrapper has already capped the body to its own Config.MaxBodyBytes (see
// stdwrapper.BodyLimitFromContext), so DecodeJSON defers to that cap and adds
// none of its own — the decode reflects the handling wrapper's limit rather
// than a fixed default. When no wrapper is present (standalone use), DecodeJSON
// applies stdwrapper.DefaultMaxBodyBytes as its own guard.
//
// Decoding is strict: unknown fields are rejected and the body must contain
// exactly one JSON value with no trailing data. On any failure DecodeJSON
// returns the zero value of T and a *stderr.StdErr with HTTP status 400
// and no application error code, so a handler can return it directly for a 400
// or re-wrap it to attach a code of its own.
//
// A nil request, or a request with no body, is treated as an empty payload and
// reported as a bad-request error rather than panicking.
//
// # Usage
//
//	func create(w http.ResponseWriter, r *http.Request) (any, error) {
//	    body, err := httpkit.DecodeJSON[CreateUser](r)
//	    if err != nil {
//	        return nil, err // 400 via stdwrapper
//	    }
//	    return store.Create(r.Context(), body)
//	}
func DecodeJSON[T any](r *http.Request) (T, error) {
	out, err := decodeRequest[T](r, defaultLimitFor(r))
	return out, decodeError(err)
}

// defaultLimitFor returns the body limit DecodeJSON should apply to r. When a
// stdwrapper has already capped the body (recorded on the request context),
// DecodeJSON adds no cap of its own (0), deferring to the wrapper's limit.
// Otherwise it applies the shared default.
func defaultLimitFor(r *http.Request) int64 {
	if r != nil {
		if _, handled := stdwrapper.BodyLimitFromContext(r.Context()); handled {
			return 0 // wrapper already bounded (or intentionally unbounded) the body
		}
	}
	return stdwrapper.DefaultMaxBodyBytes
}

// DecodeJSONMax is DecodeJSON with an explicit body size limit in bytes. A
// maxBytes of zero or less disables the limit, decoding the body unbounded;
// only do that for trusted input. All other behavior — strict decoding and the
// 400 *stderr.StdErr on failure — matches DecodeJSON.
func DecodeJSONMax[T any](r *http.Request, maxBytes int64) (T, error) {
	out, err := decodeRequest[T](r, maxBytes)
	return out, decodeError(err)
}

// DecodeJSONReader decodes a single JSON value from body into a value of type
// T. It is the reader-oriented counterpart to DecodeJSON for callers that have
// already bounded or otherwise prepared the stream (for example with their own
// io.LimitReader), or that are decoding something other than an *http.Request.
//
// It applies the same strict rules as DecodeJSON — unknown fields rejected,
// exactly one value, no trailing data — and returns the same
// *stderr.StdErr (HTTP 400, no application code) on failure. A nil body
// is reported as a bad-request error rather than panicking.
func DecodeJSONReader[T any](body io.Reader) (T, error) {
	if body == nil {
		return *new(T), decodeError(errors.New("nil body"))
	}
	out, err := decode[T](body)
	return out, decodeError(err)
}

// decodeRequest prepares r's body (applying the size limit) and decodes it. It
// returns a plain error; callers wrap it with decodeError so the captured stack
// trace points at the exported entry point's caller.
func decodeRequest[T any](r *http.Request, maxBytes int64) (T, error) {
	if r == nil || r.Body == nil {
		return *new(T), errors.New("empty request body")
	}
	body := io.Reader(r.Body)
	if maxBytes > 0 {
		body = http.MaxBytesReader(nil, r.Body, maxBytes)
	}
	return decode[T](body)
}

// decode performs the strict JSON decode shared by the entry points. It assumes
// body is non-nil and returns a plain error on failure.
func decode[T any](body io.Reader) (T, error) {
	var out T

	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(&out); err != nil {
		return *new(T), err
	}

	// Reject trailing data so "{}{}" or "{} garbage" is an error rather than a
	// silently ignored partial read.
	if dec.More() {
		return *new(T), errors.New("unexpected trailing data after JSON value")
	}

	return out, nil
}

// decodeError wraps a non-nil err as a 400 bad-request standard error, and
// returns nil unchanged. It sets no application error code, leaving that to the
// caller, which can re-wrap the returned error (stderr inherits the
// empty code) to classify the failure for its own clients.
//
// It is called directly from each exported entry point so the stderr
// stack trace, captured at New, is a fixed two frames below the entry point
// (decodeError, then the entry point), making SkipFrames constant regardless of
// which entry point or internal helper produced err.
func decodeError(err error) error {
	if err == nil {
		return nil
	}
	return stderr.New(err, stderr.Options{
		Message: "decode JSON body",
		Status:  http.StatusBadRequest,
		// Skip decodeError and the exported entry point so the trace starts at
		// the application code that called in.
		SkipFrames: 2,
	})
}
