package agents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreflightProvesTheHarnessCanStart(t *testing.T) {
	t.Parallel()
	base := PreflightDeps{
		LookPath:          func(string) (string, error) { return "/bin/harness", nil },
		ResolveCredential: func(Provider) (Credential, error) { return Credential{EnvName: "X", Value: "v"}, nil },
		Run:               func(context.Context, string, ...string) ([]byte, error) { return []byte("v1.2.3"), nil },
		TempDir:           func() (string, error) { return t.TempDir(), nil },
	}
	if err := Preflight(context.Background(), Resolved{Harness: HarnessCodex, Provider: "deepseek", Routing: Provider{CredentialEnv: "X"}}, base); err != nil {
		t.Fatalf("codex preflight: %v", err)
	}
	if err := Preflight(context.Background(), Resolved{Harness: HarnessOpenCode, Model: "p/m"}, base); err != nil {
		t.Fatalf("opencode preflight: %v", err)
	}

	missing := base
	missing.LookPath = func(string) (string, error) { return "", errors.New("not found") }
	if err := Preflight(context.Background(), Resolved{Harness: HarnessOpenCode}, missing); err == nil || !strings.Contains(err.Error(), "not on PATH") {
		t.Fatalf("missing harness = %v", err)
	}

	noCredential := base
	noCredential.ResolveCredential = func(Provider) (Credential, error) { return Credential{}, errors.New("DEEPSEEK_API_KEY is not set") }
	if err := Preflight(context.Background(), Resolved{Harness: HarnessCodex, Provider: "deepseek"}, noCredential); err == nil || !strings.Contains(err.Error(), "DEEPSEEK_API_KEY") {
		t.Fatalf("missing credential = %v", err)
	}
	if err := Preflight(context.Background(), Resolved{Harness: HarnessCodex}, PreflightDeps{LookPath: base.LookPath, Run: base.Run}); err == nil {
		t.Fatal("a codex preflight without a credential resolver must fail")
	}
	if err := Preflight(context.Background(), Resolved{Harness: HarnessOpenCode}, PreflightDeps{}); err == nil {
		t.Fatal("a preflight without a runner must fail")
	}
}

func TestPreflightProvesTheDshModelThroughItsComposedConfiguration(t *testing.T) {
	t.Parallel()
	accepted := PreflightDeps{
		LookPath: func(string) (string, error) { return "/bin/dsh", nil },
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if !containsAll(args, "--profile", dshProfile, "--patch", "--dump-config") {
				return nil, errors.New("unexpected probe arguments")
			}
			return []byte("- id: agent-default-model\n  config:\n    provider: deepseek-official\n    model: deepseek-v4-flash\n"), nil
		},
		TempDir: func() (string, error) { return t.TempDir(), nil },
	}
	if err := Preflight(context.Background(), Resolved{Harness: HarnessDSH, Model: "deepseek-v4-flash"}, accepted); err != nil {
		t.Fatalf("dsh preflight: %v", err)
	}

	mismatched := accepted
	mismatched.Run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("- id: agent-default-model\n  config:\n    model: another-model\n"), nil
	}
	if err := Preflight(context.Background(), Resolved{Harness: HarnessDSH, Model: "deepseek-v4-flash"}, mismatched); err == nil || !strings.Contains(err.Error(), "different model") {
		t.Fatalf("mismatched model = %v", err)
	}

	failing := accepted
	failing.Run = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("boom") }
	if err := Preflight(context.Background(), Resolved{Harness: HarnessDSH, Model: "m"}, failing); err == nil {
		t.Fatal("a failing dump must refuse the run")
	}

	noTemp := accepted
	noTemp.TempDir = func() (string, error) { return "", errors.New("no temp") }
	if err := Preflight(context.Background(), Resolved{Harness: HarnessDSH, Model: "m"}, noTemp); err == nil {
		t.Fatal("a temp failure must refuse the run")
	}
}

