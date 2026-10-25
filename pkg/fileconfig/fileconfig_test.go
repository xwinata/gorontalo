package fileconfig_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gorontalo/pkg/fileconfig"
	"gorontalo/pkg/stderr"
)

type appConfig struct {
	Addr string `json:"addr"`
	Port int    `json:"port"`
}

// writeTemp writes contents to a file in a temp dir and returns its path.
func writeTemp(t *testing.T, name, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

// codeOf extracts the stderr application code from err, failing the test if err
// is not a *stderr.StdErr.
func codeOf(t *testing.T, err error) string {
	t.Helper()
	var se *stderr.StdErr
	if !errors.As(err, &se) {
		t.Fatalf("expected *stderr.StdErr, got %T: %v", err, err)
	}
	return se.Code()
}

func TestLoadDecodesJSON(t *testing.T) {
	path := writeTemp(t, "app.json", `{"addr":"localhost","port":8080}`)

	loader, err := fileconfig.New(fileconfig.Config[appConfig]{Path: path})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cfg, err := loader.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != "localhost" || cfg.Port != 8080 {
		t.Fatalf("got %+v, want {localhost 8080}", *cfg)
	}
}

func TestNewEmptyPathFails(t *testing.T) {
	_, err := fileconfig.New(fileconfig.Config[appConfig]{})
	if err == nil {
		t.Fatal("expected error for empty Path")
	}
	if got := codeOf(t, err); got != fileconfig.CodeInvalidConfig {
		t.Fatalf("code = %q, want %q", got, fileconfig.CodeInvalidConfig)
	}
}

func TestLoadMissingFile(t *testing.T) {
	loader, err := fileconfig.New(fileconfig.Config[appConfig]{
		Path: filepath.Join(t.TempDir(), "does-not-exist.json"),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = loader.Load(context.Background())
	if got := codeOf(t, err); got != fileconfig.CodeReadFailed {
		t.Fatalf("code = %q, want %q", got, fileconfig.CodeReadFailed)
	}
}

func TestLoadDecodeErrorWithoutValidate(t *testing.T) {
	path := writeTemp(t, "bad.json", `{"port": "not-a-number"}`)

	loader, err := fileconfig.New(fileconfig.Config[appConfig]{Path: path})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = loader.Load(context.Background())
	if got := codeOf(t, err); got != fileconfig.CodeDecodeFailed {
		t.Fatalf("code = %q, want %q", got, fileconfig.CodeDecodeFailed)
	}
}

func TestLoadValidationFailure(t *testing.T) {
	path := writeTemp(t, "app.json", `{"addr":"localhost","port":-1}`)

	loader, err := fileconfig.New(fileconfig.Config[appConfig]{
		Path: path,
		Validate: func(c *appConfig) error {
			if c.Port <= 0 {
				return errors.New("port must be positive")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = loader.Load(context.Background())
	if got := codeOf(t, err); got != fileconfig.CodeValidationFailed {
		t.Fatalf("code = %q, want %q", got, fileconfig.CodeValidationFailed)
	}
}

func TestLoadValidationPasses(t *testing.T) {
	path := writeTemp(t, "app.json", `{"addr":"localhost","port":8080}`)

	loader, err := fileconfig.New(fileconfig.Config[appConfig]{
		Path: path,
		Validate: func(c *appConfig) error {
			if c.Port <= 0 {
				return errors.New("port must be positive")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := loader.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestLoadCustomDecoder(t *testing.T) {
	// A trivial decoder standing in for a non-JSON format: it reads a single
	// "addr=..." line.
	path := writeTemp(t, "app.conf", "addr=example.com\n")

	loader, err := fileconfig.New(fileconfig.Config[appConfig]{
		Path: path,
		Decoder: func(data []byte, v any) error {
			c, ok := v.(*appConfig)
			if !ok {
				return errors.New("unexpected target type")
			}
			line := string(data)
			const prefix = "addr="
			if len(line) <= len(prefix) {
				return errors.New("malformed")
			}
			c.Addr = line[len(prefix) : len(line)-1] // drop trailing newline
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cfg, err := loader.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != "example.com" {
		t.Fatalf("addr = %q, want example.com", cfg.Addr)
	}
}

func TestLoadRespectsCanceledContext(t *testing.T) {
	path := writeTemp(t, "app.json", `{"addr":"localhost","port":8080}`)

	loader, err := fileconfig.New(fileconfig.Config[appConfig]{Path: path})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := loader.Load(ctx); err == nil {
		t.Fatal("expected error from canceled context")
	}
}

func TestLoadZeroTTLRereadsEachTime(t *testing.T) {
	path := writeTemp(t, "app.json", `{"addr":"localhost","port":8080}`)

	// Zero TTL, no permanent cache: every Load re-reads the file.
	loader, err := fileconfig.New(fileconfig.Config[appConfig]{Path: path})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	first, err := loader.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if first.Port != 8080 {
		t.Fatalf("port = %d, want 8080", first.Port)
	}

	if err := os.WriteFile(path, []byte(`{"addr":"localhost","port":9090}`), 0o600); err != nil {
		t.Fatalf("rewrite file: %v", err)
	}

	second, err := loader.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if second.Port != 9090 {
		t.Fatalf("port = %d, want 9090 (file should be re-read with zero TTL)", second.Port)
	}
}

func TestLoadCachePermanently(t *testing.T) {
	path := writeTemp(t, "app.json", `{"addr":"localhost","port":8080}`)

	loader, err := fileconfig.New(fileconfig.Config[appConfig]{
		Path:             path,
		CachePermanently: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	first, err := loader.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if first.Port != 8080 {
		t.Fatalf("port = %d, want 8080", first.Port)
	}

	// Change and then remove the file; a permanent cache must keep serving the
	// first value without touching disk again.
	if err := os.WriteFile(path, []byte(`{"addr":"localhost","port":9090}`), 0o600); err != nil {
		t.Fatalf("rewrite file: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove file: %v", err)
	}

	second, err := loader.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if second.Port != 8080 {
		t.Fatalf("port = %d, want 8080 (CachePermanently serves first value)", second.Port)
	}
}

func TestLoadCachePermanentlyOverridesTTL(t *testing.T) {
	path := writeTemp(t, "app.json", `{"addr":"localhost","port":8080}`)

	// A tiny TTL that would normally expire, but CachePermanently overrides it.
	loader, err := fileconfig.New(fileconfig.Config[appConfig]{
		Path:             path,
		TTL:              time.Millisecond,
		CachePermanently: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := loader.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if err := os.WriteFile(path, []byte(`{"addr":"localhost","port":9090}`), 0o600); err != nil {
		t.Fatalf("rewrite file: %v", err)
	}
	time.Sleep(5 * time.Millisecond) // past the TTL

	second, err := loader.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if second.Port != 8080 {
		t.Fatalf("port = %d, want 8080 (CachePermanently overrides TTL)", second.Port)
	}
}

func TestLoadCachesWithinTTL(t *testing.T) {
	path := writeTemp(t, "app.json", `{"addr":"localhost","port":8080}`)

	// A TTL long enough that it will not elapse during the test.
	loader, err := fileconfig.New(fileconfig.Config[appConfig]{
		Path: path,
		TTL:  time.Minute,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	first, err := loader.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if first.Port != 8080 {
		t.Fatalf("port = %d, want 8080", first.Port)
	}

	// Change the file; within the TTL the cached value must still be returned.
	if err := os.WriteFile(path, []byte(`{"addr":"localhost","port":9090}`), 0o600); err != nil {
		t.Fatalf("rewrite file: %v", err)
	}

	second, err := loader.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if second.Port != 8080 {
		t.Fatalf("port = %d, want 8080 (cached within TTL)", second.Port)
	}
}

func TestLoadRereadsAfterTTLExpires(t *testing.T) {
	path := writeTemp(t, "app.json", `{"addr":"localhost","port":8080}`)

	// A tiny TTL that elapses between the two loads.
	loader, err := fileconfig.New(fileconfig.Config[appConfig]{
		Path: path,
		TTL:  time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := loader.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if err := os.WriteFile(path, []byte(`{"addr":"localhost","port":9090}`), 0o600); err != nil {
		t.Fatalf("rewrite file: %v", err)
	}

	time.Sleep(5 * time.Millisecond)

	second, err := loader.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if second.Port != 9090 {
		t.Fatalf("port = %d, want 9090 (file should be re-read after TTL)", second.Port)
	}
}

func TestLoadCachedValueIsIsolatedCopy(t *testing.T) {
	path := writeTemp(t, "app.json", `{"addr":"localhost","port":8080}`)

	loader, err := fileconfig.New(fileconfig.Config[appConfig]{
		Path: path,
		TTL:  time.Minute,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	first, err := loader.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Mutating the returned value must not corrupt the cache.
	first.Port = 1

	second, err := loader.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if second.Port != 8080 {
		t.Fatalf("port = %d, want 8080 (cache mutated via returned pointer)", second.Port)
	}
}

func TestRelativePathResolvedAgainstWorkingDir(t *testing.T) {
	loader, err := fileconfig.New(fileconfig.Config[appConfig]{
		Path: "configs/app.json",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !filepath.IsAbs(loader.Path()) {
		t.Fatalf("Path() = %q, want absolute", loader.Path())
	}
}
