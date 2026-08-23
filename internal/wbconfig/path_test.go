package wbconfig

import (
	"path/filepath"
	"testing"
)

func TestDefaultPathLivesUnderUserConfigDir(t *testing.T) {
	t.Setenv("HOME", "/tmp/wbconfig-home")
	got := DefaultPath()
	want := filepath.Join("/tmp/wbconfig-home", ".config", "wb", "wb.yaml")
	if got != want {
		t.Fatalf("DefaultPath() = %q, want %q", got, want)
	}
}
