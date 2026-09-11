package remotestate

import (
	"errors"
	"fmt"
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

func TestLoadConfigReadsHTTPSHubAndUsesPrivacySafeDefault(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	path := writeConfig(t, fmt.Sprintf("remote:\n  provider: hub\n  url: https://wb-github-app.sneat.dev\n  token_file: '%s'\n  machine: vm-1\n", tokenFile))
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "hub" || cfg.URL != "https://wb-github-app.sneat.dev" || cfg.TokenFile != tokenFile || cfg.Publish.Unpushed != RedactUnpushed {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoadConfigIncompleteHubShowsHubSnippet(t *testing.T) {
	_, err := LoadConfig(writeConfig(t, "remote:\n  provider: hub\n  machine: vm-1\n"))
	var unconfigured *UnconfiguredError
	if !errors.As(err, &unconfigured) || !strings.Contains(err.Error(), "https://wb-github-app.sneat.dev") ||
		!strings.Contains(err.Error(), "token_file") {
		t.Fatalf("err = %v, want hub configuration guidance", err)
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
		"hub insecure URL":         "remote:\n  provider: hub\n  url: http://hub.example\n  token_file: /private/token\n  machine: m\n",
		"hub URL credential":       "remote:\n  provider: hub\n  url: https://user:secret@hub.example\n  token_file: /private/token\n  machine: m\n",
		"hub relative token":       "remote:\n  provider: hub\n  url: https://hub.example\n  token_file: token\n  machine: m\n",
	} {
		if _, err := LoadConfig(writeConfig(t, body)); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

// TestLoadConfigMachineErrorIsHumanReadable proves an invalid remote.machine
// produces a message describing the rule in words, not the raw regexp
// pattern, so a user hitting it does not need to read Go regexp syntax to
// understand what to fix.
func TestLoadConfigMachineErrorIsHumanReadable(t *testing.T) {
	_, err := LoadConfig(writeConfig(t, "remote:\n  repo: a/b\n  machine: 'has space'\n"))
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "letters, digits") {
		t.Fatalf("err = %q, want it to contain %q", err.Error(), "letters, digits")
	}
	if strings.Contains(err.Error(), "[A-Za-z0-9]") {
		t.Fatalf("err = %q, should not leak the raw regexp", err.Error())
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

// TestValidateHubURLAcceptsLoopbackHTTP is what lets `wb daemon serve` point
// the in-process provider at the hub it is itself hosting. Everything off the
// loopback still has to be HTTPS, because the machine credential travels in
// an Authorization header.
func TestValidateHubURLAcceptsLoopbackHTTP(t *testing.T) {
	for _, accepted := range []string{
		"https://wb-github-app.sneat.dev",
		"https://wb-github-app.sneat.dev/",
		"http://127.0.0.1:8766",
		"http://[::1]:8766",
		"http://localhost:8766",
		"http://LOCALHOST:8766",
		"http://127.0.0.1:8766/",
	} {
		if err := ValidateHubURL(accepted); err != nil {
			t.Fatalf("ValidateHubURL(%q) = %v, want it accepted", accepted, err)
		}
	}
	for _, rejected := range []string{
		"",
		"http://example.com",
		"http://10.0.0.1:8766",
		"http://bench.internal:8766",
		"ftp://127.0.0.1",
		"https://user:pass@wb-github-app.sneat.dev",
		"https://wb-github-app.sneat.dev?x=1",
		"https://wb-github-app.sneat.dev#f",
		"https://wb-github-app.sneat.dev/path",
		"https://%zz",
	} {
		if err := ValidateHubURL(rejected); !errors.Is(err, ErrHubURL) {
			t.Fatalf("ValidateHubURL(%q) = %v, want ErrHubURL", rejected, err)
		}
	}
}

// TestLoadConfigAcceptsALoopbackHubURL proves the relaxation reaches the
// configuration loader, which is where a self-hosted remote: section lands.
func TestLoadConfigAcceptsALoopbackHubURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wb.yaml")
	body := "remote:\n  provider: hub\n  url: http://127.0.0.1:8766\n  machine: laptop\n  token_file: " + filepath.Join(t.TempDir(), "hub.token") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil || cfg.URL != "http://127.0.0.1:8766" {
		t.Fatalf("LoadConfig = %+v, %v", cfg, err)
	}
}

// TestLoadConfigRejectsANonLoopbackHTTPHubURL keeps the relaxation narrow.
func TestLoadConfigRejectsANonLoopbackHTTPHubURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wb.yaml")
	body := "remote:\n  provider: hub\n  url: http://bench.example\n  machine: laptop\n  token_file: /tmp/hub.token\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); !errors.Is(err, ErrHubURL) {
		t.Fatalf("LoadConfig = %v, want ErrHubURL", err)
	}
}
