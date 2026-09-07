package wbconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSetRemoteHubPreservesUnrelatedConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "wb.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := "parallel: 3\nremote:\n  provider: git\n  repo: acme/state\n  publish:\n    unpushed: counts\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(filepath.Dir(path), "credentials", "hub.token")
	if err := SetRemoteHub(path, "https://hub.example", "studio-mac", tokenFile); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"parallel: 3", "repo: acme/state", "unpushed: counts", "provider: hub", "url: https://hub.example", "machine: studio-mac", "token_file: " + tokenFile} {
		if !strings.Contains(text, want) {
			t.Errorf("updated config lacks %q:\n%s", want, text)
		}
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("config mode = %o, want 600", info.Mode().Perm())
		}
	}
}

func TestSetRemoteHubCreatesConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "wb.yaml")
	if err := SetRemoteHub(path, "https://hub.example", "vm", filepath.Join(t.TempDir(), "token")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
