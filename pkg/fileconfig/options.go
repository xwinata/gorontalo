package fileconfig

import (
	"net/http"
	"time"

	"gorontalo/pkg/stderr"
)

// Config customizes a Loader created by New. Only Path is required; every other
// field has a usable zero value.
type Config[T any] struct {
	// Path is the configuration file to read. A relative path is resolved
	// against the process working directory (the app root); an absolute path is
	// used as-is. Required: an empty Path makes New fail.
	Path string

	// TTL is how long a successfully loaded value is cached and reused before
	// Load re-reads the file. Zero (the default) disables caching: every Load
	// re-reads and re-decodes the file. A positive TTL caches the value for
	// that duration, after which the next Load re-reads. A negative value is
	// treated as zero. Ignored when CachePermanently is set.
	TTL time.Duration

	// CachePermanently, when true, reads the file once on the first Load and
	// returns that cached value for every later Load. It overrides TTL.
	CachePermanently bool

	// Decoder decodes the raw file bytes into *T. When nil, encoding/json is
	// used. Supply a YAML (or other) decoder here to read other formats without
	// this package depending on them.
	Decoder func(data []byte, v any) error

	// Validate, when non-nil, checks the decoded value after a successful
	// decode. A non-nil error it returns is surfaced to the Load caller wrapped
	// with CodeValidationFailed. When nil, the only decode-stage error a caller
	// sees is the underlying unmarshal error (wrapped with CodeDecodeFailed).
	Validate func(*T) error
}

// resolvedConfig holds the effective settings after defaults are applied.
type resolvedConfig[T any] struct {
	path      string
	ttl       time.Duration
	permanent bool
	decode    func(data []byte, v any) error
	validate  func(*T) error
}

// resolved validates Config and applies defaults: a nil Decoder becomes the
// JSON decoder, a negative TTL is treated as zero (no caching), and a
// relative Path is made absolute against the app root. CachePermanently is
// carried through and overrides TTL at load time. It returns a *stderr.StdErr
// with CodeInvalidConfig when Path is empty or cannot be resolved.
func (c Config[T]) resolved() (resolvedConfig[T], error) {
	if c.Path == "" {
		return resolvedConfig[T]{}, stderr.New(nil, stderr.Options{
			Message: "fileconfig: Config.Path is required",
			Code:    CodeInvalidConfig,
			Status:  http.StatusInternalServerError,
		})
	}

	path, err := resolvePath(c.Path)
	if err != nil {
		return resolvedConfig[T]{}, stderr.New(err, stderr.Options{
			Message: "fileconfig: resolve path " + c.Path,
			Code:    CodeInvalidConfig,
			Status:  http.StatusInternalServerError,
		})
	}

	decode := c.Decoder
	if decode == nil {
		decode = jsonDecode
	}

	ttl := c.TTL
	if ttl < 0 {
		ttl = 0
	}

	return resolvedConfig[T]{
		path:      path,
		ttl:       ttl,
		permanent: c.CachePermanently,
		decode:    decode,
		validate:  c.Validate,
	}, nil
}
