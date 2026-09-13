package wbconfig

import (
	"path/filepath"
	"testing"
)

func TestDefaultPathLivesUnderUserConfigDir(t *testing.T) {
	t.Setenv("HOME", "/tmp/wbconfig-home")
	t.Setenv("XDG_CONFIG_HOME", "")
	got := DefaultPath()
	want := filepath.Join("/tmp/wbconfig-home", ".config", "wb", "wb.yaml")
	if got != want {
		t.Fatalf("DefaultPath() = %q, want %q", got, want)
	}
}

func TestDefaultPathHonorsXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/wbconfig-xdg")
	want := filepath.Join("/tmp/wbconfig-xdg", "wb", "wb.yaml")
	if got := DefaultPath(); got != want {
		t.Fatalf("DefaultPath() = %q, want %q", got, want)
	}
}
