package stderr

import (
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"testing"
)

// callerLine returns the line of its caller. Used on the same line as a
// constructor call, it yields the line the trace should record.
func callerLine() int {
	_, _, line, _ := runtime.Caller(1)
	return line
}

// thisFile is the path of this test file, as recorded in trace frames.
func thisFile(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return file
}

// errWrap is a non-*StdErr error that wraps another, used to check that
// metadata lookups reach through foreign links in the chain.
type errWrap struct{ err error }

func (e errWrap) Error() string { return "mid: " + e.err.Error() }
func (e errWrap) Unwrap() error { return e.err }

func TestConstructorsMessageAndChain(t *testing.T) {
	cause := errors.New("secret cause")

	tests := []struct {
		name      string
		err       error
		wantMsg   string
		wantCause error // checked with errors.Is when non-nil
	}{
		{"New is verbatim", New("100% done %w"), "100% done %w", nil},
		{"Wrap keeps message", Wrap(cause), "secret cause", cause},
		{"Wrap keeps fmt.Errorf chain", Wrap(fmt.Errorf("x: %w", cause)), "x: secret cause", cause},
		{"Opaque hides cause", Opaque(cause, "internal error"), "internal error", cause},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.wantMsg {
				t.Fatalf("Error() = %q, want %q", got, tt.wantMsg)
			}
			if tt.wantCause != nil && !errors.Is(tt.err, tt.wantCause) {
				t.Fatal("errors.Is should find the cause")
			}
		})
	}
}

func TestNewUnwrapsToPlainRoot(t *testing.T) {
	root := New("boom").Unwrap()
	if root == nil || root.Error() != "boom" {
		t.Fatalf("Unwrap() = %v, want an error with message %q", root, "boom")
	}
	var se *StdErr
	if errors.As(root, &se) {
		t.Fatal("root cause should be a plain error, not *StdErr")
	}
}

func TestHelpersTolerateTypedNil(t *testing.T) {
	// A nil *StdErr stored in an error interface is non-nil; the package-level
	// helpers must return zero values rather than panic.
	var err error = Wrap(nil)
	if Code(err) != "" || HTTPStatus(err) != 0 || StackTrace(err) != nil {
		t.Fatal("helpers should return zero values for a typed-nil *StdErr")
	}
}

func TestWrapAndOpaqueNilReturnNil(t *testing.T) {
	if Wrap(nil) != nil || Opaque(nil, "x") != nil {
		t.Fatal("Wrap/Opaque should return a nil error for nil input")
	}
}

func TestOptionsCodeAndHTTPStatus(t *testing.T) {
	inner := New("inner", WithOptions(Options{Code: "INNER", HTTPStatus: http.StatusBadRequest}))

	tests := []struct {
		name       string
		err        error
		wantCode   string
		wantStatus int
	}{
		{"plain error has none", errors.New("x"), "", 0},
		{"set directly", inner, "INNER", http.StatusBadRequest},
		{"out-of-range status ignored", New("x", WithOptions(Options{HTTPStatus: 42})), "", 0},
		{
			"inherited through Wrap",
			Wrap(fmt.Errorf("outer: %w", inner)),
			"INNER",
			http.StatusBadRequest,
		},
		{"inherited through Opaque", Opaque(inner, "hidden"), "INNER", http.StatusBadRequest},
		{"inherited across foreign link", Wrap(errWrap{inner}), "INNER", http.StatusBadRequest},
		{
			"outer overrides",
			Wrap(inner, WithOptions(Options{Code: "OUTER", HTTPStatus: http.StatusConflict})),
			"OUTER", http.StatusConflict,
		},
		{
			"last WithOptions wins whole",
			New("x",
				WithOptions(Options{Code: "FIRST", HTTPStatus: http.StatusBadRequest}),
				WithOptions(Options{Code: "SECOND"}),
			),
			"SECOND", 0,
		},
		{"nil Option ignored", New("x", nil), "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Code(tt.err); got != tt.wantCode {
				t.Fatalf("Code() = %q, want %q", got, tt.wantCode)
			}
			if got := HTTPStatus(tt.err); got != tt.wantStatus {
				t.Fatalf("HTTPStatus() = %d, want %d", got, tt.wantStatus)
			}
		})
	}
}

func TestStackTraceDefaultIsCallSiteOnly(t *testing.T) {
	cause := errors.New("x")
	opts := WithOptions(Options{Code: "C"})

	tests := []struct {
		name string
		mk   func() (error, int)
	}{
		{"New", func() (error, int) { return New("m"), callerLine() }},
		{"Wrap", func() (error, int) { return Wrap(cause), callerLine() }},
		{"Opaque", func() (error, int) { return Opaque(cause, "m"), callerLine() }},
		{"with options", func() (error, int) { return New("m", opts), callerLine() }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err, line := tt.mk()
			assertTrace(t, err, thisFile(t), line)
		})
	}
}

func TestStackTraceSkipFramesPointsAtHelperCaller(t *testing.T) {
	helper := func() error { return New("m", WithOptions(Options{SkipFrames: 1})) }
	err, line := helper(), callerLine()
	assertTrace(t, err, thisFile(t), line)
}

func TestStackTraceDepthCapturesMore(t *testing.T) {
	err, line := New("m", WithOptions(Options{TraceDepth: 3})), callerLine()
	frames := StackTrace(err)
	if len(frames) != 3 {
		t.Fatalf("len(trace) = %d, want 3", len(frames))
	}
	if frames[0].File != thisFile(t) || frames[0].Line != line {
		t.Fatalf("top frame = %s:%d, want line %d", frames[0].File, frames[0].Line, line)
	}
}

func TestStackTraceReusedFromCause(t *testing.T) {
	inner, line := New("inner"), callerLine()

	for name, outer := range map[string]error{
		"Wrap":   Wrap(fmt.Errorf("outer: %w", inner)),
		"Opaque": Opaque(inner, "outer"),
	} {
		t.Run(name, func(t *testing.T) {
			assertTrace(t, outer, thisFile(t), line)
		})
	}
}

func TestStackTraceNilForPlainError(t *testing.T) {
	if StackTrace(errors.New("plain")) != nil {
		t.Fatal("StackTrace should be nil for a non-standard error")
	}
}

// assertTrace checks that err's trace is exactly one frame at file:line.
func assertTrace(t *testing.T, err error, file string, line int) {
	t.Helper()
	frames := StackTrace(err)
	if len(frames) != 1 {
		t.Fatalf("len(trace) = %d, want 1 (call site only): %+v", len(frames), frames)
	}
	if got := frames[0]; got.File != file || got.Line != line {
		t.Fatalf("frame = %s:%d, want %s:%d", got.File, got.Line, file, line)
	}
	if !strings.HasPrefix(frames[0].Function, "github.com/xwinata/gorontalo/pkg/stderr.Test") {
		t.Fatalf("frame function = %q, want a test function", frames[0].Function)
	}
}
