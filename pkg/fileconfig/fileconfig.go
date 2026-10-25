// Package fileconfig reads a single configuration file from disk and decodes
// it into a caller-chosen type.
//
// It targets the common case: one known file, decoded once into one typed
// struct. It deliberately does not do what Viper does — environment and flag
// merging, remote stores, live reloading, case-insensitive key lookup. For
// that one-file case the standard library is enough, so this package pulls in
// no third-party dependencies: os.ReadFile plus encoding/json by default. The
// decoder is pluggable (Config.Decoder), so a caller that needs YAML can supply
// one without every other caller taking on that dependency.
//
// The type to decode into is a generic parameter, so Load returns a *T with no
// casting:
//
//	type AppConfig struct {
//	    Addr string `json:"addr"`
//	    Port int    `json:"port"`
//	}
//
//	loader, err := fileconfig.New(fileconfig.Config[AppConfig]{
//	    Path: "configs/app.json",
//	})
//	if err != nil {
//	    return err
//	}
//	cfg, err := loader.Load(ctx)
//
// A relative Path is resolved against the process working directory (the app
// root), so "configs/app.json" refers to <root>/configs/app.json. An absolute
// Path is used as-is.
//
// Caching is controlled by TTL and CachePermanently. The zero value of both
// disables caching: every Load re-reads and re-decodes the file. A positive
// TTL caches a successful result for that duration, after which the next Load
// re-reads. CachePermanently reads the file once on the first Load and returns
// that value for every later Load, overriding TTL. The context passed to Load
// bounds each actual read, so a caller can cancel or set a deadline
// independently.
//
// Validation is optional. When Config.Validate is set it runs on the decoded
// value and its error is surfaced to the caller; when it is nil the only
// decode-time error a caller sees is the underlying unmarshal error.
//
// Errors are returned as *stderr.StdErr so they carry an application error code
// and an HTTP status at the service boundary.
//
// # Usage
//
//	loader, err := fileconfig.New(fileconfig.Config[AppConfig]{
//	    Path: "configs/app.json",
//	    TTL:  2 * time.Second,
//	    Validate: func(c *AppConfig) error {
//	        if c.Port <= 0 {
//	            return errors.New("port must be positive")
//	        }
//	        return nil
//	    },
//	})
//	if err != nil {
//	    return err
//	}
//	cfg, err := loader.Load(ctx)
//
// The zero value of Loader is not usable; create one with New.
package fileconfig

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gorontalo/pkg/stderr"
)

// Error codes reported on the *stderr.StdErr returned by New and Load.
const (
	// CodeInvalidConfig means the Config passed to New was unusable (e.g. an
	// empty Path).
	CodeInvalidConfig = "FILECONFIG_INVALID_CONFIG"

	// CodeReadFailed means the file could not be read (missing, no permission,
	// or the read timed out / was canceled).
	CodeReadFailed = "FILECONFIG_READ_FAILED"

	// CodeDecodeFailed means the file contents could not be decoded into T.
	CodeDecodeFailed = "FILECONFIG_DECODE_FAILED"

	// CodeValidationFailed means the decoded value failed Config.Validate.
	CodeValidationFailed = "FILECONFIG_VALIDATION_FAILED"
)

// Loader reads and decodes a configuration file into a *T. It is safe for
// concurrent use. By default (zero TTL, CachePermanently false) every Load
// re-reads the file; a positive Config.TTL caches a result for that duration,
// and Config.CachePermanently caches the first result forever. Create one with
// New.
type Loader[T any] struct {
	cfg resolvedConfig[T]

	mu       sync.Mutex
	cached   *T        // last successfully loaded value, nil until first Load
	loadedAt time.Time // when cached was produced
}

// New validates cfg and returns a Loader that decodes into T. It fails with a
// *stderr.StdErr carrying CodeInvalidConfig when Path is empty. All other
// fields have valid zero values: a zero TTL with CachePermanently false
// disables caching, a nil Decoder selects JSON, and a nil Validate skips
// validation.
func New[T any](cfg Config[T]) (*Loader[T], error) {
	rc, err := cfg.resolved()
	if err != nil {
		return nil, err
	}
	return &Loader[T]{cfg: rc}, nil
}

// Load returns the configured value, decoded into a new T.
//
// Caching depends on Config: with the default (zero TTL, CachePermanently
// false) every call re-reads the file. A positive TTL serves a cached copy
// until the TTL elapses; CachePermanently serves the first result for every
// later call. A cache hit returns a copy of the cached value without touching
// the disk; otherwise Load reads the file, decodes it into a new T, runs the
// optional validator, caches the result when caching is enabled, and returns
// it.
//
// Each actual read is bounded by ctx. On any failure it returns a
// *stderr.StdErr whose code identifies the stage that failed (CodeReadFailed,
// CodeDecodeFailed, or CodeValidationFailed); a failed Load leaves any existing
// cached value untouched.
func (l *Loader[T]) Load(ctx context.Context) (*T, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.cached != nil &&
		(l.cfg.permanent || (l.cfg.ttl > 0 && time.Since(l.loadedAt) < l.cfg.ttl)) {
		cp := *l.cached
		return &cp, nil
	}

	data, err := readFile(ctx, l.cfg.path)
	if err != nil {
		return nil, err
	}

	var out T
	if err := l.cfg.decode(data, &out); err != nil {
		return nil, stderr.New(err, stderr.Options{
			Message: "fileconfig: decode " + l.cfg.path,
			Code:    CodeDecodeFailed,
			Status:  http.StatusUnprocessableEntity,
		})
	}

	if l.cfg.validate != nil {
		if err := l.cfg.validate(&out); err != nil {
			return nil, stderr.New(err, stderr.Options{
				Message: "fileconfig: validate " + l.cfg.path,
				Code:    CodeValidationFailed,
				Status:  http.StatusUnprocessableEntity,
			})
		}
	}

	if l.cfg.permanent || l.cfg.ttl > 0 {
		cached := out
		l.cached = &cached
		l.loadedAt = time.Now()
	}

	return &out, nil
}

// Path returns the resolved absolute path the Loader reads from.
func (l *Loader[T]) Path() string { return l.cfg.path }

// readFile reads path, honoring ctx by racing the read against ctx.Done. The
// read itself is not interruptible, so on cancellation the goroutine finishes
// in the background and its result is discarded.
func readFile(ctx context.Context, path string) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		data, err := os.ReadFile(path)
		ch <- result{data: data, err: err}
	}()

	select {
	case <-ctx.Done():
		return nil, stderr.New(ctx.Err(), stderr.Options{
			Message: "fileconfig: read " + path,
			Code:    CodeReadFailed,
			Status:  http.StatusInternalServerError,
		})
	case r := <-ch:
		if r.err != nil {
			status := http.StatusInternalServerError
			if os.IsNotExist(r.err) {
				status = http.StatusNotFound
			}
			return nil, stderr.New(r.err, stderr.Options{
				Message: "fileconfig: read " + path,
				Code:    CodeReadFailed,
				Status:  status,
			})
		}
		return r.data, nil
	}
}

// jsonDecode is the default Decoder: a strict-enough encoding/json unmarshal.
func jsonDecode(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// resolvePath makes a relative path absolute against the process working
// directory (the app root). An already-absolute path is returned cleaned.
func resolvePath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return abs, nil
}
