package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

// canonicalBranchFixture is a real canonical clone with WB's managed hooks
// installed, a bare origin, and the built wb binary: the state of
// sneat-dev/wb#824, where a landing's branch deletion was refused because
// another agent had left the canonical clone on its own branch.
type canonicalBranchFixture struct {
	binary    string
	root      string
	projects  string
	remote    string
	canonical string
	env       []string
	hookEnv   []string
}

func newCanonicalBranchFixture(t *testing.T) canonicalBranchFixture {
	t.Helper()
	binary := buildWB(t)
	root := t.TempDir()
	home := filepath.Join(root, "home")
	projects := filepath.Join(root, "projects")
	fixture := canonicalBranchFixture{
		binary: binary, root: root, projects: projects,
		remote:    filepath.Join(root, "remote.git"),
		canonical: filepath.Join(projects, "acme", "app"),
		env:       wbUpgradeEnv(home),
	}
	fixture.hookEnv = append(append([]string(nil), fixture.env...), "WB_EXECUTABLE="+binary)
	mustUpgradeMkdir(t, home)
	testenv.InitBareRemoteForTest(t, fixture.remote)
	mustUpgradeMkdir(t, filepath.Dir(fixture.canonical))
	upgradeGit(t, root, nil, "clone", fixture.remote, fixture.canonical)
	configureUpgradeGitUser(t, fixture.canonical)
	mustUpgradeWrite(t, filepath.Join(fixture.canonical, "README.md"), "initial\n")
	mustUpgradeWrite(t, filepath.Join(fixture.canonical, ".wb", "hooks.yaml"), "version: 1\nprofiles:\n  include: [worktree]\nmetrics:\n  enabled: false\n")
	upgradeGit(t, fixture.canonical, nil, "add", "README.md", ".wb/hooks.yaml")
	upgradeGit(t, fixture.canonical, nil, "commit", "-m", "initial policy")
	upgradeGit(t, fixture.canonical, nil, "push", "-u", "origin", "main")
	installed := runWBUpgrade(t, binary, fixture.env, "--projects-root", projects, "hooks", "install", fixture.canonical)
	if installed.exitCode != exitOK {
		t.Fatalf("hooks install: exit=%d stdout=%s stderr=%s", installed.exitCode, installed.stdout, installed.stderr)
	}
	return fixture
}

// leaveCanonicalOnAnotherBranch is what another agent does: check out its own
// branch in the shared clone, optionally leaving uncommitted work behind.
func (fixture canonicalBranchFixture) leaveCanonicalOnAnotherBranch(t *testing.T, dirty bool) {
	t.Helper()
	upgradeGit(t, fixture.canonical, fixture.hookEnv, "checkout", "-b", "feat/coverage-100")
	if dirty {
		mustUpgradeWrite(t, filepath.Join(fixture.canonical, "wip.txt"), "another agent's uncommitted work\n")
	}
}

//nolint:paralleltest // builds and runs the real wb binary against real Git hooks.
func TestPrePushGuardInACanonicalCloneOnAnotherBranch(t *testing.T) {
	for _, test := range []struct {
		name  string
		dirty bool
	}{{"clean", false}, {"dirty", true}} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCanonicalBranchFixture(t)
			// Two remote branches to delete, created while the clone is still
			// on main (the guard admits that push).
			upgradeGit(t, fixture.canonical, fixture.hookEnv, "push", "origin", "main:refs/heads/merged-one", "main:refs/heads/merged-two")
			fixture.leaveCanonicalOnAnotherBranch(t, test.dirty)

			// The exact shape wb's own landing push takes: a lease-checked
			// deletion of the merged branch.
			head := upgradeGitOutput(t, fixture.canonical, nil, "rev-parse", "origin/main")
			if err := upgradeGitRun(t, fixture.canonical, fixture.hookEnv,
				"push", "--force-with-lease=refs/heads/merged-one:"+head, "origin", ":refs/heads/merged-one"); err != nil {
				t.Fatalf("a deletion push was refused from a canonical clone on another branch (dirty=%t): %v", test.dirty, err)
			}
			if out := upgradeGitOutput(t, fixture.remote, nil, "branch", "--list", "merged-one"); out != "" {
				t.Fatalf("merged-one still exists on origin: %q", out)
			}
			// Deleting several refs in one push is still only a deletion.
			if err := upgradeGitRun(t, fixture.canonical, fixture.hookEnv, "push", "origin", "--delete", "merged-two"); err != nil {
				t.Fatalf("a delete-only push was refused (dirty=%t): %v", test.dirty, err)
			}

			// Publishing the branch is a different matter: a clean clone on a
			// feature branch may push it, and a dirty one still may not.
			pushErr := upgradeGitRun(t, fixture.canonical, fixture.hookEnv, "push", "origin", "feat/coverage-100")
			switch {
			case test.dirty && (pushErr == nil || !strings.Contains(pushErr.Error(), "must remain clean")):
				t.Fatalf("a dirty canonical clone pushed its branch: %v", pushErr)
			case !test.dirty && pushErr != nil:
				t.Fatalf("a clean canonical clone on a feature branch was refused: %v", pushErr)
			}
			if pushErr != nil && strings.Contains(pushErr.Error(), "must stay on") {
				t.Fatalf("the refusal still claims the clone must stay on main: %v", pushErr)
			}
		})
	}
}

