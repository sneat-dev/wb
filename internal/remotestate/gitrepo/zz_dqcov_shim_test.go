//go:build !windows

package gitrepo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// dqCovInstallGitShim puts a `git` wrapper first on PATH that fails only for
// failSubcommand and delegates everything else to the real git. It is the
// fault-injection seam for git-command failures that cannot be produced by
// repository state alone. Unix-only: it relies on a /bin/sh script, so the
// windows build simply does not compile it.
func dqCovInstallGitShim(t *testing.T, failSubcommand string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("locate real git: %v", err)
	}
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = " + dqCovShellQuote(failSubcommand) + " ]; then\n" +
		"  echo 'dqCov git shim: " + failSubcommand + " failed' >&2\n" +
		"  exit 3\n" +
		"fi\n" +
		"exec " + dqCovShellQuote(realGit) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestDQCovPushReportsBranchLookupFailure proves push surfaces a failure to
// determine the current branch (the path taken when the branch has no upstream
// and must be pushed with -u) instead of pushing an empty branch name.
func TestDQCovPushReportsBranchLookupFailure(t *testing.T) {
	origin := dqCovEmptyBareOrigin(t)
	p := machine(t, origin)
	if err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}

	dqCovInstallGitShim(t, "branch")

	err := p.push()
	if err == nil {
		t.Fatal("push succeeded although the current branch could not be determined")
	}
	if !strings.Contains(err.Error(), "branch") {
		t.Fatalf("push error = %v, want the branch lookup failure", err)
	}
}
