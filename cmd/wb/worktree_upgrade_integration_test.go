package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPreviousReleaseWorktreeUpgrade is intentionally black-box. It installs
// the old managed shim format first, then uses the candidate binary exactly as
// a workstation upgrade would. This catches incompatibilities that unit tests
// of either the resolver or the hook manager cannot see in isolation.
func TestPreviousReleaseWorktreeUpgrade(t *testing.T) {
	binary := buildWB(t)
	root := t.TempDir()
	home := filepath.Join(root, "home")
	projects := filepath.Join(root, "projects")
	remote := filepath.Join(root, "remote.git")
	canonical := filepath.Join(projects, "acme", "app")
	// Candidate WB invocations deliberately have neither WB_EXECUTABLE nor the
	// candidate binary on PATH. Their child Git hooks can succeed only when the
	// running CLI transiently propagates its own executable. Standalone Git
	// commands later in the journey model an operator-provided override.
	upgradeEnv := wbUpgradeEnv(home)
	hookEnv := append(append([]string(nil), upgradeEnv...), "WB_EXECUTABLE="+binary)
	mustUpgradeMkdir(t, home)
	resolvedHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}

	upgradeGit(t, root, nil, "init", "--bare", "--initial-branch=main", remote)
	mustUpgradeMkdir(t, filepath.Dir(canonical))
	upgradeGit(t, root, nil, "clone", remote, canonical)
	configureUpgradeGitUser(t, canonical)
	mustUpgradeWrite(t, filepath.Join(canonical, "README.md"), "initial\n")
	mustUpgradeWrite(t, filepath.Join(canonical, ".wb", "hooks.yaml"), "version: 1\nprofiles:\n  include: [worktree]\nmetrics:\n  enabled: false\n")
	upgradeGit(t, canonical, nil, "add", "README.md", ".wb/hooks.yaml")
	upgradeGit(t, canonical, nil, "commit", "-m", "initial policy")
	upgradeGit(t, canonical, nil, "push", "-u", "origin", "main")

	legacy := filepath.Join(projects, ".wb", "worktrees", "legacy", "acme", "app")
	upgradeGit(t, canonical, nil, "worktree", "add", "-b", "feature/legacy", legacy, "main")
	configureUpgradeGitUser(t, legacy)
	installPreviousReleaseHooks(t, binary, projects, canonical)

	prompt := filepath.Join(root, "upgrade-original-prompt.txt")
	mustUpgradeWrite(t, prompt, "upgrade the managed worktree contract\n")
	created := runWBUpgrade(t, binary, upgradeEnv, "--projects-root", projects, "worktree", "create", "upgrade", "acme/app", "--original-prompt-file", prompt)
	if created.exitCode != exitOK {
		t.Fatalf("candidate create failed: %s", created.stderr)
	}
	worktree := filepath.Join(resolvedHome, ".wb", "worktrees", "upgrade", "acme", "app")
	if !strings.Contains(created.stdout, worktree) {
		t.Fatalf("candidate create did not use default home %s:\n%s", worktree, created.stdout)
	}
	if strings.Contains(created.stdout, filepath.Join(projects, ".wb")) {
		t.Fatalf("candidate create silently reused legacy home:\n%s", created.stdout)
	}
	common := filepath.Join(canonical, ".git", "wb-hooks")
	refreshed, err := os.ReadFile(filepath.Join(common, "pre-commit"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(refreshed), "export WB_HOME='"+filepath.Join(resolvedHome, ".wb")+"'") ||
		!strings.Contains(string(refreshed), "WB_HOME_MIGRATION_COMPAT='"+filepath.Join(resolvedHome, ".wb")+"'") {
		checked := runWBUpgrade(t, binary, upgradeEnv, "--projects-root", projects, "hooks", "check", canonical)
		t.Fatalf("candidate did not refresh previous-release hook home:\n%s\ncheck exit=%d stdout=%s stderr=%s", refreshed, checked.exitCode, checked.stdout, checked.stderr)
	}
	if strings.Contains(string(refreshed), binary) || !strings.Contains(string(refreshed), "command -v wb") {
		t.Fatalf("candidate retained the previous release's installer path instead of a runtime resolver:\n%s", refreshed)
	}

	// Both new and legacy linked worktrees now run the refreshed guard. A
	// commit in each proves the default-pinned shim retains migration access.
	mustUpgradeWrite(t, filepath.Join(worktree, "feature.txt"), "new home\n")
	upgradeGit(t, worktree, hookEnv, "add", "feature.txt")
	upgradeGit(t, worktree, hookEnv, "commit", "-m", "new home feature")
	mustUpgradeWrite(t, filepath.Join(legacy, "legacy.txt"), "legacy\n")
	upgradeGit(t, legacy, hookEnv, "add", "legacy.txt")
	upgradeGit(t, legacy, hookEnv, "commit", "-m", "legacy feature")
	legacyHead := upgradeGitOutput(t, legacy, hookEnv, "rev-parse", "HEAD")
	upgradeGit(t, legacy, hookEnv, "push", "-u", "origin", "feature/legacy")

	listed := runWBUpgrade(t, binary, upgradeEnv, "--projects-root", projects, "worktree", "list", "--format", "json")
	if listed.exitCode != exitOK || !strings.Contains(listed.stdout, worktree) || !strings.Contains(listed.stdout, legacy) {
		t.Fatalf("candidate list did not inventory both layouts: exit=%d stdout=%s stderr=%s", listed.exitCode, listed.stdout, listed.stderr)
	}

	// Merge the legacy branch through an unhooked writer so the canonical clone
	// remains a clean base checkout throughout the fixture.
	mergeLegacyIntoMain(t, remote, root, "feature/legacy")

	// A real conflict leaves Git in rebase-merge. The refreshed post-checkout
	// hook and an explicit candidate guard must both accept only that transient
	// detached state, then return to normal after --continue.
	mustUpgradeWrite(t, filepath.Join(worktree, "README.md"), "feature side\n")
	upgradeGit(t, worktree, hookEnv, "add", "README.md")
	upgradeGit(t, worktree, hookEnv, "commit", "-m", "conflicting feature")
	writeMainConflict(t, remote, root)
	upgradeGit(t, worktree, hookEnv, "fetch", "origin", "main")
	if output, err := runUpgradeGit(worktree, hookEnv, "rebase", "origin/main"); err == nil {
		t.Fatalf("rebase unexpectedly succeeded:\n%s", output)
	}
	guarded := runWBUpgrade(t, binary, upgradeEnv, "--projects-root", projects, "worktree", "guard", worktree)
	if guarded.exitCode != exitOK {
		t.Fatalf("candidate guard rejected active rebase: %s", guarded.stderr)
	}
	mustUpgradeWrite(t, filepath.Join(worktree, "README.md"), "resolved\n")
	upgradeGit(t, worktree, hookEnv, "add", "README.md")
	if output, err := runUpgradeGit(worktree, append(hookEnv, "GIT_EDITOR=true"), "rebase", "--continue"); err != nil {
		t.Fatalf("finish rebase: %v\n%s", err, output)
	}
	guarded = runWBUpgrade(t, binary, upgradeEnv, "--projects-root", projects, "worktree", "guard", worktree)
	if guarded.exitCode != exitOK {
		t.Fatalf("candidate guard rejected completed rebase: %s", guarded.stderr)
	}

	ghDir := filepath.Join(root, "bin")
	mustUpgradeMkdir(t, ghDir)
	mustUpgradeWriteExecutable(t, filepath.Join(ghDir, "gh"), fmt.Sprintf(`#!/bin/sh
set -eu
printf '[{"number":17,"url":"https://github.com/acme/app/pull/17","state":"MERGED","mergedAt":"2026-07-01T12:00:00Z","headRefOid":"%s","baseRefName":"main"}]\n'
`, legacyHead))
	cleanupEnv := append(upgradeEnv, "PATH="+ghDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cleanup := runWBUpgrade(t, binary, cleanupEnv, "--projects-root", projects, "worktree", "cleanup", "legacy", "--apply", "--remote", "--older-than", "0s")
	if cleanup.exitCode != exitOK {
		t.Fatalf("candidate cleanup failed: %s\n%s", cleanup.stdout, cleanup.stderr)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("candidate cleanup left legacy worktree: %v", err)
	}
}

// TestHooksInstallReportsAndBlocksRawWorktreeAtAdmission is black-box evidence
// for the admission boundary: an ordinary external `git worktree add` reports
// the unsafe checkout immediately without pretending post-checkout can undo
// it, while pre-commit blocks work from landing and WB's staged managed create
// remains allowed. The raw checkout stays visible for explicit recovery rather
// than WB silently deleting evidence it does not own.
func TestHooksInstallReportsAndBlocksRawWorktreeAtAdmission(t *testing.T) {
	binary := buildWB(t)
	root := t.TempDir()
	home := filepath.Join(root, "home")
	projects := filepath.Join(root, "projects")
	remote := filepath.Join(root, "remote.git")
	canonical := filepath.Join(projects, "acme", "app")
	environment := append(wbUpgradeEnv(home), "WB_EXECUTABLE="+binary)

	upgradeGit(t, root, nil, "init", "--bare", "--initial-branch=main", remote)
	mustUpgradeMkdir(t, filepath.Dir(canonical))
	upgradeGit(t, root, nil, "clone", remote, canonical)
	configureUpgradeGitUser(t, canonical)
	mustUpgradeWrite(t, filepath.Join(canonical, "README.md"), "initial\n")
	upgradeGit(t, canonical, nil, "add", "README.md")
	upgradeGit(t, canonical, nil, "commit", "-m", "initial")
	upgradeGit(t, canonical, nil, "push", "-u", "origin", "main")

	installed := runWBUpgrade(t, binary, environment, "--projects-root", projects, "hooks", "install", canonical)
	if installed.exitCode != exitOK || !strings.Contains(installed.stdout, "post-checkout") {
		t.Fatalf("default hooks install did not install admission guard: exit=%d stdout=%s stderr=%s", installed.exitCode, installed.stdout, installed.stderr)
	}

	raw := filepath.Join(root, "raw-external-worktree")
	output, rawErr := runUpgradeGit(canonical, environment, "worktree", "add", "-b", "feature/raw-external", raw, "main")
	if rawErr != nil {
		t.Fatalf("post-checkout reported after Git had already created the raw worktree but corrupted Git's control flow: %v\n%s", rawErr, output)
	}
	for _, message := range []string{
		"checkout already happened outside WB's managed worktree hierarchy",
		"wb worktree rescue is not available yet",
	} {
		if !strings.Contains(output, message) {
			t.Fatalf("raw worktree admission report is missing %q:\n%s", message, output)
		}
	}
	if _, err := os.Stat(raw); err != nil {
		t.Fatalf("reported raw checkout was silently removed instead of remaining recoverable: %v", err)
	}
	registeredRaw, err := filepath.EvalSymlinks(raw)
	if err != nil {
		t.Fatalf("resolve rejected raw checkout for Git registration comparison: %v", err)
	}
	listing := upgradeGitOutput(t, canonical, environment, "worktree", "list", "--porcelain")
	if !strings.Contains(listing, "worktree "+registeredRaw) {
		t.Fatalf("rejected raw checkout was not visibly registered for recovery:\n%s", listing)
	}
	guarded := runWBUpgrade(t, binary, environment, "--projects-root", projects, "worktree", "guard", raw)
	if guarded.exitCode == exitOK || !strings.Contains(guarded.stderr, "must be below a resolver-recognized") {
		t.Fatalf("raw checkout guard = exit=%d stdout=%s stderr=%s", guarded.exitCode, guarded.stdout, guarded.stderr)
	}
	mustUpgradeWrite(t, filepath.Join(raw, "blocked.txt"), "must not land from an unmanaged checkout\n")
	upgradeGit(t, raw, environment, "add", "blocked.txt")
	commitOutput, commitErr := runUpgradeGit(raw, environment, "commit", "-m", "must be blocked")
	if commitErr == nil || !strings.Contains(commitOutput, "must be below a resolver-recognized") {
		t.Fatalf("unmanaged checkout commit was not blocked: err=%v\n%s", commitErr, commitOutput)
	}

	prompt := filepath.Join(root, "managed-prompt.txt")
	mustUpgradeWrite(t, prompt, "create managed checkout\n")
	managed := runWBUpgrade(t, binary, environment, "--projects-root", projects, "worktree", "create", "managed", "acme/app", "--original-prompt-file", prompt)
	if managed.exitCode != exitOK {
		t.Fatalf("WB-managed checkout did not pass its own admission guard: stdout=%s stderr=%s", managed.stdout, managed.stderr)
	}
}

type wbUpgradeResult struct {
	stdout   string
	stderr   string
	exitCode int
}

func runWBUpgrade(t *testing.T, binary string, environment []string, args ...string) wbUpgradeResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = append(upgradeEnvironmentWithout(os.Environ(), "WB_EXECUTABLE"), environment...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if ctx.Err() != nil {
		t.Fatalf("wb %s timed out", strings.Join(args, " "))
	}
	result := wbUpgradeResult{stdout: stdout.String(), stderr: stderr.String()}
	var exitErr *exec.ExitError
	if err == nil {
		return result
	} else if errors.As(err, &exitErr) {
		result.exitCode = exitErr.ExitCode()
		return result
	}
	t.Fatalf("wb %s: %v", strings.Join(args, " "), err)
	return wbUpgradeResult{}
}

func upgradeEnvironmentWithout(environment []string, names ...string) []string {
	removed := make(map[string]bool, len(names))
	for _, name := range names {
		removed[name] = true
	}
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if found && removed[name] {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func wbUpgradeEnv(home string) []string {
	// The fixture must not inherit an operator's global hooks policy. In
	// particular, an explicit worktree-profile exclusion would make this
	// black-box default-policy test appear to install a disabled guard.
	return []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
		"WB_HOME=",
		"WB_HOME_MIGRATION_COMPAT=",
	}
}

func installPreviousReleaseHooks(t *testing.T, binary, projects, canonical string) {
	t.Helper()
	managed := filepath.Join(canonical, ".git", "wb-hooks")
	mustUpgradeMkdir(t, managed)
	upgradeGit(t, canonical, nil, "config", "core.hooksPath", managed)
	for _, hook := range []string{"post-checkout", "pre-commit", "pre-push"} {
		mustUpgradeWriteExecutable(t, filepath.Join(managed, hook), "#!/bin/sh\nset -eu\n\n### Start of WB managed hook ###\n"+
			shellQuoteUpgrade(binary)+" --projects-root "+shellQuoteUpgrade(projects)+" hooks run "+shellQuoteUpgrade(hook)+" -- \"$@\"\n"+
			"_wb_hook_status=$?\nif [ \"$_wb_hook_status\" -ne 0 ]; then\n    exit \"$_wb_hook_status\"\nfi\n### End of WB managed hook ###\n")
	}
}

func mergeLegacyIntoMain(t *testing.T, remote, root, branch string) {
	t.Helper()
	writer := filepath.Join(root, "writer-legacy")
	upgradeGit(t, root, nil, "clone", remote, writer)
	configureUpgradeGitUser(t, writer)
	upgradeGit(t, writer, nil, "fetch", "origin", branch)
	upgradeGit(t, writer, nil, "merge", "--no-ff", "origin/"+branch, "-m", "merge legacy")
	upgradeGit(t, writer, nil, "push", "origin", "main")
}

func writeMainConflict(t *testing.T, remote, root string) {
	t.Helper()
	writer := filepath.Join(root, "writer-main")
	upgradeGit(t, root, nil, "clone", remote, writer)
	configureUpgradeGitUser(t, writer)
	mustUpgradeWrite(t, filepath.Join(writer, "README.md"), "main side\n")
	upgradeGit(t, writer, nil, "add", "README.md")
	upgradeGit(t, writer, nil, "commit", "-m", "conflicting main")
	upgradeGit(t, writer, nil, "push", "origin", "main")
}

func configureUpgradeGitUser(t *testing.T, directory string) {
	t.Helper()
	upgradeGit(t, directory, nil, "config", "user.name", "WB Upgrade Test")
	upgradeGit(t, directory, nil, "config", "user.email", "wb-upgrade@example.test")
}

func upgradeGit(t *testing.T, directory string, environment []string, args ...string) {
	t.Helper()
	if output, err := runUpgradeGit(directory, environment, args...); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func upgradeGitOutput(t *testing.T, directory string, environment []string, args ...string) string {
	t.Helper()
	output, err := runUpgradeGit(directory, environment, args...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(output)
}

func runUpgradeGit(directory string, environment []string, args ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	command.Env = append(os.Environ(), environment...)
	output, err := command.CombinedOutput()
	return string(output), err
}

func mustUpgradeWrite(t *testing.T, path, content string) {
	t.Helper()
	mustUpgradeMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustUpgradeWriteExecutable(t *testing.T, path, content string) {
	t.Helper()
	mustUpgradeMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustUpgradeMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func shellQuoteUpgrade(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `"'"'"'`) + "'"
}