//nolint:paralleltest // builds and runs the real wb binary against real Git hooks.
func TestCheckoutOfAnotherBranchInACanonicalCloneRaisesNoGuardWarning(t *testing.T) {
	fixture := newCanonicalBranchFixture(t)
	output, err := runUpgradeGit(fixture.canonical, fixture.hookEnv, "checkout", "-b", "feat/other")
	if err != nil {
		t.Fatalf("checkout failed: %v\n%s", err, output)
	}
	for _, unwanted := range []string{"must stay on", "outside WB's managed worktree hierarchy"} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("checking out another branch in the canonical clone warned %q:\n%s", unwanted, output)
		}
	}
}

// The incident: `wb worktree cleanup` of a landed task, with the canonical
// clone on another agent's branch, clean or dirty. The merged remote branch is
// deleted and the worktree retired, and the other agent's work is untouched.
//
//nolint:paralleltest // builds and runs the real wb binary against real Git hooks.
func TestLandedWorktreeIsRetiredWhileTheCanonicalCloneIsOnAnotherBranch(t *testing.T) {
	for _, test := range []struct {
		name  string
		dirty bool
	}{{"clean", false}, {"dirty", true}} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCanonicalBranchFixture(t)
			prompt := filepath.Join(fixture.root, "prompt.txt")
			mustUpgradeWrite(t, prompt, "land and clean up\n")
			created := runWBUpgrade(t, fixture.binary, fixture.env, "--projects-root", fixture.projects,
				"worktree", "create", "landed", "acme/app", "--model", "unknown", "--original-prompt-file", prompt)
			if created.exitCode != exitOK {
				t.Fatalf("create: exit=%d stderr=%s", created.exitCode, created.stderr)
			}
			_, afterColon, found := strings.Cut(created.stdout, ": ")
			worktree, _, _ := strings.Cut(afterColon, " (")
			if !found || worktree == "" {
				t.Fatalf("cannot read the worktree path from %q", created.stdout)
			}
			configureUpgradeGitUser(t, worktree)
			mustUpgradeWrite(t, filepath.Join(worktree, "change.txt"), "the landed change\n")
			upgradeGit(t, worktree, fixture.hookEnv, "add", "change.txt")
			upgradeGit(t, worktree, fixture.hookEnv, "commit", "-m", "feat: the change")
			branch := upgradeGitOutput(t, worktree, nil, "branch", "--show-current")
			head := upgradeGitOutput(t, worktree, nil, "rev-parse", "HEAD")
			upgradeGit(t, worktree, fixture.hookEnv, "push", "-u", "origin", branch)
			mergeLegacyIntoMain(t, fixture.remote, fixture.root, branch)

			fixture.leaveCanonicalOnAnotherBranch(t, test.dirty)

			ghDir := filepath.Join(fixture.root, "bin")
			mustUpgradeWriteExecutable(t, filepath.Join(ghDir, "gh"), fmt.Sprintf(`#!/bin/sh
set -eu
printf '[{"number":17,"url":"https://github.com/acme/app/pull/17","state":"MERGED","mergedAt":"2026-07-01T12:00:00Z","headRefOid":"%s","baseRefName":"main"}]\n'
`, head))
			environment := append(append([]string(nil), fixture.env...), "PATH="+ghDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			cleanup := runWBUpgrade(t, fixture.binary, environment, "--projects-root", fixture.projects,
				"worktree", "cleanup", "landed", "--apply", "--remote", "--older-than", "0s")
			if cleanup.exitCode != exitOK {
				t.Fatalf("cleanup with the canonical clone on another branch (dirty=%t): exit=%d\nstdout=%s\nstderr=%s", test.dirty, cleanup.exitCode, cleanup.stdout, cleanup.stderr)
			}
			if _, err := os.Stat(worktree); !os.IsNotExist(err) {
				t.Fatalf("the landed worktree was not retired: %v", err)
			}
			if out := upgradeGitOutput(t, fixture.remote, nil, "branch", "--list", branch); out != "" {
				t.Fatalf("the merged remote branch %s still exists: %q", branch, out)
			}
			if got := upgradeGitOutput(t, fixture.canonical, nil, "branch", "--show-current"); got != "feat/coverage-100" {
				t.Fatalf("cleanup moved the canonical clone off its branch: now on %q", got)
			}
			if test.dirty {
				if content, err := os.ReadFile(filepath.Join(fixture.canonical, "wip.txt")); err != nil || !strings.Contains(string(content), "uncommitted work") {
					t.Fatalf("cleanup disturbed the canonical clone's uncommitted work: %q, %v", content, err)
				}
			}
		})
	}
}
