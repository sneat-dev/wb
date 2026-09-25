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
	"github.com/sneat-dev/wb/internal/runner/runnertest"
	"github.com/sneat-dev/wb/internal/testenv"
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
	testenv.InitBareRemoteForTest(t, origin)
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
	// registeredWorktrees now runs its `git worktree list` through
	// internal/runner (task-8), and this test's fleet sweep depends on real
	// git registering the linked worktree the fixture below builds. This
	// file is already on internal/quality/testdata/unit_tier.pending
	// (task-22).
	runnertest.AllowRealProcess(t)
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

// TestWorktreeCreateThenCleanupApplyRetiresHostLevelWorktree is the exact
// CLI reproduction from sneat-dev/wb#594:
//
//	$ wb worktree create <task> <owner>/<repo> --base main ...
//	created ...: <projects-root>/.worktrees/<task>/github.com/<owner>/<repo>
//	$ wb worktree cleanup <task> --apply
//	error: cleanup worktree ... has unsupported hierarchy
//
// `wb worktree create` writes the default central-store host-level layout
// (no ~/.config/wb/worktrees.yaml present, exactly the report's own note),
// and `wb worktree cleanup --apply` must retire that exact path once the
// branch has landed, rather than refusing it.
func TestWorktreeCreateThenCleanupApplyRetiresHostLevelWorktree(t *testing.T) {
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	remote := filepath.Join(root, "remote.git")
	canonical := filepath.Join(projectsRoot, "acme", "app")
	testenv.InitBareRemoteForTest(t, remote)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	gcGit(t, root, "clone", remote, canonical)
	gcGit(t, canonical, "config", "user.email", "wb-test@example.com")
	gcGit(t, canonical, "config", "user.name", "wb-test")
	if err := os.WriteFile(filepath.Join(canonical, "README.md"), []byte("# app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gcGit(t, canonical, "add", "README.md")
	gcGit(t, canonical, "commit", "-m", "initial")
	gcGit(t, canonical, "push", "-u", "origin", "main")
	// Name a real forge in the origin, aliased locally, so the default
	// central store mode derives the literal host level exactly as it does
	// for a real clone of a github.com repository.
	forgeURL := "https://github.com/acme/app.git"
	gcGit(t, canonical, "config", "url."+remote+".insteadOf", forgeURL)
	gcGit(t, canonical, "remote", "set-url", "origin", forgeURL)

	// Central is the default store mode; an isolated, empty config
	// directory keeps this hermetic against any ambient user config.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	installGCFakeGh(t)
	prompt := writeOriginalPromptFixture(t, "host-level cleanup e2e prompt")

	const task = "host-e2e"
	created := runWB(t, "worktree", "create", task, "acme/app",
		"--projects-root", projectsRoot, "--model", "unknown",
		"--mode", "manual", "--initiator", "wb-test",
		"--original-prompt-file", prompt, "--format", "json")
	if created.exitCode != exitOK {
		t.Fatalf("create exit = %d stderr=%s stdout=%s", created.exitCode, created.stderr, created.stdout)
	}
	var results []worktrees.CreateResult
	if err := json.Unmarshal([]byte(created.stdout), &results); err != nil {
		t.Fatalf("decode create output: %v\n%s", err, created.stdout)
	}
	if len(results) != 1 {
		t.Fatalf("create results = %+v", results)
	}
	worktree := results[0].WorktreeDir
	wantWorktree := filepath.Join(projectsRoot, ".worktrees", task, "github.com", "acme", "app")
	if worktree != wantWorktree {
		t.Fatalf("create did not place the checkout at the host-level layout the bug reported: got %q, want %q", worktree, wantWorktree)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("created worktree missing: %v", err)
	}

	// Land the branch directly into canonical main — the same "direct push,
	// no pull request" integration path the worktrees package's own cleanup
	// tests already rely on — so cleanup finds it eligible without any real
	// GitHub state.
	if err := os.WriteFile(filepath.Join(worktree, "feature.txt"), []byte("host-level\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gcGit(t, worktree, "add", "feature.txt")
	gcGit(t, worktree, "commit", "-m", "feature")
	gcGit(t, worktree, "push", "-u", "origin", results[0].Branch)
	gcGit(t, canonical, "merge", "--no-ff", results[0].Branch, "-m", "merge feature")
	gcGit(t, canonical, "push", "origin", "main")

	ownerDir := filepath.Dir(worktree)
	hostDir := filepath.Dir(ownerDir)

	cleaned := runWB(t, "worktree", "cleanup", task, "--apply", "--remote", "--projects-root", projectsRoot, "--format", "json")
	if cleaned.exitCode != exitOK {
		t.Fatalf("cleanup exit = %d stderr=%s stdout=%s", cleaned.exitCode, cleaned.stderr, cleaned.stdout)
	}
	if strings.Contains(cleaned.stderr, "unsupported hierarchy") {
		t.Fatalf("cleanup refused the exact host-level layout create just wrote: %s", cleaned.stderr)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree survived cleanup --apply: %v", err)
	}
	if _, err := os.Stat(ownerDir); !os.IsNotExist(err) {
		t.Fatalf("empty owner directory %s was not retired: %v", ownerDir, err)
	}
	if _, err := os.Stat(hostDir); !os.IsNotExist(err) {
		t.Fatalf("empty host directory %s was not retired: %v", hostDir, err)
	}
}
