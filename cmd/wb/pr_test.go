package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// An operator holds a pull request in whichever form their source gave them:
// what they typed, what a report printed, or what they copied from a browser.
// Every one of them addresses the same pull request, and making the caller
// normalize it is how a URL ends up inside an API path.
func TestPRLandReportsLocalLinkPreflightBeforeGitHub(t *testing.T) {
	// Hide gh without hiding git: the preflight itself shells out to git
	// (branch-name validation) before the landing ever calls gh, so a PATH
	// wiped down to an empty directory defeats the preflight too and this
	// test would never reach GitHub in the first place. A PATH restricted to
	// git's own directory (e.g. /usr/bin) is not safe either: on this VM and
	// on GitHub's own runners that directory also holds a real, authenticated
	// gh, so the "missing gh" refusal this test wants would instead become a
	// live call to the real GitHub API. Symlink only the git executable into
	// an otherwise-empty directory and point PATH there, so gh is genuinely
	// absent and git is genuinely present.
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not found on PATH: %v", err)
	}
	binDir := t.TempDir()
	if err := os.Symlink(gitPath, filepath.Join(binDir, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	if err := os.MkdirAll(projectsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(wbhome.EnvOverride, projectsRoot)

	command := prCommandForTest(&invocation{projectsRoot: projectsRoot}, "land")
	var stderr bytes.Buffer
	command.SetErr(&stderr)
	command.SetArgs([]string{"acme/app#7", "--non-interactive"})
	if err := command.Execute(); err == nil {
		t.Fatal("missing gh unexpectedly let the landing continue")
	}
	output := stderr.String()
	if strings.Contains(output, "GitHub repos/") {
		t.Fatalf("test reached the real GitHub API (PATH leaked a real gh):\n%s", output)
	}
	if !strings.Contains(output, "pr land: local link preflight: acme/app: started") ||
		!strings.Contains(output, "pr land: local link preflight: acme/app: completed") {
		t.Fatalf("local-link preflight did not complete before GitHub:\n%s", output)
	}
}
