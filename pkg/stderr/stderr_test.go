package stderr

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestNewWrapsCauseAndMessage(t *testing.T) {
	cause := errors.New("boom")
	err := New(cause, Options{Message: "load failed"})

	if got, want := err.Error(), "load failed: boom"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, cause) {
		t.Fatal("errors.Is should find the wrapped cause")
	}
}

func TestNewNoCauseUsesMessageOnly(t *testing.T) {
	err := New(nil, Options{Message: "standalone"})
	if got := err.Error(); got != "standalone" {
		t.Fatalf("Error() = %q, want %q", got, "standalone")
	}
	if err.Unwrap() != nil {
		t.Fatal("Unwrap should be nil when there is no cause")
	}
}

func TestNewEmptyMessageWithCauseRendersCause(t *testing.T) {
	cause := errors.New("boom")
	err := New(cause, Options{})
	if got := err.Error(); got != "boom" {
		t.Fatalf("Error() = %q, want %q", got, "boom")
	}
}

func TestCodeAndStatus(t *testing.T) {
	err := New(nil, Options{
		Message: "nope",
		Code:    "USER_NOT_FOUND",
		Status:  http.StatusNotFound,
	})
	if got := err.Code(); got != "USER_NOT_FOUND" {
		t.Fatalf("Code() = %q, want %q", got, "USER_NOT_FOUND")
	}
	if got := err.Status(); got != http.StatusNotFound {
		t.Fatalf("Status() = %d, want %d", got, http.StatusNotFound)
	}
}

func TestInvalidStatusIgnored(t *testing.T) {
	err := New(nil, Options{Message: "x", Status: 42})
	if got := err.Status(); got != 0 {
		t.Fatalf("Status() = %d, want 0 for out-of-range input", got)
	}
}

func TestCodeAndStatusInheritedFromCause(t *testing.T) {
	inner := New(nil, Options{
		Message: "inner",
		Code:    "INNER",
		Status:  http.StatusBadRequest,
	})
	outer := New(inner, Options{Message: "outer"})

	if got := outer.Code(); got != "INNER" {
		t.Fatalf("Code() = %q, want inherited %q", got, "INNER")
	}
	if got := outer.Status(); got != http.StatusBadRequest {
		t.Fatalf("Status() = %d, want inherited %d", got, http.StatusBadRequest)
	}
}

func TestOuterCodeAndStatusWin(t *testing.T) {
	inner := New(nil, Options{Message: "inner", Code: "INNER", Status: 400})
	outer := New(inner, Options{Message: "outer", Code: "OUTER", Status: 409})

	if got := outer.Code(); got != "OUTER" {
		t.Fatalf("Code() = %q, want %q", got, "OUTER")
	}
	if got := outer.Status(); got != 409 {
		t.Fatalf("Status() = %d, want %d", got, 409)
	}
}

func TestMessageInheritedFromCause(t *testing.T) {
	inner := New(nil, Options{Message: "load user", Code: "USER_NOT_FOUND", Status: 404})
	// Outer sets only a code; message and status should be inherited.
	outer := New(inner, Options{Code: "OVERRIDE"})

	if got := outer.Error(); got != "load user" {
		t.Fatalf("Error() = %q, want inherited message %q without duplication", got, "load user")
	}
	if got := outer.Code(); got != "OVERRIDE" {
		t.Fatalf("Code() = %q, want %q", got, "OVERRIDE")
	}
	if got := outer.Status(); got != 404 {
		t.Fatalf("Status() = %d, want inherited %d", got, 404)
	}
}

