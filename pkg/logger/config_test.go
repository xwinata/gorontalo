package logger

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestLevel_NormalizeAndString(t *testing.T) {
	cases := []struct {
		in   Level
		want Level
	}{
		{"", LevelInfo},
		{"DEBUG", LevelDebug},
		{" warn ", LevelWarn},
		{"warning", LevelWarn},
		{"err", LevelError},
		{"bogus", LevelInfo},
	}
	for _, tc := range cases {
		if got := tc.in.normalize(); got != tc.want {
			t.Errorf("Level(%q).normalize() = %v, want %v", string(tc.in), got, tc.want)
		}
		// String() returns the canonical label of the normalized value.
		if got := tc.in.String(); got != string(tc.want) {
			t.Errorf("Level(%q).String() = %q, want %q", string(tc.in), got, string(tc.want))
		}
	}
}

func TestWriter_Normalize(t *testing.T) {
	cases := []struct {
		in   Writer
		want Writer
	}{
		{"stdout", WriterStdout},
		{"STDOUT", WriterStdout},
		{" File ", WriterFile},
	}
	for _, tc := range cases {
		got := tc.in.normalize()
		if got != tc.want {
			t.Errorf("Writer(%q).normalize() = %q, want %q",
				string(tc.in), string(got), string(tc.want))
		}
	}
}

func TestNew_AcceptsCaseInsensitiveStringConfig(t *testing.T) {
	// Values as they might arrive from env vars: mixed case, built-in writer by
	// name. New must accept them without any pre-parsing by the caller.
	buf := &bytes.Buffer{}
	log, err := New(Config{
		Level:                 "ERROR",
		Writers:               []Writer{"STDOUT"}, // overridden below by custom-only
		DisableBuiltinWriters: true,
		CustomWriters:         []io.Writer{buf},
		Format:                FormatJSON,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	log.Info(ctx, "dropped")            // below Error -> filtered
	log.Error(ctx, errors.New("shown")) // at Error -> emitted

	out := buf.String()
	if strings.Contains(out, "dropped") {
		t.Errorf("info line should be filtered at level ERROR: %q", out)
	}
	if !strings.Contains(out, "shown") {
		t.Errorf("error line should be emitted at level ERROR: %q", out)
	}
}

func TestNew_UnknownWriterNameErrors(t *testing.T) {
	_, err := New(Config{Writers: []Writer{"kafka"}})
	if err == nil {
		t.Fatal("expected error for unrecognized writer name")
	}
	if !strings.Contains(err.Error(), "unsupported writer") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLevel_SlogRoundTrip(t *testing.T) {
	cases := []struct {
		lvl  Level
		slog slog.Level
	}{
		{LevelDebug, slog.LevelDebug},
		{LevelInfo, slog.LevelInfo},
		{LevelWarn, slog.LevelWarn},
		{LevelError, slog.LevelError},
	}
	for _, tc := range cases {
		if got := tc.lvl.slog(); got != tc.slog {
			t.Errorf("%v.slog() = %v, want %v", tc.lvl, got, tc.slog)
		}
		if got := fromSlog(tc.slog); got != tc.lvl {
			t.Errorf("fromSlog(%v) = %v, want %v", tc.slog, got, tc.lvl)
		}
	}
}

func TestWithWriters_DisablesBuiltins(t *testing.T) {
	// No built-in writers and no custom writer -> build error.
	_, err := New(Config{DisableBuiltinWriters: true})
	if err == nil {
		t.Fatal("expected error when no writers are configured")
	}
	if !strings.Contains(err.Error(), "no output writers") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestWithWriter_NilIgnored(t *testing.T) {
	// A nil custom writer should be ignored; with no built-ins this errors.
	if _, err := New(Config{
		DisableBuiltinWriters: true,
		CustomWriters:         []io.Writer{nil},
	}); err == nil {
		t.Fatal("expected error: nil writer should not count as a destination")
	}
}

func TestWithFormat_Text(t *testing.T) {
	buf := &bytes.Buffer{}
	log, err := New(Config{
		Level:                 LevelInfo,
		DisableBuiltinWriters: true,
		CustomWriters:         []io.Writer{buf},
		Format:                FormatText,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	log.Info(context.Background(), "hello", Attr{Key: "user_id", Value: 7})

	out := buf.String()
	// Text handler emits key=value, not JSON.
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("expected text output, got JSON-looking line: %q", out)
	}
	if !strings.Contains(out, "msg=hello") || !strings.Contains(out, "user_id=7") {
		t.Errorf("text output missing expected fields: %q", out)
	}
}
