package logger

import (
	"log/slog"
	"testing"
)

func TestAttr_Fields(t *testing.T) {
	a := Attr{Key: "user_id", Value: 123}
	if a.Key != "user_id" {
		t.Errorf("Key = %q, want user_id", a.Key)
	}
	if a.Value != 123 {
		t.Errorf("Value = %v, want 123", a.Value)
	}
}

func TestAttr_SlogAttr(t *testing.T) {
	sa := Attr{Key: "op", Value: "save"}.slogAttr()
	if sa.Key != "op" {
		t.Errorf("slog Key = %q, want op", sa.Key)
	}
	if sa.Value.Any() != "save" {
		t.Errorf("slog Value = %v, want save", sa.Value.Any())
	}
	// slog.Any specializes the stored kind by the dynamic type.
	if sa.Value.Kind() != slog.KindString {
		t.Errorf("slog kind = %v, want %v", sa.Value.Kind(), slog.KindString)
	}
}
