package remotestate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigReadsRemoteSectionAndDefaults(t *testing.T) {
	path := writeConfig(t, "recipes: {}\nremote:\n  repo: sneat-dev/wb-state\n  machine: vm-1\n")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "git" || cfg.Repo != "sneat-dev/wb-state" || cfg.Machine != "vm-1" || cfg.Publish.Unpushed != RedactNone {
		t.Fatalf("cfg = %+v", cfg)
	}
	if cfg.RepoOwner() != "sneat-dev" || cfg.RepoName() != "wb-state" {
		t.Fatalf("owner/name = %q/%q", cfg.RepoOwner(), cfg.RepoName())
	}
}

func TestLoadConfigMissingFileIsUnconfigured(t *testing.T) {
	_, err := LoadConfig(filepath.Join(t.TempDir(), "absent.yaml"))
	var unconfigured *UnconfiguredError
	if !errors.As(err, &unconfigured) {
		t.Fatalf("err = %v, want UnconfiguredError", err)
	}
	if !strings.Contains(err.Error(), "remote:\n  provider: git\n  repo: <owner>/<name>\n  machine: <unique-name-for-this-machine>") {
		t.Fatalf("snippet missing from %q", err.Error())
	}
}

func TestLoadConfigRequiresRepoAndMachine(t *testing.T) {
	_, err := LoadConfig(writeConfig(t, "remote:\n  repo: a/b\n"))
	var unconfigured *UnconfiguredError
	if !errors.As(err, &unconfigured) || strings.Join(unconfigured.Missing, ",") != "machine" {
		t.Fatalf("err = %v, want missing machine", err)
	}
}

func TestLoadConfigRejectsBadValues(t *testing.T) {
	for name, body := range map[string]string{
		"provider":                 "remote:\n  provider: ftp\n  repo: a/b\n  machine: m\n",
		"repo":                     "remote:\n  repo: just-a-name\n  machine: m\n",
		"machine":                  "remote:\n  repo: a/b\n  machine: 'has space'\n",
		"unpushed":                 "remote:\n  repo: a/b\n  machine: m\n  publish:\n    unpushed: maybe\n",
		"repo owner traversal":     "remote:\n  repo: ../x\n  machine: m\n",
		"repo name traversal":      "remote:\n  repo: a/..\n  machine: m\n",
		"repo too many separators": "remote:\n  repo: a/b/c\n  machine: m\n",
	} {
		if _, err := LoadConfig(writeConfig(t, body)); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestLoadConfigFileWithoutRemoteSectionIsUnconfigured(t *testing.T) {
	path := writeConfig(t, "recipes: {}\n")
	_, err := LoadConfig(path)
	var unconfigured *UnconfiguredError
	if !errors.As(err, &unconfigured) {
		t.Fatalf("err = %v, want UnconfiguredError", err)
	}
	if len(unconfigured.Missing) != 1 || unconfigured.Missing[0] != "remote section" {
		t.Fatalf("Missing = %v, want [\"remote section\"]", unconfigured.Missing)
	}
	if !strings.Contains(err.Error(), ConfigSnippet) {
		t.Fatalf("ConfigSnippet missing from %q", err.Error())
	}
}

func TestLoadConfigMalformedYAMLIsPlainError(t *testing.T) {
	path := writeConfig(t, "remote: [unclosed\n")
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for malformed YAML")
	}
	var unconfigured *UnconfiguredError
	if errors.As(err, &unconfigured) {
		t.Fatalf("err should not be UnconfiguredError, got %v", err)
	}
	if !strings.Contains(err.Error(), "parse config") {
		t.Fatalf("\"parse config\" missing from %q", err.Error())
	}
}
