package quality

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryRunOptionsLoadsExplicitShardingPolicy(t *testing.T) {
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

func TestRepositoryRunOptionsFailsClosed(t *testing.T) {
	for _, contents := range []string{
		"version: 2\ngo_test:\n  shards: 8\n  packages: [./cmd/wb]\n",
		"version: 1\ngo_test:\n  shards: 1\n  packages: [./cmd/wb]\n",
		"version: 1\ngo_test:\n  shards: 8\n  packages: []\n",
		"version: 1\ngo_test:\n  shards: 8\n  packages: [./cmd/wb, ./cmd/wb]\n",
		"version: 1\nunknown: true\ngo_test:\n  shards: 8\n  packages: [./cmd/wb]\n",
		"version: 1\ngo_test:\n  shards: 8\n  packages: [./cmd/wb]\n---\nversion: 1\n",
		"version: 1\ngo_lint:\n  commands: [[]]\ngo_test:\n  shards: 8\n  packages: [./cmd/wb]\n",
		"version: 1\ngo_lint:\n  commands: [[go, \"\"]]\ngo_test:\n  shards: 8\n  packages: [./cmd/wb]\n",
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