func TestDshModelPatchIsValidYAMLForTheComposeTree(t *testing.T) {
	t.Parallel()
	raw, err := dshModelPatch("deepseek-v4-flash")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "- id: agent-default-model") || !strings.Contains(text, "model: deepseek-v4-flash") {
		t.Fatalf("patch = %q", text)
	}
	if strings.Contains(text, ": null") {
		t.Fatalf("the patch must not emit empty keys: %q", text)
	}
	if _, err := dshModelPatch(""); err != nil {
		t.Fatalf("an empty model is caught earlier, but the patch itself must not fail: %v", err)
	}
}

func TestDetectVersionReadsTheFirstLineBestEffort(t *testing.T) {
	t.Parallel()
	lookPath := func(string) (string, error) { return "/bin/harness", nil }
	version := DetectVersion(context.Background(), HarnessCodex, PreflightDeps{
		LookPath: lookPath,
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("codex-cli 1.2.3\nsecond line\n"), nil
		},
	})
	if version != "codex-cli 1.2.3" {
		t.Fatalf("version = %q", version)
	}
	if got := DetectVersion(context.Background(), HarnessCodex, PreflightDeps{
		LookPath: lookPath,
		Run:      func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("boom") },
	}); got != "" {
		t.Fatalf("a failed probe must be empty, got %q", got)
	}
	if got := DetectVersion(context.Background(), HarnessCodex, PreflightDeps{
		LookPath: func(string) (string, error) { return "", errors.New("missing") },
		Run:      func(context.Context, string, ...string) ([]byte, error) { return []byte("x"), nil },
	}); got != "" {
		t.Fatalf("a missing binary must be empty, got %q", got)
	}
	long := DetectVersion(context.Background(), HarnessCodex, PreflightDeps{
		LookPath: lookPath,
		Run:      func(context.Context, string, ...string) ([]byte, error) { return []byte(strings.Repeat("v", 200)), nil },
	})
	if len(long) != 120 {
		t.Fatalf("version must be bounded, got %d bytes", len(long))
	}
	if got := DetectVersion(context.Background(), HarnessCodex, PreflightDeps{}); got != "" {
		t.Fatalf("a probe without a runner must be empty, got %q", got)
	}
}

func containsAll(values []string, wanted ...string) bool {
	joined := strings.Join(values, "\x00")
	for _, want := range wanted {
		if !strings.Contains(joined, want) {
			return false
		}
	}
	return true
}

func TestShippedPreflightDepsResolveTheHarnessAndItsCredential(t *testing.T) {
	t.Parallel()
	deps := DefaultPreflightDeps()
	if deps.LookPath == nil || deps.ResolveCredential == nil || deps.Run == nil || deps.TempDir == nil {
		t.Fatalf("default preflight deps = %#v", deps)
	}
	if _, err := deps.LookPath("wb-agent-definitely-not-installed"); err == nil {
		t.Fatal("a missing binary must not resolve")
	}
	if _, err := deps.TempDir(); err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	if _, err := deps.Run(context.Background(), "/bin/sh", "-c", "echo probed"); err != nil {
		t.Fatalf("probe run: %v", err)
	}
}

func TestDshPreflightWritesTheOverlayIntoItsOwnTemporaryDirectory(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	files := 0
	deps := PreflightDeps{
		LookPath: func(string) (string, error) { return "/bin/dsh", nil },
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			files++
			return []byte("id: agent-default-model\nmodel: m\n"), nil
		},
		TempDir: func() (string, error) {
			dir := filepath.Join(tempDir, "probe")
			return dir, os.MkdirAll(dir, 0o700)
		},
	}
	if err := Preflight(context.Background(), Resolved{Harness: HarnessDSH, Model: "m"}, deps); err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if files != 1 {
		t.Fatalf("probes = %d", files)
	}
	if _, err := os.Stat(filepath.Join(tempDir, "probe")); !os.IsNotExist(err) {
		t.Fatal("the probe directory must be removed")
	}
}