func TestInheritedFieldsAreStoredOnNewError(t *testing.T) {
	// Inheritance happens at construction, so a re-wrapped error carries the
	// values on its own fields even if the chain below it is later inspected
	// only through this error.
	inner := New(nil, Options{Message: "inner msg", Code: "INNER", Status: 500})
	outer := New(inner, Options{Message: "outer msg"})

	if got := outer.Code(); got != "INNER" {
		t.Fatalf("Code() = %q, want inherited %q", got, "INNER")
	}
	if got := outer.Status(); got != 500 {
		t.Fatalf("Status() = %d, want inherited %d", got, 500)
	}
	if got := outer.Error(); got != "outer msg: inner msg" {
		t.Fatalf("Error() = %q, want %q", got, "outer msg: inner msg")
	}
}

func TestInheritanceSkipsNonStandardCause(t *testing.T) {
	// A plain cause carries no code/status/message to inherit, so empty fields
	// stay empty.
	err := New(errors.New("boom"), Options{})
	if got := err.Code(); got != "" {
		t.Fatalf("Code() = %q, want empty", got)
	}
	if got := err.Status(); got != 0 {
		t.Fatalf("Status() = %d, want 0", got)
	}
	if got := err.Error(); got != "boom" {
		t.Fatalf("Error() = %q, want %q", got, "boom")
	}
}

func TestInheritanceAcrossPlainErrorInChain(t *testing.T) {
	// stderr -> plain error -> stderr. errors.As finds the
	// nearest *StdErr below, so inheritance reaches through the plain wrapper.
	base := New(nil, Options{Message: "base", Code: "BASE", Status: 403})
	plain := errWrap{msg: "mid", err: base}
	outer := New(plain, Options{Message: "outer"})

	if got := outer.Code(); got != "BASE" {
		t.Fatalf("Code() = %q, want inherited %q", got, "BASE")
	}
	if got := outer.Status(); got != 403 {
		t.Fatalf("Status() = %d, want inherited %d", got, 403)
	}
}

// errWrap is a minimal error that wraps another error, used to test
// inheritance reaching through a non-*StdErr link in the chain.
type errWrap struct {
	msg string
	err error
}

func (e errWrap) Error() string { return e.msg + ": " + e.err.Error() }
func (e errWrap) Unwrap() error { return e.err }

func TestStackTraceCaptured(t *testing.T) {
	err := New(nil, Options{Message: "trace me"})
	frames := err.StackTrace()
	if len(frames) == 0 {
		t.Fatal("expected at least one captured frame")
	}
	// The first frame should be this test function, not an internal frame.
	if !strings.Contains(frames[0].Function, "TestStackTraceCaptured") {
		t.Fatalf("first frame = %q, want it to be the caller of New", frames[0].Function)
	}
}

func TestStackTraceReusedFromCause(t *testing.T) {
	inner := New(nil, Options{Message: "inner"})
	innerTop := inner.StackTrace()[0]

	outer := New(inner, Options{Message: "outer"})
	outerTop := outer.StackTrace()[0]

	if innerTop != outerTop {
		t.Fatalf("outer trace top = %+v, want reused inner top %+v", outerTop, innerTop)
	}
}

func TestPackageHelpers(t *testing.T) {
	plain := errors.New("plain")
	if Code(plain) != "" || Status(plain) != 0 || StackTrace(plain) != nil {
		t.Fatal("helpers should return zero values for a non-standard error")
	}

	err := New(plain, Options{Message: "wrapped", Code: "C", Status: 503})
	if Code(err) != "C" {
		t.Fatalf("Code() = %q, want %q", Code(err), "C")
	}
	if Status(err) != 503 {
		t.Fatalf("Status() = %d, want 503", Status(err))
	}
	if len(StackTrace(err)) == 0 {
		t.Fatal("StackTrace() should return frames for a standard error")
	}
}

func TestErrorsAsFindsStandardError(t *testing.T) {
	err := New(errors.New("x"), Options{Message: "y", Code: "Z"})
	var se *StdErr
	if !errors.As(err, &se) {
		t.Fatal("errors.As should find *StdErr")
	}
	if se.Code() != "Z" {
		t.Fatalf("Code() = %q, want %q", se.Code(), "Z")
	}
}
