package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pushTierTestRepo builds a real, minimal Git repository with a real "origin"
// remote (a local bare repo) and a pushed main branch, so detectDefaultBranch
// can resolve "main" from purely local Git state -- exactly as it would after
// a real clone, with no network access anywhere in this test.
func pushTierTestRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	repo := filepath.Join(root, "work")
	pushTierGit(t, root, "init", "--bare", "--initial-branch=main", bare)
	pushTierGit(t, root, "init", "-b", "main", repo)
	pushTierGit(t, repo, "config", "user.name", "wb-test")
	pushTierGit(t, repo, "config", "user.email", "wb-test@example.invalid")
	pushTierGit(t, repo, "remote", "add", "origin", bare)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pushTierGit(t, repo, "add", "README.md")
	pushTierGit(t, repo, "commit", "-m", "initial")
	// Pushing main creates the local refs/remotes/origin/main WB's
	// detectDefaultBranch falls back to when origin/HEAD was never recorded --
	// exactly the state a fresh, non-cloned repository is in.
	pushTierGit(t, repo, "push", "-u", "origin", "main")
	return repo
}

func pushTierGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// fakeGHOnPath builds a minimal PATH containing only git (so wb's own git
// calls keep working) plus, when withGH is true, a fake `gh` that answers the
// production open-PR observation (`gh api --method GET repos/<slug>/pulls ...`)
// with one open PR when branch equals openBranch, and an empty list otherwise.
// It deliberately excludes any real `gh` on the machine's PATH: these
// scenarios must never make a real network call.
func fakeGHOnPath(t *testing.T, withGH bool, openBranch string) string {
	t.Helper()
	dir := t.TempDir()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(gitPath, filepath.Join(dir, "git")); err != nil {
		t.Fatal(err)
	}
	if withGH {
		script := "#!/bin/sh\n" +
			"branch=\"\"\n" +
			"for arg in \"$@\"; do\n" +
			"  case \"$arg\" in\n" +
			"    head=*:*) branch=${arg#head=*:} ;;\n" +
			"  esac\n" +
			"done\n" +
			"if [ \"$branch\" = \"" + openBranch + "\" ]; then\n" +
			"  echo '[{\"number\":42}]'\n" +
			"else\n" +
			"  echo '[]'\n" +
			"fi\n"
		ghPath := filepath.Join(dir, "gh")
		if err := os.WriteFile(ghPath, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// runPushTier runs the real, compiled `wb hooks push-tier` as a subprocess
// against repo, feeding stdin exactly as Git's pre-push hook would, on a PATH
// that deliberately excludes the machine's real gh. It returns the exit code
// (the tier: 0, 1, or 2) and combined output.
func runPushTier(t *testing.T, repo, stdin, path string) (exitCode int, output string) {
	t.Helper()
	binary := buildWB(t)
	ctx, cancel := context.WithTimeout(context.Background(), smokeDeadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "hooks", "push-tier")
	cmd.Dir = repo
	cmd.Stdin = strings.NewReader(stdin)
	stateHome := t.TempDir()
	cmd.Env = []string{
		"PATH=" + path,
		"HOME=" + t.TempDir(),
		"XDG_STATE_HOME=" + stateHome,
		"TERM=dumb",
	}
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("wb hooks push-tier did not exit within %s; output so far: %s", smokeDeadline, out)
	}
	code := 0
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		code = 0
	case isExitError(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		t.Fatalf("wb hooks push-tier: %v\n%s", err, out)
	}
	return code, string(out)
}

func isExitError(err error, target **exec.ExitError) bool {
	if e, ok := err.(*exec.ExitError); ok {
		*target = e
		return true
	}
	return false
}

func zeroOID() string { return strings.Repeat("0", 40) }
func fakeOID(b byte) string {
	buf := make([]byte, 40)
	for i := range buf {
		buf[i] = b
	}
	return string(buf)
}

func refLine(localRef, localSHA, remoteRef, remoteSHA string) string {
	return localRef + " " + localSHA + " " + remoteRef + " " + remoteSHA + "\n"
}

// TestPushTierCLISixFoundationalScenarios drives the real, compiled `wb
// hooks push-tier` (not a fake stand-in) through the exact six scenarios the
// founder named, end to end through the actual CLI/cobra wiring in
// newHooksPushTierCmd -- proving the production exit-code contract, not just
// the underlying pure classification function.
func TestPushTierCLISixFoundationalScenarios(t *testing.T) {
	t.Parallel()
	repo := pushTierTestRepo(t)
	offlinePath := fakeGHOnPath(t, false, "")
	onlinePath := fakeGHOnPath(t, true, "has-pr")

	tests := []struct {
		name     string
		stdin    string
		path     string
		wantTier int
	}{
		{
			name:     "branch with no open PR runs tier 1 (gh absent, offline-safe)",
			stdin:    refLine("refs/heads/no-pr", fakeOID('a'), "refs/heads/no-pr", zeroOID()),
			path:     offlinePath,
			wantTier: 1,
		},
		{
			name:     "branch with an open PR runs tier 2",
			stdin:    refLine("refs/heads/has-pr", fakeOID('a'), "refs/heads/has-pr", zeroOID()),
			path:     onlinePath,
			wantTier: 2,
		},
		{
			name:     "the default branch runs tier 2",
			stdin:    refLine("refs/heads/main", fakeOID('a'), "refs/heads/main", fakeOID('b')),
			path:     offlinePath,
			wantTier: 2,
		},
		{
			name:     "a tag runs tier 2",
			stdin:    refLine("refs/tags/v1.0.0", fakeOID('a'), "refs/tags/v1.0.0", zeroOID()),
			path:     offlinePath,
			wantTier: 2,
		},
		{
			name:     "a deletion-only push runs tier 0",
			stdin:    refLine("(delete)", zeroOID(), "refs/heads/no-pr", fakeOID('a')),
			path:     offlinePath,
			wantTier: 0,
		},
		{
			name:     "a WB checkpoint-ref push runs tier 0",
			stdin:    refLine("refs/heads/task", fakeOID('a'), "refs/wb/checkpoints/task", fakeOID('b')),
			path:     offlinePath,
			wantTier: 0,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, output := runPushTier(t, repo, test.stdin, test.path)
			if code != test.wantTier {
				t.Fatalf("exit code = %d, want %d\noutput: %s", code, test.wantTier, output)
			}
			if !strings.Contains(output, "WB hook: tier") {
				t.Fatalf("output did not print a one-line tier decision: %q", output)
			}
		})
	}
}

// TestPushTierCLINeverBlocksOnMissingGH proves the offline-safety property
// directly: the "no open PR" scenario above must complete within a small
// fraction of the smoke deadline, not merely before it, since a stalled `gh`
// lookup timing out would still technically finish inside a 60s deadline.
func TestPushTierCLINeverBlocksOnMissingGH(t *testing.T) {
	repo := pushTierTestRepo(t)
	offlinePath := fakeGHOnPath(t, false, "")
	started := time.Now()
	code, _ := runPushTier(t, repo, refLine("refs/heads/no-pr", fakeOID('a'), "refs/heads/no-pr", zeroOID()), offlinePath)
	elapsed := time.Since(started)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("push-tier with no gh on PATH took %s, want well under 5s (gh must fail fast via exec.LookPath, never block)", elapsed)
	}
}
