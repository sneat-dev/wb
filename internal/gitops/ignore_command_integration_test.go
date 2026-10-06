package gitops_test

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/cmdrepo"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/gitops"
)

// This integration preserves the genuine command-to-local-Git effect assertions
// without importing root or building a WB executable for command output checks.
func TestRepoIgnoreCommandTogglesTheMarker(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	child := exec.Command("git", "-C", repo, "init", "-q", "-b", "main")
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("init: %v: %s", err, output)
	}
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		cmd := cmdrepo.New(shared.Runtime{}, cmdrepo.Dependencies{SetSkipSync: gitops.SetSkipSync, UnsetSkipSync: gitops.UnsetSkipSync})
		cmd.SetOut(&out)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(append([]string{"ignore"}, args...))
		err := cmd.Execute()
		return out.String(), err
	}
	marker := func() (string, bool) {
		command := exec.Command("git", "-C", repo, "config", "--local", "--get", "wb.skip-sync")
		command.Env = os.Environ()
		raw, err := command.CombinedOutput()
		return strings.TrimSpace(string(raw)), err == nil
	}
	stdout, err := run(repo)
	if err != nil {
		t.Fatalf("repo ignore: %v", err)
	}
	if !strings.Contains(stdout, "ignored by wb sync") {
		t.Errorf("repo ignore output on stdout = %q", stdout)
	}
	value, ok := marker()
	if !ok || value != "true" {
		t.Fatalf("marker = %q (present=%v), want true", value, ok)
	}
	stdout, err = run(repo, "--unset")
	if err != nil {
		t.Fatalf("repo ignore --unset: %v", err)
	}
	if !strings.Contains(stdout, "wb sync re-enabled") {
		t.Errorf("repo ignore --unset output = %q", stdout)
	}
	if _, ok := marker(); ok {
		t.Fatal("marker survived --unset")
	}
	if _, err := run(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("repo ignore accepted a directory that is not a repository")
	}
}
