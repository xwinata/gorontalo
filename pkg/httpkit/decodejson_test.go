package httpkit

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gorontalo/pkg/httpkit/stdwrapper"
	"gorontalo/pkg/stderr"
)

type createUser struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

func TestDecodeJSONValidPayload(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"ada","age":36}`))

	got, err := DecodeJSON[createUser](req)
	if err != nil {
		t.Fatalf("DecodeJSON returned error: %v", err)
	}
	if got.Name != "ada" || got.Age != 36 {
		t.Fatalf("DecodeJSON = %+v, want {Name:ada Age:36}", got)
	}
}

func TestDecodeJSONUnknownField(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"ada","nope":1}`))

	_, err := DecodeJSON[createUser](req)
	assertDecodeError(t, err)
}

func TestDecodeJSONMalformed(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":`))

	_, err := DecodeJSON[createUser](req)
	assertDecodeError(t, err)
}

func TestDecodeJSONTrailingData(t *testing.T) {
	const payload = `{"name":"ada"}{"name":"bob"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(payload))

	_, err := DecodeJSON[createUser](req)
	assertDecodeError(t, err)
}

func TestDecodeJSONNilRequest(t *testing.T) {
	_, err := DecodeJSON[createUser](nil)
	assertDecodeError(t, err)
}

func TestDecodeJSONEnforcesMaxBody(t *testing.T) {
	big := `{"name":"` + strings.Repeat("a", int(stdwrapper.DefaultMaxBodyBytes)+1) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(big))

	_, err := DecodeJSON[createUser](req)
	assertDecodeError(t, err)
}

func TestDecodeJSONReaderNilBody(t *testing.T) {
	_, err := DecodeJSONReader[createUser](nil)
	assertDecodeError(t, err)
}

func TestDecodeJSONReaderValidPayload(t *testing.T) {
	got, err := DecodeJSONReader[createUser](strings.NewReader(`{"name":"grace","age":45}`))
	if err != nil {
		t.Fatalf("DecodeJSONReader returned error: %v", err)
	}
	if got.Name != "grace" || got.Age != 45 {
		t.Fatalf("DecodeJSONReader = %+v, want {Name:grace Age:45}", got)
	}
}

func TestDecodeJSONMaxEnforcesCustomLimit(t *testing.T) {
	// A 10-byte cap rejects a larger valid payload.
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"ada","age":36}`))
	_, err := DecodeJSONMax[createUser](req, 10)
	assertDecodeError(t, err)
}

func TestDecodeJSONMaxZeroDisablesLimit(t *testing.T) {
	// maxBytes <= 0 decodes unbounded, so a payload larger than the default cap
	// still succeeds.
	want := int(stdwrapper.DefaultMaxBodyBytes) + 1
	big := `{"name":"` + strings.Repeat("a", want) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(big))
	got, err := DecodeJSONMax[createUser](req, 0)
	if err != nil {
		t.Fatalf("DecodeJSONMax with no limit returned error: %v", err)
	}
	if len(got.Name) != want {
		t.Fatalf("decoded Name length = %d, want %d", len(got.Name), want)
	}
}

func TestDecodeJSONErrorTracePointsAtCaller(t *testing.T) {
	_, err := DecodeJSONReader[createUser](strings.NewReader(`{bad`))
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	frames := stderr.StackTrace(err)
	if len(frames) == 0 {
		t.Fatal("expected a captured stack trace")
	}
	// decodeError skips itself and the entry point, so the top frame is this
	// test (the application code that called in).
	if !strings.Contains(frames[0].Function, "TestDecodeJSONErrorTracePointsAtCaller") {
		t.Fatalf("top frame = %q, want it to be the caller", frames[0].Function)
	}
}

// serveDecode runs body through a stdwrapper handler that decodes it with
// DecodeJSON, so DecodeJSON sees the wrapper's body-limit context. It returns
// the HTTP status the wrapper produced.
func serveDecode(cfg stdwrapper.Config, body string) int {
	h := stdwrapper.New(cfg).Wrap(func(_ http.ResponseWriter, r *http.Request) (any, error) {
		return DecodeJSON[createUser](r)
	}, stdwrapper.RouteConfig{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	return rec.Code
}

func TestDecodeJSONDefersToWrapperLargerLimit(t *testing.T) {
	// Body exceeds DecodeJSON's standalone default but is within the wrapper's
	// larger cap, so DecodeJSON must defer to the wrapper and succeed.
	size := int(stdwrapper.DefaultMaxBodyBytes) + 1024
	body := `{"name":"` + strings.Repeat("a", size) + `"}`

	status := serveDecode(stdwrapper.Config{MaxBodyBytes: int64(size) + 512}, body)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (should honor wrapper's larger limit)", status)
	}
}

func TestDecodeJSONDefersToWrapperDisabledLimit(t *testing.T) {
	// Wrapper disables the cap; a body past the standalone default must still
	// decode because DecodeJSON adds no cap of its own behind a wrapper.
	size := int(stdwrapper.DefaultMaxBodyBytes) + 1024
	body := `{"name":"` + strings.Repeat("a", size) + `"}`

	status := serveDecode(stdwrapper.Config{MaxBodyBytes: -1}, body)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (wrapper disabled the cap)", status)
	}
}

func TestDecodeJSONHonorsWrapperSmallerLimit(t *testing.T) {
	// Wrapper caps below the body size; the wrapper's MaxBytesReader trips and
	// DecodeJSON surfaces a 400.
	body := `{"name":"` + strings.Repeat("a", 2048) + `"}`

	status := serveDecode(stdwrapper.Config{MaxBodyBytes: 64}, body)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (wrapper's small cap should trip)", status)
	}
}

func assertDecodeError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if code := stderr.Code(err); code != "" {
		t.Fatalf("error code = %q, want empty (caller's choice)", code)
	}
	if status := stderr.Status(err); status != http.StatusBadRequest {
		t.Fatalf("error status = %d, want %d", status, http.StatusBadRequest)
	}
}
