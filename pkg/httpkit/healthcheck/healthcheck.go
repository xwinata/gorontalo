// Package healthcheck registers a small set of common HTTP endpoints on a
// standard library *http.ServeMux. It has no third-party dependencies.
//
// It exposes three probes, each on its own path so orchestrators such as
// Kubernetes can target them independently:
//
//   - a health check (GET /healthcheck) that confirms the process is up,
//   - a liveness probe (GET /livez) that fails when the process is
//     unrecoverable and should be restarted,
//   - a readiness probe (GET /readyz) that fails when the process is up but
//     not yet able to serve traffic, so it is pulled from rotation.
//
// Liveness and readiness run any checks registered for them and report ready
// only when every check passes. A failing probe returns 503 with a JSON body
// naming the failed checks; a passing probe returns 200.
//
// # Usage
//
//	h := healthcheck.New(healthcheck.Config{
//	    ReadinessChecks: map[string]healthcheck.Check{
//	        "db": func(ctx context.Context) error {
//	            return db.PingContext(ctx)
//	        },
//	    },
//	})
//	mux := http.NewServeMux()
//	h.Register(mux)
//	http.ListenAndServe(":8080", mux)
//
// Register may target any *http.ServeMux, so these endpoints can share a mux
// with an application's own routes.
package healthcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"gorontalo/pkg/stderr"
)

// Default paths served by a Handler, overridable with Config.HealthPath,
// Config.ReadinessPath, and Config.LivenessPath.
const (
	// defaultHealthPath is the path served for the simple health check.
	defaultHealthPath = "/healthcheck"
	// defaultReadinessPath is the path served for the readiness probe.
	defaultReadinessPath = "/readyz"
	// defaultLivenessPath is the path served for the liveness probe.
	defaultLivenessPath = "/livez"
)

// Status is the condition reported in a probe response body. Its zero value is
// StatusOK.
type Status int

const (
	// StatusOK is reported when a probe passes.
	StatusOK Status = iota
	// StatusUnavailable is reported when a probe fails.
	StatusUnavailable
)

// String returns the lowercase label for s, e.g. "ok" or "unavailable". Unknown
// values render as "unknown".
func (s Status) String() string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusUnavailable:
		return "unavailable"
	default:
		return "unknown"
	}
}

// MarshalJSON encodes the Status as its String label so probe response bodies
// stay stable (e.g. {"status":"ok"}) regardless of the underlying int values.
func (s Status) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
}

// UnmarshalJSON decodes a Status from its String label. An unrecognized label
// is an error so malformed input is not silently accepted.
func (s *Status) UnmarshalJSON(data []byte) error {
	var label string
	if err := json.Unmarshal(data, &label); err != nil {
		return err
	}
	switch label {
	case "ok":
		*s = StatusOK
	case "unavailable":
		*s = StatusUnavailable
	default:
		return stderr.New(nil, stderr.Options{
			Message: fmt.Sprintf("healthcheck: unknown status %q", label),
			Code:    "INVALID_STATUS",
			Status:  http.StatusBadRequest,
		})
	}
	return nil
}

// response is the JSON body returned by every probe. Failures is populated only
// when one or more checks fail, mapping each failed check's name to its error.
type response struct {
	Status   Status            `json:"status"`
	Failures map[string]string `json:"failures,omitempty"`
}

// Handler serves the health, liveness, and readiness endpoints. Create one with
// New and wire it into a mux with Register.
type Handler struct {
	healthPath    string
	readinessPath string
	livenessPath  string
	readiness     map[string]Check
	liveness      map[string]Check
}

// New creates a Handler configured by cfg. A zero-value Config serves the
// default paths with no readiness or liveness checks (so both probes simply
// confirm the process is up).
func New(cfg Config) *Handler {
	rc := cfg.resolved()

	return &Handler{
		healthPath:    rc.healthPath,
		readinessPath: rc.readinessPath,
		livenessPath:  rc.livenessPath,
		readiness:     rc.readiness,
		liveness:      rc.liveness,
	}
}

// Register mounts the Handler's endpoints on mux. It registers the health,
// liveness, and readiness paths, each restricted to GET.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET "+h.healthPath, h.handleHealth)
	mux.HandleFunc("GET "+h.livenessPath, h.handleLiveness)
	mux.HandleFunc("GET "+h.readinessPath, h.handleReadiness)
}

// handleHealth answers the simple health check. It always reports OK, since
// reaching this handler means the process is serving requests.
func (h *Handler) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, response{Status: StatusOK})
}

// handleLiveness runs the liveness checks and reports the outcome.
func (h *Handler) handleLiveness(w http.ResponseWriter, r *http.Request) {
	writeProbe(w, r, h.liveness)
}

// handleReadiness runs the readiness checks and reports the outcome.
func (h *Handler) handleReadiness(w http.ResponseWriter, r *http.Request) {
	writeProbe(w, r, h.readiness)
}

// writeProbe runs every check and writes the result: 200 when all pass, 503
// with the failing checks named when any fail. Checks run sequentially so a
// slow one is bounded by the request context rather than racing the others.
func writeProbe(w http.ResponseWriter, r *http.Request, checks map[string]Check) {
	failures := runChecks(r.Context(), checks)
	if len(failures) > 0 {
		writeJSON(w, http.StatusServiceUnavailable, response{
			Status:   StatusUnavailable,
			Failures: failures,
		})
		return
	}
	writeJSON(w, http.StatusOK, response{Status: StatusOK})
}

// runChecks runs each check with ctx and returns a map of failed check names to
// their error messages. It returns nil when every check passes.
func runChecks(ctx context.Context, checks map[string]Check) map[string]string {
	var failures map[string]string
	for name, fn := range checks {
		if err := fn(ctx); err != nil {
			if failures == nil {
				failures = make(map[string]string)
			}
			failures[name] = err.Error()
		}
	}
	return failures
}

// writeJSON writes body as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, body response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
