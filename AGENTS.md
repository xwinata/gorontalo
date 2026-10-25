# AGENTS.md

Guidance for AI coding agents (and humans) working in this repository. This is
the canonical, tool-agnostic source of conventions. It follows the open
[AGENTS.md](https://agents.md) convention and is read by most agentic coding
tools. These conventions apply regardless of which agent or editor you use — do
not introduce agent- or tool-specific rules that diverge from what is written
here. If a particular tool needs its own config, keep it thin and have it defer
to this file rather than restating or overriding it.

## Project overview

`gorontalo` (`module gorontalo`, Go 1.27) is a single Go module intended to hold all
of this author's Go code. It follows the
[golang-standards/project-layout](https://github.com/golang-standards/project-layout)
convention, so directories are added as the repo grows — for example `cmd/`
(application entrypoints), `internal/` (private code), `configs/`, `examples/`,
and so on. Only add a top-level directory when there is code to put in it, and
place it according to that layout.

Import paths are module-relative, e.g. `import "gorontalo/pkg/logger"`.

### Current layout

- `pkg/` — public, reusable library packages. Each is self-contained and
  independently documented:
  - `pkg/logger` — structured logging on `log/slog`, zero third-party deps.
  - `pkg/tracer` — call-stack capture for diagnostics.
  - `pkg/serialqueue` — runs jobs one at a time with a bounded FIFO backlog.
  - `pkg/database` — database helpers.
  - `pkg/httpkit` — net/http presets. `healthcheck` serves common
    health, liveness, and readiness endpoints; `stdwrapper` adapts
    application handlers to a standard request/response contract (request ID,
    response envelope, error-code header, logging, panic recovery).
  - `pkg/stderr` — errors carrying a message, stack trace (via
    `pkg/tracer`), an application error code, and an HTTP status.
- `tools/` — build-time tooling (see "Documentation" below).

As the repo takes on binaries, private packages, configs, and examples, expect
`cmd/`, `internal/`, `configs/`, `examples/`, and similar directories from the
standard layout to appear alongside these.

## Core principles

These are the load-bearing conventions. Match them in any new or modified code.

### 1. Minimal dependencies, prefer the standard library

Packages lean on the Go standard library. `logger` and `tracer` have zero
third-party *and* zero in-repo dependencies and must stay that way — they are
the foundation other packages build on. `stderr` uses only the standard library
plus `tracer`. `httpkit` (including `healthcheck`) and `database` use only the
standard library plus other in-repo `pkg/` packages: notably they return errors
via `stderr` so failures carry an application code and HTTP status. Standardize
error returns on `stderr` at and above the HTTP/service boundary; keep the
foundational `logger`/`tracer` packages on stdlib errors. Do not add a
third-party dependency to a `pkg/` package unless there is no reasonable stdlib
path and the user has agreed. Third-party modules in `go.mod` are build/tooling
deps, not runtime deps of the library packages.

### 2. Parameter struct for configuration

Configurable constructors take a single exported `Config` (or `Params`) struct
rather than functional options or long positional parameter lists. This favors
discoverability: every setting shows up by name with its type and doc comment,
and a struct literal at the call site reads top-to-bottom.

- A public `Config`/`Params` struct holds the settings as exported fields. Its
  **zero value is valid** and selects sensible defaults, so `New(Config{})`
  works and callers set only the fields they care about.
- The constructor (`New`, or an `Init`) resolves the struct into the package's
  internal state, applying defaults and **defensive validation**: ignore
  empty/`nil` fields, clamp out-of-range values to a sane bound, keep the
  default for no-op values, and copy caller-supplied maps/slices rather than
  retaining them. The constructor never panics on bad input.
- Keep the resolved form unexported (e.g. an internal `config`/`resolvedConfig`)
  so it never leaks into the public API. A small `func (c Config) resolved()`
  method is the usual home for the defaulting/validation logic.

Example shape (from `pkg/serialqueue`):

```go
type Config struct {
    // Capacity is the backlog size. Values below 1 are clamped to 1; zero
    // selects DefaultCapacity.
    Capacity int
}

type resolvedConfig struct{ capacity int }

func (c Config) resolved() resolvedConfig {
    capacity := c.Capacity
    switch {
    case capacity == 0:
        capacity = DefaultCapacity // zero value -> default
    case capacity < 1:
        capacity = 1 // clamp, don't panic
    }
    return resolvedConfig{capacity: capacity}
}

func New(cfg Config) *Queue {
    rc := cfg.resolved()
    // ...
}
```

Because the zero value cannot be distinguished from an explicit zero, pick field
semantics that make the zero value mean "default" (e.g. `Capacity == 0` selects
`DefaultCapacity`). When a caller genuinely needs a distinct "off" state, add an
explicit boolean field for it (e.g. `logger.Config.DisableBuiltinWriters`)
rather than overloading the zero value.

Exported defaults are named constants (e.g. `DefaultCapacity`) so callers can
reference them. Keep a default unexported when callers are unlikely to need it
(e.g. the health-check paths in `pkg/httpkit/healthcheck`).

Do not add functional options (`WithX(...) Option`) to new or existing `pkg/`
code; the parameter struct is the standard across every configurable
constructor here (`logger.New`, `tracer.Trace`, `serialqueue.New`,
`httpkit/healthcheck.New`, `httpkit/stdwrapper.Wrap`, and
`stderr.New`).

### 3. Documentation is part of the code

Every package has a package-level doc comment on the `package` clause that:

- Explains what the package does and its key design decisions.
- Includes a `# Usage` section with a short, runnable example.
- Notes gotchas (e.g. "the zero value is not usable; create one with New").

Every exported identifier (type, func, const, method) has a doc comment written
as full sentences starting with the identifier's name, per Go convention.
Unexported identifiers get comments when the intent is non-obvious.

### 4. Per-package README via gomarkdoc

Each package's `README.md` is generated from its Go doc comments by
[gomarkdoc](https://github.com/princjef/gomarkdoc) and starts with
`<!-- Code generated by gomarkdoc. DO NOT EDIT -->`. Do not hand-edit these
files. To improve a README, improve the doc comments in the `.go` files, then
regenerate (see commands below).

### 5. Enumerations: typed `int` with `iota` and a `String()` method

Model a closed set of named states (statuses, kinds, modes) as a defined
integer type whose constants are declared with `iota`, not as a string type.
Give the type a `String() string` method that maps each value to its
human-readable label, with a `default` arm returning `"unknown"` so an
out-of-range value never renders as a bare number. This keeps comparisons and
storage cheap, makes the zero value a meaningful default, and lets the label
change without touching call sites.

```go
// Status is the condition reported by a probe. Its zero value is StatusOK.
type Status int

const (
	StatusOK Status = iota
	StatusUnavailable
)

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
```

When such a type crosses a serialization boundary (JSON, config), the wire form
must be the stable string label, not the underlying int. Add `MarshalJSON`
(encode `String()`) and `UnmarshalJSON` (parse the label, erroring on an
unrecognized value) so the external representation stays stable even if the
constant order changes:

```go
func (s Status) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
}

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
		return fmt.Errorf("pkgname: unknown status %q", label)
	}
	return nil
}
```

Choose the `iota` ordering so the zero value is the sensible default state. In
tests and log messages, format these values with `%v`/`%s` (which invoke
`String()`), not `%q` or `%d`.

### 6. Discoverability comes from shallow paths, not re-export facades

As a package family grows (e.g. `pkg/httpkit`), favor many small, focused
sub-packages reachable by a predictable, shallow import path over a single
parent that forwards everyone's symbols. Go has no namespaces and no re-export
syntax: a "one import exposes everything" facade can only be built by
redeclaring each child symbol in the parent (type aliases for types, assignment
for consts/vars, and functions cannot be aliased at all — generic functions
least of all). That mirror has to be hand-maintained for every new symbol and
ages badly. Do not build one.

Discoverability is instead achieved by:

- **Shallow, self-evident paths.** One path segment per concept, no empty
  grouping directories (`httpkit/healthcheck`, not `httpkit/handlers/healthcheck`).
  The import path *is* the discovery path, the same way the standard library
  uses `net/http`, `net/url`, `net/http/httptest`.
- **A parent `doc.go`/package comment that indexes the sub-packages**, one line
  each with the full import path. This renders as a navigable table of contents
  on `go doc` and pkg.go.dev. Keep it updated when a sub-package is added.
- **A lean parent package** that holds only genuinely shared contracts — things
  used *across* children or *without* any specific child (e.g.
  `httpkit.Headers`, `httpkit.DecodeJSON`) — never forwarded copies of child
  symbols.

The one facade that is idiomatic is a **composition entry point**: a parent
function that imports the children and offers a high-value combined behavior
(e.g. a hypothetical `httpkit.Install(mux, …)` that wires every standard
endpoint onto a mux). That adds behavior rather than forwarding names. Add it
only when a concrete "wire it all up at once" use case exists, not
speculatively.

## Code style

- Format with `gofmt`. The linter also enforces `golines` and `goimports`, so
  keep lines reasonable and imports ordered/grouped.
- Keep exported surface minimal; prefer unexported types and helpers.
- Return errors, do not panic, in library code. Recover from panics only where
  a package explicitly owns that responsibility (e.g. `serialqueue`'s worker).
- Guard for `nil`/empty/invalid inputs at API boundaries and degrade gracefully.
- Comments explain *why*, not *what*; keep them accurate when code changes.

## Testing

- Use the standard `testing` package. No third-party assertion libraries.
- Factor repeated setup into small helpers that call `t.Helper()`.
- For HTTP, use `net/http/httptest`.
- To capture a package's output in tests, prefer an injectable sink over global
  state (e.g. `logger.WithWriter(buf)` writing to a `bytes.Buffer`).
- Test names describe behavior, e.g. `TestReadinessFailingCheck`.
- Do not add tests unless the change warrants them, but new features and bug
  fixes should come with tests.

## Commands

Run from the module root.

```bash
# Format
gofmt -w .

# Lint (golangci-lint v2, config in .golangci.yaml)
golangci-lint run

# Vet
go vet ./...

# Test (single run, not watch mode)
go test ./...

# Test one package, verbose
go test ./pkg/httpkit/healthcheck/ -v

# Regenerate all package READMEs from doc comments
go generate ./tools
```

## Commits

Write commit messages as [Conventional Commits](https://www.conventionalcommits.org/):
a `type(scope): summary` subject line, where `scope` is the affected package or
area (e.g. `fileconfig`, `httpkit`, `mod`). Types used in this repo: `feat`,
`fix`, `refactor`, `docs`, `chore`, `build`. Keep the summary imperative and
under ~70 characters; use the body to explain *what* and *why*. Keep unrelated
changes in separate commits.

## Definition of done

Before considering a change complete:

1. `gofmt` clean and `go vet ./...` passes.
2. `go test ./...` passes (add/adjust tests for the change).
3. Doc comments updated for any new or changed exported symbols.
4. Affected package `README.md` regenerated via `go generate ./tools` (never
   hand-edited).
5. New `pkg/` code stays stdlib-first and uses the parameter-struct pattern
   (section 2) where it is configurable.

## Adding code

Place new code according to the standard layout: reusable libraries in `pkg/`,
application entrypoints in `cmd/<binary>/`, code private to this module in
`internal/`, and so on. Create a top-level directory only when there is code to
put in it.

A new library package under `pkg/<name>` should include:

- `<name>.go` with the package doc comment (`# Usage` + example) and core types.
- `options.go` if it is configurable, holding the exported `Config`/`Params`
  struct and its `resolved()` method (parameter struct as in section 2).
- `<name>_test.go` with behavior-focused tests.
- A generated `README.md` (produced by `go generate ./tools`, not by hand).

A new binary under `cmd/<name>/` should keep `main` thin — parse flags/config,
wire dependencies, and delegate to packages under `pkg/` or `internal/`. Keep
business logic out of `main` so it stays testable.
