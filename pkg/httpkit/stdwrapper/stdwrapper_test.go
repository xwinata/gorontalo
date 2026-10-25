package stdwrapper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gorontalo/pkg/logger"
	"gorontalo/pkg/stderr"
)

// captureLogs builds a Logger that writes JSON into a buffer and returns both.
// Inject the logger into Config.Logger and assert on the buffer. The logger is
// closed on cleanup.
func captureLogs(t *testing.T) (*logger.Logger, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	log, err := logger.New(logger.Config{
		DisableBuiltinWriters: true,
		CustomWriters:         []io.Writer{buf},
		Level:                 logger.LevelDebug,
		Format:                logger.FormatJSON,
	})
	if err != nil {
		t.Fatalf("logger.New: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log, buf
}

// do serves req against the wrapped handler and returns the recorder.
func do(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) envelope {
	t.Helper()
	var env envelope
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return env
}

func TestSuccessEnvelopeAndStatus(t *testing.T) {
	log, _ := captureLogs(t)
	h := New(Config{Logger: log}).Wrap(func(http.ResponseWriter, *http.Request) (any, error) {
		return map[string]string{"name": "alice"}, nil
	}, RouteConfig{})

	rec := do(h, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	env := decodeEnvelope(t, rec)
	if env.Message != defaultSuccessMessage {
		t.Fatalf("message = %q, want %q", env.Message, defaultSuccessMessage)
	}
	data, ok := env.Data.(map[string]any)
	if !ok || data["name"] != "alice" {
		t.Fatalf("data = %#v, want name=alice", env.Data)
	}
}

func TestRequestIDGeneratedWhenAbsent(t *testing.T) {
	log, _ := captureLogs(t)
	var seen string
	h := New(Config{Logger: log}).Wrap(func(_ http.ResponseWriter, r *http.Request) (any, error) {
		id, ok := RequestIDFromContext(r.Context())
		if !ok {
			t.Fatal("request ID missing from context")
		}
		seen = id
		return nil, nil
	}, RouteConfig{})

	rec := do(h, httptest.NewRequest(http.MethodGet, "/", nil))

	got := rec.Header().Get(HeaderRequestID)
	if got == "" {
		t.Fatal("response missing X-Request-ID header")
	}
	if got != seen {
		t.Fatalf("header id %q != context id %q", got, seen)
	}
}

func TestRequestIDPropagatedFromHeader(t *testing.T) {
	log, _ := captureLogs(t)
	h := New(Config{Logger: log}).Wrap(func(http.ResponseWriter, *http.Request) (any, error) {
		return nil, nil
	}, RouteConfig{})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(HeaderRequestID, "abc-123")
	rec := do(h, req)

	if got := rec.Header().Get(HeaderRequestID); got != "abc-123" {
		t.Fatalf("X-Request-ID = %q, want %q", got, "abc-123")
	}
}

func TestErrorStatusAndErrorCodeHeader(t *testing.T) {
	log, _ := captureLogs(t)
	h := New(Config{Logger: log}).Wrap(func(http.ResponseWriter, *http.Request) (any, error) {
		return nil, stderr.New(nil, stderr.Options{
			Message: "user not found",
			Code:    "USER_NOT_FOUND",
			Status:  http.StatusNotFound,
		})
	}, RouteConfig{})

	rec := do(h, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if got := rec.Header().Get(HeaderErrorCode); got != "USER_NOT_FOUND" {
		t.Fatalf("X-Error-Code = %q, want %q", got, "USER_NOT_FOUND")
	}
	env := decodeEnvelope(t, rec)
	if env.Message != "user not found" {
		t.Fatalf("message = %q, want %q", env.Message, "user not found")
	}
	if env.Data != nil {
		t.Fatalf("data = %#v, want nil on error", env.Data)
	}
}

func TestPlainErrorDefaultsToInternalServerError(t *testing.T) {
	log, _ := captureLogs(t)
	h := New(Config{Logger: log}).Wrap(func(http.ResponseWriter, *http.Request) (any, error) {
		return nil, errors.New("boom")
	}, RouteConfig{})

	rec := do(h, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if got := rec.Header().Get(HeaderErrorCode); got != "" {
		t.Fatalf("X-Error-Code = %q, want empty for plain error", got)
	}
	// A plain error's message must not leak to the client; the generic default
	// is used instead.
	env := decodeEnvelope(t, rec)
	if env.Message != defaultErrorMessage {
		t.Fatalf("message = %q, want generic default %q", env.Message, defaultErrorMessage)
	}
	if strings.Contains(env.Message, "boom") {
		t.Fatalf("message %q leaked the internal error text", env.Message)
	}
}

func TestErrorIsLogged(t *testing.T) {
	log, buf := captureLogs(t)
	h := New(Config{Logger: log}).Wrap(func(http.ResponseWriter, *http.Request) (any, error) {
		return nil, stderr.New(nil, stderr.Options{
			Message: "kaboom",
			Code:    "X",
			Status:  http.StatusBadGateway,
		})
	}, RouteConfig{})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(HeaderRequestID, "rid-1")
	do(h, req)

	out := buf.String()
	if !strings.Contains(out, "request failed") {
		t.Fatalf("log missing failure message: %s", out)
	}
	if !strings.Contains(out, "rid-1") {
		t.Fatalf("log missing request id: %s", out)
	}
}

func TestPanicRecovered(t *testing.T) {
	log, buf := captureLogs(t)
	h := New(Config{Logger: log}).Wrap(func(http.ResponseWriter, *http.Request) (any, error) {
		panic("unexpected")
	}, RouteConfig{})

	rec := do(h, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	env := decodeEnvelope(t, rec)
	if env.Message != defaultErrorMessage {
		t.Fatalf("message = %q, want %q", env.Message, defaultErrorMessage)
	}
	if rec.Header().Get(HeaderRequestID) == "" {
		t.Fatal("panic response missing X-Request-ID header")
	}
	if !strings.Contains(buf.String(), "request panicked") {
		t.Fatalf("log missing panic message: %s", buf.String())
	}
	// The logged stack must reach the panic's origin (the wrapped handler),
	// not just the recovery site, and record the panic value.
	out := buf.String()
	if !strings.Contains(out, "TestPanicRecovered") {
		t.Fatalf("stack missing panic origin frame: %s", out)
	}
	if !strings.Contains(out, "unexpected") {
		t.Fatalf("log missing panic value: %s", out)
	}
}

func TestRouteSuccessMessageOverride(t *testing.T) {
	log, _ := captureLogs(t)
	h := New(Config{Logger: log}).Wrap(
		func(http.ResponseWriter, *http.Request) (any, error) {
			return nil, nil
		},
		RouteConfig{SuccessMessage: "created"},
	)

	rec := do(h, httptest.NewRequest(http.MethodGet, "/", nil))
	env := decodeEnvelope(t, rec)
	if env.Message != "created" {
		t.Fatalf("message = %q, want %q", env.Message, "created")
	}
}

func TestRouteMessageOverridesGlobalDefault(t *testing.T) {
	log, _ := captureLogs(t)
	// Global default success message is "global"; the route overrides it.
	wp := New(Config{SuccessMessage: "global", Logger: log})
	noop := func(http.ResponseWriter, *http.Request) (any, error) { return nil, nil }

	overridden := wp.Wrap(noop, RouteConfig{SuccessMessage: "route"})
	env := decodeEnvelope(t, do(overridden, httptest.NewRequest(http.MethodGet, "/", nil)))
	if env.Message != "route" {
		t.Fatalf("overridden message = %q, want %q", env.Message, "route")
	}

	// A route with no override inherits the global default.
	inherited := wp.Wrap(noop, RouteConfig{})
	env = decodeEnvelope(t, do(inherited, httptest.NewRequest(http.MethodGet, "/", nil)))
	if env.Message != "global" {
		t.Fatalf("inherited message = %q, want %q", env.Message, "global")
	}
}

func TestContextCarriesRequestID(t *testing.T) {
	id, ok := RequestIDFromContext(context.Background())
	if ok || id != "" {
		t.Fatalf("empty context should have no request id, got %q, %v", id, ok)
	}
}

func TestCustomRequestIDHeader(t *testing.T) {
	log, _ := captureLogs(t)
	h := New(Config{RequestIDHeader: "X-Trace-Id", Logger: log}).Wrap(
		func(_ http.ResponseWriter, r *http.Request) (any, error) {
			if id, ok := RequestIDFromContext(r.Context()); !ok || id != "trace-9" {
				t.Fatalf("context request id = %q (%v), want %q", id, ok, "trace-9")
			}
			return nil, nil
		},
		RouteConfig{},
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Trace-Id", "trace-9")
	rec := do(h, req)

	if got := rec.Header().Get("X-Trace-Id"); got != "trace-9" {
		t.Fatalf("X-Trace-Id = %q, want %q", got, "trace-9")
	}
	// The default header must not be used when overridden.
	if got := rec.Header().Get(HeaderRequestID); got != "" {
		t.Fatalf("default %s = %q, want empty when overridden", HeaderRequestID, got)
	}
}

func TestCustomErrorCodeHeader(t *testing.T) {
	log, _ := captureLogs(t)
	h := New(Config{ErrorCodeHeader: "X-App-Error", Logger: log}).Wrap(
		func(http.ResponseWriter, *http.Request) (any, error) {
			return nil, stderr.New(nil, stderr.Options{
				Message: "nope",
				Code:    "E_NOPE",
				Status:  http.StatusBadRequest,
			})
		},
		RouteConfig{},
	)

	rec := do(h, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := rec.Header().Get("X-App-Error"); got != "E_NOPE" {
		t.Fatalf("X-App-Error = %q, want %q", got, "E_NOPE")
	}
	if got := rec.Header().Get(HeaderErrorCode); got != "" {
		t.Fatalf("default %s = %q, want empty when overridden", HeaderErrorCode, got)
	}
}

// readBodyHandler returns a handler that reads the entire request body and
// surfaces any read error, letting a test observe the body-cap behavior.
func readBodyHandler() HandlerFunc {
	return func(_ http.ResponseWriter, r *http.Request) (any, error) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		return string(b), nil
	}
}

func TestBodyCapRejectsOversizedBody(t *testing.T) {
	log, _ := captureLogs(t)
	h := New(Config{MaxBodyBytes: 8, Logger: log}).Wrap(readBodyHandler(), RouteConfig{})

	body := strings.NewReader(strings.Repeat("a", 64))
	rec := do(h, httptest.NewRequest(http.MethodPost, "/", body))

	// Reading past the cap fails, so the handler returns a (plain) error and
	// the wrapper responds 500.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 for oversized body", rec.Code)
	}
}

func TestBodyCapAllowsBodyWithinLimit(t *testing.T) {
	log, _ := captureLogs(t)
	h := New(Config{MaxBodyBytes: 64, Logger: log}).Wrap(readBodyHandler(), RouteConfig{})

	rec := do(h, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("hello")))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d for body within limit", rec.Code, http.StatusOK)
	}
	env := decodeEnvelope(t, rec)
	if env.Data != "hello" {
		t.Fatalf("data = %#v, want %q", env.Data, "hello")
	}
}

func TestBodyCapDefaultApplied(t *testing.T) {
	log, _ := captureLogs(t)
	// Config{} resolves MaxBodyBytes to DefaultMaxBodyBytes.
	h := New(Config{Logger: log}).Wrap(readBodyHandler(), RouteConfig{})

	big := strings.NewReader(strings.Repeat("a", int(DefaultMaxBodyBytes)+1))
	rec := do(h, httptest.NewRequest(http.MethodPost, "/", big))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 past default cap", rec.Code)
	}
}

func TestBodyCapDisabledByNegative(t *testing.T) {
	log, _ := captureLogs(t)
	h := New(Config{MaxBodyBytes: -1, Logger: log}).Wrap(readBodyHandler(), RouteConfig{})

	big := strings.Repeat("a", int(DefaultMaxBodyBytes)+1)
	rec := do(h, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(big)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d with cap disabled", rec.Code, http.StatusOK)
	}
}
