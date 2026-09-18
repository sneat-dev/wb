package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/canonicalrescue"
	"github.com/sneat-dev/wb/internal/checkoutmarker"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

type hostLevelCheckoutFixture struct {
	ProjectsRoot string
	Canonical    string
	Worktree     string
}

// newHostLevelCheckoutFixture builds the shape a migrated machine has: the
// canonical clone sits at <root>/github.com/{owner}/{repo} with a real linked
// worktree, and nothing is flat. Every command that has to find a canonical
// clone must find this one.
func newHostLevelCheckoutFixture(t *testing.T) hostLevelCheckoutFixture {
	t.Helper()
	base := t.TempDir()
	origin := filepath.Join(base, "origin.git")
	runCommand(t, base, "git", "init", "-q", "--bare", origin)
	projectsRoot := filepath.Join(base, "projects")
	canonical := filepath.Join(projectsRoot, "github.com", "acme", "app")
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	runCommand(t, base, "git", "clone", "-q", origin, canonical)
	runCommand(t, canonical, "git", "config", "user.email", "wb@example.test")
	runCommand(t, canonical, "git", "config", "user.name", "wb")
	if err := os.WriteFile(filepath.Join(canonical, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCommand(t, canonical, "git", "add", "-A")
	runCommand(t, canonical, "git", "commit", "-qm", "init")
	runCommand(t, canonical, "git", "branch", "-M", "main")
	runCommand(t, canonical, "git", "push", "-q", "origin", "main")
	worktree := filepath.Join(base, "worktrees", "host-task", "acme", "app")
	runCommand(t, canonical, "git", "worktree", "add", "-q", "-b", "host-task", worktree)
	t.Setenv("HOME", filepath.Join(base, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	t.Setenv(wbhome.EnvOverride, projectsRoot)
	return hostLevelCheckoutFixture{ProjectsRoot: projectsRoot, Canonical: canonical, Worktree: worktree}
}

// TestWorktreeCreateMarksAHostLevelCanonicalClone is the regression for the
// marker the AGENTS.md contract depends on: `wb worktree create` must refresh
// .worktree.md in the canonical clone it cut the checkout from, wherever that
// clone lives. Rebuilding a flat <root>/{owner}/{repository} path silently
// skipped every host-level clone, so on a migrated fleet the canonical marker
// was never written and the warning named a path that does not exist.
func TestWorktreeCreateMarksAHostLevelCanonicalClone(t *testing.T) {
	checkouts := newHostLevelCheckoutFixture(t)
	prompt := writeOriginalPromptFixture(t, "host level create prompt")
	previousProjectsRoot := projectsRoot
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })

	var stdout, stderr bytes.Buffer
	code := run([]string{"--projects-root", checkouts.ProjectsRoot, "worktree", "create", "host-create", "acme/app",
		"--model", "unknown", "--original-prompt-file", prompt, "--no-claim", "--format", "json"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("worktree create exited %d: %s%s", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), "is not a WB-managed checkout") {
		t.Fatalf("create reported the canonical clone as unmanaged:\n%s", stderr.String())
	}
	var results []worktrees.CreateResult
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
		t.Fatalf("decode create output: %v\n%s", err, stdout.String())
	}
	if len(results) != 1 {
		t.Fatalf("results = %+v", results)
	}
	result := results[0]
	if filepath.Clean(result.CanonicalDir) != filepath.Clean(checkouts.Canonical) {
		t.Fatalf("create used canonical %q, want the host-level clone %q", result.CanonicalDir, checkouts.Canonical)
	}

	canonicalMarker, err := os.ReadFile(filepath.Join(checkouts.Canonical, checkoutmarker.FileName))
	if err != nil {
		t.Fatalf("the canonical clone did not receive %s: %v (stderr: %s)", checkoutmarker.FileName, err, stderr.String())
	}
	if !strings.Contains(string(canonicalMarker), "kind: canonical") {
		t.Fatalf("canonical marker does not say kind: canonical:\n%s", canonicalMarker)
	}
	if result.WorktreeDir == "" {
		t.Fatal("create did not report its checkout")
	}
	worktreeMarker, err := os.ReadFile(filepath.Join(result.WorktreeDir, checkoutmarker.FileName))
	if err != nil {
		t.Fatalf("the created checkout did not receive %s: %v", checkoutmarker.FileName, err)
	}
	if !strings.Contains(string(worktreeMarker), "kind: worktree") {
		t.Fatalf("checkout marker does not say kind: worktree:\n%s", worktreeMarker)
	}
	if _, err := os.Stat(filepath.Join(checkouts.ProjectsRoot, "acme", "app")); !os.IsNotExist(err) {
		t.Fatalf("create produced a host-less clone at %s", filepath.Join(checkouts.ProjectsRoot, "acme", "app"))
	}
}

// TestWorktreeMarkerFleetCoversAHostLevelClone proves the fleet sweep — the
// refresh AGENTS.md tells every agent to rely on — reaches a clone that only
// exists at the literal host level, and its linked worktree with it.
func TestWorktreeMarkerFleetCoversAHostLevelClone(t *testing.T) {
	checkouts := newHostLevelCheckoutFixture(t)

	code, stdout, stderr := runCheckoutCommand(t, "--projects-root", checkouts.ProjectsRoot, "worktree", "marker", "--fleet", "--format", "json")
	if code != exitOK {
		t.Fatalf("fleet marker exited %d: %s%s", code, stdout, stderr)
	}
	kinds := map[string]bool{}
	for _, path := range []string{checkouts.Canonical, checkouts.Worktree} {
		marker, err := os.ReadFile(filepath.Join(path, checkoutmarker.FileName))
		if err != nil {
			t.Fatalf("fleet marker skipped %s: %v\n%s", path, err, stdout)
		}
		if strings.Contains(string(marker), "kind: canonical") {
			kinds["canonical"] = true
		}
		if strings.Contains(string(marker), "kind: worktree") {
			kinds["worktree"] = true
		}
	}
	if !kinds["canonical"] || !kinds["worktree"] {
		t.Fatalf("the fleet sweep missed a kind: %v\n%s", kinds, stdout)
	}
}

// TestWorktreeRescueFleetFindsAHostLevelClone proves the rescue sweep sees
// uncommitted work sitting in a host-level canonical clone, which is where a
// migrated fleet keeps every clone.
func TestWorktreeRescueFleetFindsAHostLevelClone(t *testing.T) {
	checkouts := newHostLevelCheckoutFixture(t)
	if err := os.WriteFile(filepath.Join(checkouts.Canonical, "README.md"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The text form is the one whose exit code is the findings contract.
	code, stdout, stderr := runCheckoutCommand(t, "--projects-root", checkouts.ProjectsRoot, "worktree", "rescue", "--fleet")
	if code != exitFindings {
		t.Fatalf("fleet rescue exited %d, want findings: %s%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, checkouts.Canonical) {
		t.Fatalf("the fleet sweep did not name the host-level clone:\n%s", stdout)
	}

	jsonCode, jsonOut, jsonErr := runCheckoutCommand(t, "--projects-root", checkouts.ProjectsRoot, "worktree", "rescue", "--fleet", "--format", "json")
	if jsonCode != exitOK {
		t.Fatalf("fleet rescue json exited %d: %s%s", jsonCode, jsonOut, jsonErr)
	}
	var reports []canonicalrescue.Report
	if err := json.Unmarshal([]byte(jsonOut), &reports); err != nil {
		t.Fatalf("fleet rescue output is not JSON: %v\n%s", err, jsonOut)
	}
	found := false
	for _, report := range reports {
		if filepath.Clean(report.Path) == filepath.Clean(checkouts.Canonical) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the fleet sweep did not report the host-level clone: %+v", reports)
	}

	// The single-clone form must work on the same path, unchanged.
	code, stdout, stderr = runCheckoutCommand(t, "--projects-root", checkouts.ProjectsRoot, "worktree", "rescue", checkouts.Canonical)
	if code != exitFindings {
		t.Fatalf("rescue of the host-level clone exited %d, want findings: %s%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "Nothing has been changed") {
		t.Fatalf("the report does not say it changed nothing:\n%s", stdout)
	}
}
