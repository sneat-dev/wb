//go:build e2e

package worktreeretire

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

//nolint:paralleltest // PATH is process-wide; this test verifies the real Git executable failure path.
func TestE2EArchiveGitObjectRejectsMissingGit(t *testing.T) {
	root := t.TempDir()
	if _, err := GitObjectSHA(context.Background(), root, "missing"); err == nil {
		t.Fatal("missing repository object accepted")
	}
	t.Setenv("PATH", "")
	if _, err := GitObjectSHA(context.Background(), root, "missing"); err == nil {
		t.Fatal("missing Git executable accepted")
	}
}

func TestE2EArchiveGitObjectHashesExactBytes(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	if output, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	source := filepath.Join(repo, "source")
	if err := os.WriteFile(source, []byte("private\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("git", "-C", repo, "hash-object", "-w", source).Output()
	if err != nil {
		t.Fatal(err)
	}
	got, err := GitObjectSHA(context.Background(), repo, strings.TrimSpace(string(output)))
	if err != nil || got != digest([]byte("private\n")) {
		t.Fatalf("object digest = (%q, %v)", got, err)
	}
}
