package httpkit

import (
	"testing"

	"gorontalo/pkg/httpkit/stdwrapper"
)

func TestHeadersMirrorStdwrapper(t *testing.T) {
	if Headers.RequestID != stdwrapper.HeaderRequestID {
		t.Fatalf("Headers.RequestID = %q, want %q", Headers.RequestID, stdwrapper.HeaderRequestID)
	}
	if Headers.ErrorCode != stdwrapper.HeaderErrorCode {
		t.Fatalf("Headers.ErrorCode = %q, want %q", Headers.ErrorCode, stdwrapper.HeaderErrorCode)
	}
}

func TestHeaderValues(t *testing.T) {
	if Headers.RequestID != "X-Request-ID" {
		t.Fatalf("Headers.RequestID = %q, want %q", Headers.RequestID, "X-Request-ID")
	}
	if Headers.ErrorCode != "X-Error-Code" {
		t.Fatalf("Headers.ErrorCode = %q, want %q", Headers.ErrorCode, "X-Error-Code")
	}
}
