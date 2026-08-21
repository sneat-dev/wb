package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRepoConfig(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ConfigFileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestLoadRepoConfig(t *testing.T) {
	root := writeRepoConfig(t, "policy: acme/cicd//policy/backend.yaml\ntype: extension-implementation\nstrict: true\n")
	config, err := LoadRepoConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if config.Policy != "acme/cicd//policy/backend.yaml" {
		t.Fatalf("policy = %q", config.Policy)
	}
	if config.Type != "extension-implementation" {
		t.Fatalf("type = %q", config.Type)
	}
	if !config.Strict {
		t.Fatal("strict should be true")
	}
	if !config.Found {
		t.Fatal("Found should be true")
	}
}

func TestLoadRepoConfigAbsentIsNotAnError(t *testing.T) {
	config, err := LoadRepoConfig(t.TempDir())
	if err != nil {
		t.Fatalf("a missing config file is not an error: %v", err)
	}
	if config.Found {
		t.Fatal("Found should be false")
	}
}

// A repository may tighten its own rules and may never loosen them. These are
// the shapes someone reaches for when they want an exception, and each has to
// fail loudly rather than quietly take effect.
func TestRepoConfigRefusesToLoosen(t *testing.T) {
	cases := map[string]string{
		"declaring groups": "policy: p.yaml\ngroups:\n  - {name: mine, match: [\"...\"]}\n",
		"declaring types":  "policy: p.yaml\ntypes:\n  - name: t\n    detect: [\"x\"]\n",
		"extending allow":  "policy: p.yaml\nallow: [dalgo-adapter]\n",
		"setting a mode":   "policy: p.yaml\nmode: report\n",
		"demoting layers":  "policy: p.yaml\nlayers:\n  mode: report\n",
		"unknown key":      "policy: p.yaml\nwhatever: 1\n",
		"strict false":     "policy: p.yaml\nstrict: false\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadRepoConfig(writeRepoConfig(t, body))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), "tighten") {
				t.Fatalf("error should explain the tighten-never-loosen rule, got: %v", err)
			}
		})
	}
}

func TestRepoConfigRefusesAPinnedPolicyVersion(t *testing.T) {
	_, err := LoadRepoConfig(writeRepoConfig(t, "policy: acme/cicd//policy/backend.yaml@v1.2.0\n"))
	if err == nil {
		t.Fatal("pinning a policy release is an exception with extra steps and must be refused")
	}
	if !strings.Contains(err.Error(), "release") {
		t.Fatalf("error should name the reason, got: %v", err)
	}
}

func TestParseSource(t *testing.T) {
	cases := []struct {
		raw  string
		kind SourceKind
	}{
		{"./policy/backend.yaml", SourcePath},
		{"/etc/policy.yaml", SourcePath},
		{"policy.yaml", SourcePath},
		{"acme/cicd//policy/backend.yaml", SourceFleet},
		{"https://example.com/policy.yaml", SourceURL},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			source, err := ParseSource(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			if source.Kind != tc.kind {
				t.Fatalf("kind = %q, want %q", source.Kind, tc.kind)
			}
		})
	}
	for _, bad := range []string{"", "http://example.com/p.yaml", "acme/cicd//", "//x"} {
		if _, err := ParseSource(bad); err == nil {
			t.Fatalf("ParseSource(%q) should fail", bad)
		}
	}
}

func TestSourceLocateFindsPolicyInAFleetCheckout(t *testing.T) {
	fleetRoot := t.TempDir()
	target := filepath.Join(fleetRoot, "acme", "cicd", "policy", "backend.yaml")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(samplePolicy), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := ParseSource("acme/cicd//policy/backend.yaml")
	if err != nil {
		t.Fatal(err)
	}
	located, err := source.Locate(t.TempDir(), []string{fleetRoot})
	if err != nil {
		t.Fatal(err)
	}
	if located != target {
		t.Fatalf("located = %q, want %q", located, target)
	}
}

func TestSourceLocateExplainsAMissingFleetCheckout(t *testing.T) {
	source, err := ParseSource("acme/cicd//policy/backend.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Locate(t.TempDir(), []string{t.TempDir()})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "--policy") {
		t.Fatalf("error should point at the escape hatch, got: %v", err)
	}
}

func TestSourceLocateResolvesRelativePathAgainstRepoRoot(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "policy.yaml")
	if err := os.WriteFile(target, []byte(samplePolicy), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := ParseSource("policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	located, err := source.Locate(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if located != target {
		t.Fatalf("located = %q, want %q", located, target)
	}
}
