package quality

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDqCovRepositoryRunOptionsAbsentPolicyKeepsBase pins that a repository
// without a quality policy inherits the caller's options unchanged.
func TestDqCovRepositoryRunOptionsAbsentPolicyKeepsBase(t *testing.T) {
	base := RunOptions{GoTestShards: 4, GoShardPackages: []string{"./cmd/wb"}, Retry: 2}
	options, err := RepositoryRunOptions(t.TempDir(), base)
	if err != nil {
		t.Fatal(err)
	}
	if options.GoTestShards != 4 || strings.Join(options.GoShardPackages, ",") != "./cmd/wb" || options.Retry != 2 {
		t.Fatalf("options = %+v, want the base options untouched", options)
	}
}

// TestDqCovRepositoryRunOptionsSurfacesOpenFailure covers a policy path that
// exists but cannot be opened, which must not silently fall back to defaults.
func TestDqCovRepositoryRunOptionsSurfacesOpenFailure(t *testing.T) {
	root := t.TempDir()
	writeQualityFile(t, filepath.Join(root, ".wb"), "not a directory")
	if _, err := RepositoryRunOptions(root, RunOptions{}); err == nil || !strings.Contains(err.Error(), "open repository quality policy") {
		t.Fatalf("error = %v, want the open failure surfaced", err)
	}
}

// TestDqCovRepositoryRunOptionsRejectsMalformedTrailingDocument covers a second
// YAML document that cannot be decoded at all.
func TestDqCovRepositoryRunOptionsRejectsMalformedTrailingDocument(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, repositoryQualityConfigPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("version: 1\ngo_lint:\n  commands:\n    - [go, vet, ./...]\n---\n[unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RepositoryRunOptions(root, RunOptions{}); err == nil || !strings.Contains(err.Error(), "decode trailing repository quality policy") {
		t.Fatalf("error = %v, want the trailing document rejected", err)
	}
}

// TestDqCovRepositoryRunOptionsRejectsBlankPackageEntry covers a go_test
// package list whose single entry is only whitespace.
func TestDqCovRepositoryRunOptionsRejectsBlankPackageEntry(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, repositoryQualityConfigPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("version: 1\ngo_test:\n  shards: 4\n  packages: [\"  \"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RepositoryRunOptions(root, RunOptions{}); err == nil || !strings.Contains(err.Error(), "empty go_test package") {
		t.Fatalf("error = %v, want the blank package rejected", err)
	}
}
