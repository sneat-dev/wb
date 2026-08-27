package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/checkoutmarker"
)

type checkoutFixture struct {
	ProjectsRoot string
	Canonical    string
	Worktree     string
	Origin       string
}

func newCheckoutFixture(t *testing.T) checkoutFixture {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	runCommand(t, root, "git", "init", "-q", "--bare", origin)
	projectsRoot := filepath.Join(root, "projects")
	canonical := filepath.Join(projectsRoot, "sneat-co", "backstage")
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	runCommand(t, root, "git", "clone", "-q", origin, canonical)
	runCommand(t, canonical, "git", "config", "user.email", "wb@example.test")
	runCommand(t, canonical, "git", "config", "user.name", "wb")
	if err := os.WriteFile(filepath.Join(canonical, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCommand(t, canonical, "git", "add", "-A")
	runCommand(t, canonical, "git", "commit", "-qm", "init")
	runCommand(t, canonical, "git", "branch", "-M", "main")
	runCommand(t, canonical, "git", "push", "-q", "origin", "main")
	worktree := filepath.Join(root, "worktrees", "task", "sneat-co", "backstage")
	runCommand(t, canonical, "git", "worktree", "add", "-q", "-b", "task", worktree)
	return checkoutFixture{ProjectsRoot: projectsRoot, Canonical: canonical, Worktree: worktree, Origin: origin}
}

func runCommand(t *testing.T, directory, name string, arguments ...string) string {
	t.Helper()
	command := exec.Command(name, arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s in %s: %v\n%s", name, strings.Join(arguments, " "), directory, err, output)
	}
	return strings.TrimSpace(string(output))
}

// runCheckoutCommand drives the CLI the way a settings file or an operator
// would, capturing both streams and the documented exit code.
func runCheckoutCommand(t *testing.T, arguments ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(arguments, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// TestWorktreeMarkerMarksBothKindsAndKeepsStatusClean is the whole point of the
// marker, checked through the command an operator actually runs.
func TestWorktreeMarkerMarksBothKindsAndKeepsStatusClean(t *testing.T) {
	checkouts := newCheckoutFixture(t)
	for _, path := range []string{checkouts.Canonical, checkouts.Worktree} {
		code, stdout, stderr := runCheckoutCommand(t, "--projects-root", checkouts.ProjectsRoot, "worktree", "marker", path)
		if code != exitOK {
			t.Fatalf("marker %s exited %d: %s%s", path, code, stdout, stderr)
		}
		marker, err := os.ReadFile(filepath.Join(path, checkoutmarker.FileName))
		if err != nil {
			t.Fatalf("no marker in %s: %v", path, err)
		}
		if status := runCommand(t, path, "git", "status", "--porcelain=v1"); status != "" {
			t.Fatalf("%s went dirty:\n%s", path, status)
		}
		wantKind := "kind: worktree"
		if path == checkouts.Canonical {
			wantKind = "kind: canonical"
		}
		if !strings.Contains(string(marker), wantKind) {
			t.Fatalf("%s marker does not say %q:\n%s", path, wantKind, marker)
		}
	}
}

// TestWorktreeMarkerIsIdempotent keeps the refresh on every sync and every
// create free of churn.
func TestWorktreeMarkerIsIdempotent(t *testing.T) {
	checkouts := newCheckoutFixture(t)
	runCheckoutCommand(t, "--projects-root", checkouts.ProjectsRoot, "worktree", "marker", checkouts.Canonical)
	code, stdout, _ := runCheckoutCommand(t, "--projects-root", checkouts.ProjectsRoot, "worktree", "marker", checkouts.Canonical)
	if code != exitOK || !strings.Contains(stdout, "current") {
		t.Fatalf("a repeated marker run reported %q (exit %d)", stdout, code)
	}
	code, dryRun, _ := runCheckoutCommand(t, "--projects-root", checkouts.ProjectsRoot, "worktree", "marker", checkouts.Canonical, "--dry-run")
	if code != exitOK || !strings.Contains(dryRun, "current") {
		t.Fatalf("a dry run after a write reported %q", dryRun)
	}
}

// TestWorktreeMarkerFleetCoversClonesAndTheirWorktrees checks the sweep the
// brief asks to be idempotent and safe to re-run.
func TestWorktreeMarkerFleetCoversClonesAndTheirWorktrees(t *testing.T) {
	checkouts := newCheckoutFixture(t)
	code, stdout, stderr := runCheckoutCommand(t, "--projects-root", checkouts.ProjectsRoot, "worktree", "marker", "--fleet", "--format", "json")
	if code != exitOK {
		t.Fatalf("fleet marker exited %d: %s%s", code, stdout, stderr)
	}
	var outcomes []markerOutcome
	if err := json.Unmarshal([]byte(stdout), &outcomes); err != nil {
		t.Fatalf("fleet output is not JSON: %v\n%s", err, stdout)
	}
	kinds := map[string]bool{}
	for _, outcome := range outcomes {
		kinds[outcome.Kind] = true
	}
	if !kinds["canonical"] || !kinds["worktree"] {
		t.Fatalf("the fleet sweep missed a kind: %+v", outcomes)
	}
	for _, path := range []string{checkouts.Canonical, checkouts.Worktree} {
		if _, err := os.Stat(filepath.Join(path, checkoutmarker.FileName)); err != nil {
			t.Fatalf("the fleet sweep left %s unmarked: %v", path, err)
		}
	}
	// The sweep is safe to re-run: nothing changes the second time.
	_, second, _ := runCheckoutCommand(t, "--projects-root", checkouts.ProjectsRoot, "worktree", "marker", "--fleet")
	if !strings.Contains(second, "0 changed") {
		t.Fatalf("a repeated fleet sweep rewrote markers:\n%s", second)
	}
}

// TestWorktreeMarkerRejectsAnUnmanagedPath keeps the command from claiming
// authority over a checkout WB does not manage.
func TestWorktreeMarkerRejectsAnUnmanagedPath(t *testing.T) {
	checkouts := newCheckoutFixture(t)
	code, stdout, stderr := runCheckoutCommand(t, "--projects-root", checkouts.ProjectsRoot, "worktree", "marker", t.TempDir())
	if code == exitOK {
		t.Fatal("an unmanaged path was marked")
	}
	// The per-checkout reason is a result and belongs on stdout; the summary
	// that sets the exit code is a diagnostic and belongs on stderr.
	if !strings.Contains(stdout, "not a WB-managed checkout") {
		t.Fatalf("unexpected refusal on stdout: %s", stdout)
	}
	if !strings.Contains(stderr, "could not be described") {
		t.Fatalf("unexpected summary on stderr: %s", stderr)
	}
}
