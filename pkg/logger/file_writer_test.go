package logger

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileWriter_WritesDatedFile(t *testing.T) {
	dir := t.TempDir()
	fw, err := newFileWriter(filepath.Join(dir, "app.log"), 1, 1)
	if err != nil {
		t.Fatalf("newFileWriter: %v", err)
	}
	defer fw.Close()

	if _, err := fw.Write([]byte("hello\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := fw.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	suffix := time.Now().Format(time.DateOnly)
	want := filepath.Join(dir, "app-"+suffix+".log")
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("expected dated log file %q: %v", want, err)
	}
	if !strings.Contains(string(data), "hello") {
		t.Errorf("log file missing content, got %q", string(data))
	}
}

func TestFileWriter_CloseFlushes(t *testing.T) {
	dir := t.TempDir()
	fw, err := newFileWriter(filepath.Join(dir, "svc.log"), 64, 3600) // long flush interval
	if err != nil {
		t.Fatalf("newFileWriter: %v", err)
	}

	if _, err := fw.Write([]byte("buffered line\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Close should flush buffered data even though the ticker hasn't fired.
	if err := fw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	suffix := time.Now().Format(time.DateOnly)
	data, err := os.ReadFile(filepath.Join(dir, "svc-"+suffix+".log"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), "buffered line") {
		t.Errorf("Close did not flush buffered data, got %q", string(data))
	}
}

func TestBuildLogger_FileWriterIntegration(t *testing.T) {
	dir := t.TempDir()
	log, err := New(Config{
		Level:            LevelInfo,
		Writers:          []Writer{WriterFile},
		FileDir:          dir,
		FilePrefix:       "myapp",
		FileBufferKB:     1,
		FileFlushSeconds: 3600,
		Format:           FormatJSON,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer log.Close()

	log.Info(context.Background(), "persisted", Attr{Key: "id", Value: 1})
	if err := log.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	suffix := time.Now().Format(time.DateOnly)
	data, err := os.ReadFile(filepath.Join(dir, "myapp-"+suffix+".log"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), `"msg":"persisted"`) {
		t.Errorf("log file missing entry, got %q", string(data))
	}
}
