package stdwrapper

import "gorontalo/pkg/logger"

// Config holds the service-wide settings shared by every handler a Wrapper
// produces. Create a Wrapper once with New(Config{...}) and reuse it across
// routes; per-route overrides go in RouteConfig passed to Wrap.
//
// The zero value is valid: empty fields fall back to the package defaults
// (HeaderRequestID, HeaderErrorCode, DefaultMaxBodyBytes, defaultSuccessMessage,
// defaultErrorMessage), so New(Config{}) yields a wrapper with standard
// behavior.
type Config struct {
	// RequestIDHeader is the header read for the incoming request ID and echoed
	// back on the response. Empty selects HeaderRequestID.
	RequestIDHeader string

	// ErrorCodeHeader is the response header carrying the application error
	// code. Empty selects HeaderErrorCode.
	ErrorCodeHeader string

	// MaxBodyBytes caps the number of bytes read from the request body, guarding
	// against unbounded reads from a client. The wrapper swaps r.Body with an
	// http.MaxBytesReader before invoking the handler, so any body read (for
	// example via httpkit.DecodeJSON) is bounded and a handler reading past the
	// limit sees an error. Zero selects DefaultMaxBodyBytes; a negative value
	// disables the cap.
	MaxBodyBytes int64

	// SuccessMessage is the default envelope message sent on successful
	// responses. Empty selects defaultSuccessMessage. A route may override it
	// with RouteConfig.SuccessMessage.
	SuccessMessage string

	// ErrorMessage is the default fallback envelope message sent on failures
	// when the error carries no client-facing message of its own. Empty selects
	// defaultErrorMessage. A route may override it with RouteConfig.ErrorMessage.
	ErrorMessage string

	// Logger receives error-level logs for failed and panicking requests. It is
	// an injected dependency: there is no package-level default. When nil, the
	// wrapper runs without logging (request handling is otherwise unchanged).
	Logger *logger.Logger
}

// RouteConfig holds per-route overrides applied on top of a Wrapper's global
// Config. The zero value is valid and changes nothing: every empty field
// inherits the Wrapper's corresponding Config value.
type RouteConfig struct {
	// SuccessMessage overrides Config.SuccessMessage for this route. Empty
	// inherits the global value.
	SuccessMessage string

	// ErrorMessage overrides Config.ErrorMessage for this route. Empty inherits
	// the global value.
	ErrorMessage string
}

// resolvedConfig holds the effective settings after globals and any per-route
// overrides are applied. It is unexported so the resolved form never leaks into
// the public API.
type resolvedConfig struct {
	successMessage  string
	errorMessage    string
	requestIDHeader string
	errorCodeHeader string
	maxBodyBytes    int64
	logger          *logger.Logger
}

// resolved returns the global settings with empty fields replaced by their
// defaults. It performs the defensive validation the Wrapper relies on, so the
// rest of the package can assume every field is set.
func (c Config) resolved() resolvedConfig {
	r := resolvedConfig{
		successMessage:  c.SuccessMessage,
		errorMessage:    c.ErrorMessage,
		requestIDHeader: c.RequestIDHeader,
		errorCodeHeader: c.ErrorCodeHeader,
		maxBodyBytes:    c.MaxBodyBytes,
		logger:          c.Logger,
	}
	if r.successMessage == "" {
		r.successMessage = defaultSuccessMessage
	}
	if r.errorMessage == "" {
		r.errorMessage = defaultErrorMessage
	}
	if r.requestIDHeader == "" {
		r.requestIDHeader = HeaderRequestID
	}
	if r.errorCodeHeader == "" {
		r.errorCodeHeader = HeaderErrorCode
	}
	switch {
	case r.maxBodyBytes == 0:
		r.maxBodyBytes = DefaultMaxBodyBytes // zero value -> default
	case r.maxBodyBytes < 0:
		r.maxBodyBytes = 0 // negative -> disabled (no cap applied)
	}
	return r
}

// withRoute returns a copy of the resolved global config with rc's non-empty
// per-route overrides applied. The header and body-limit settings are
// service-wide and are never overridden per route.
func (r resolvedConfig) withRoute(rc RouteConfig) resolvedConfig {
	if rc.SuccessMessage != "" {
		r.successMessage = rc.SuccessMessage
	}
	if rc.ErrorMessage != "" {
		r.errorMessage = rc.ErrorMessage
	}
	return r
}
