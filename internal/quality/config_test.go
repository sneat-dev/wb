package quality

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryRunOptionsLoadsExplicitShardingPolicy(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, repositoryQualityConfigPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("version: 1\ngo_lint:\n  commands:\n    - [go, vet, ./...]\n    - [go, run, example.test/lint@v1.2.3, run, ./...]\ngo_test:\n  shards: 8\n  packages: [./internal/worktrees, ./cmd/wb]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	options, err := RepositoryRunOptions(root, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if options.GoTestShards != 8 || strings.Join(options.GoShardPackages, ",") != "./internal/worktrees,./cmd/wb" {
		t.Fatalf("repository options = %+v", options)
	}
	if got := len(options.GoLintCommands); got != 2 || strings.Join(options.GoLintCommands[1], " ") != "go run example.test/lint@v1.2.3 run ./..." {
		t.Fatalf("go lint commands = %#v", options.GoLintCommands)
	}
}

func TestRepositoryRunOptionsRetainsExplicitShardingAfterValidatingPolicy(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, repositoryQualityConfigPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("version: 1\ngo_lint:\n  commands:\n    - [go, vet, ./...]\ngo_test:\n  shards: 8\n  packages: [./internal/worktrees, ./internal/orchestrate]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	options, err := RepositoryRunOptions(root, RunOptions{
		GoTestShards:           4,
		GoShardPackages:        []string{"./internal/worktrees"},
		ExplicitGoTestSharding: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.GoTestShards != 4 || strings.Join(options.GoShardPackages, ",") != "./internal/worktrees" {
		t.Fatalf("explicit sharding was broadened: %+v", options)
	}
	if len(options.GoLintCommands) != 1 || strings.Join(options.GoLintCommands[0], " ") != "go vet ./..." {
		t.Fatalf("policy lint command was lost: %#v", options.GoLintCommands)
	}
}

func TestRepositoryRunOptionsExplicitShardingStillFailsClosedOnMalformedPolicy(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, repositoryQualityConfigPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("version: 1\ngo_test:\n  shards: 1\n  packages: [./internal/orchestrate]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RepositoryRunOptions(root, RunOptions{
		GoTestShards:           4,
		GoShardPackages:        []string{"./internal/worktrees"},
		ExplicitGoTestSharding: true,
	}); err == nil {
		t.Fatal("explicit sharding bypassed malformed repository policy")
	}
}

func TestRepositoryRunOptionsLoadsGoLintWithoutGoTest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, repositoryQualityConfigPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("version: 1\ngo_lint:\n  commands:\n    - [go, vet, ./...]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := RunOptions{GoTestShards: 3, GoShardPackages: []string{"./existing"}}
	options, err := RepositoryRunOptions(root, base)
	if err != nil {
		t.Fatal(err)
	}
	if options.GoTestShards != 3 || strings.Join(options.GoShardPackages, ",") != "./existing" {
		t.Fatalf("standalone go_lint changed go_test options: %+v", options)
	}
	if got := len(options.GoLintCommands); got != 1 || strings.Join(options.GoLintCommands[0], " ") != "go vet ./..." {
		t.Fatalf("go lint commands = %#v", options.GoLintCommands)
	}
}

func TestRepositoryRunOptionsFailsClosed(t *testing.T) {
	t.Parallel()
	for _, contents := range []string{
		"version: 2\ngo_test:\n  shards: 8\n  packages: [./cmd/wb]\n",
		"version: 1\ngo_test:\n  shards: 1\n  packages: [./cmd/wb]\n",
		"version: 1\ngo_test:\n  shards: 8\n  packages: []\n",
		"version: 1\ngo_test:\n  shards: 8\n  packages: [./cmd/wb, ./cmd/wb]\n",
		"version: 1\nunknown: true\ngo_test:\n  shards: 8\n  packages: [./cmd/wb]\n",
		"version: 1\ngo_test:\n  shards: 8\n  packages: [./cmd/wb]\n---\nversion: 1\n",
		"version: 1\ngo_lint:\n  commands: [[]]\n",
		"version: 1\ngo_lint:\n  commands: [[go, \"\"]]\n",
	} {
		root := t.TempDir()
		path := filepath.Join(root, repositoryQualityConfigPath)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := RepositoryRunOptions(root, RunOptions{}); err == nil {
			t.Fatalf("unsafe policy was accepted:\n%s", contents)
		}
	}
}

func TestRepositoryRunOptionsLoadsIntegrationChecks(t *testing.T) {
	t.Parallel()
	write := func(t *testing.T, body string) string {
		t.Helper()
		root := t.TempDir()
		path := filepath.Join(root, repositoryQualityConfigPath)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return root
	}
	options, err := RepositoryRunOptions(write(t, "version: 1\nintegration_checks: [lint, typecheck]\n"), RunOptions{})
	if err != nil || len(options.IntegrationChecks) != 2 || options.IntegrationChecks[1] != CheckTypecheck {
		t.Fatalf("options = %+v err = %v", options, err)
	}
	for name, body := range map[string]string{
		"unknown check": "version: 1\nintegration_checks: [lint, fuzz]\n",
		"empty list":    "version: 1\nintegration_checks: []\n",
		"empty entry":   "version: 1\nintegration_checks: [lint, \"\"]\n",
		"blank only":    "version: 1\nintegration_checks: [\" \"]\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := RepositoryRunOptions(write(t, body), RunOptions{}); err == nil || !strings.Contains(err.Error(), "integration_checks") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestTypecheckRunsOnlyAProjectsOwnScript(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for name, body := range map[string]string{"go.mod": "module example.test/x\n\ngo 1.22\n", "package.json": `{"scripts":{}}`, "nx.json": "{}"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Go has no typecheck command, and an Nx workspace without the script
	// is not asked to run the target.
	report := VerifyWithOptions(t.Context(), "example/x", root, []Check{CheckTypecheck}, RunOptions{})
	if len(report.Results) != 1 || report.Results[0].Language != "node" || report.Results[0].Status != StatusSkipped {
		t.Fatalf("report = %+v", report)
	}
	if checks, err := ParseChecks("typecheck,lint"); err != nil || checks[0] != CheckTypecheck {
		t.Fatalf("checks = %v err = %v", checks, err)
	}
}
