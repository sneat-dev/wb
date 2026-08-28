package archiveprune

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fixture is one local clone under a temp projects root, backed by a real
// local bare "remote" so ls-remote/push/fetch all work without any network
// access.
type fixture struct {
	t            *testing.T
	projectsRoot string
	owner, name  string
	canonical    string
	remote       string
}

func newFixture(t *testing.T, owner, name string) *fixture {
	t.Helper()
	projectsRoot := t.TempDir()
	remotesRoot := t.TempDir()

	seed := filepath.Join(remotesRoot, owner, name+"-seed")
	mustMkdirAll(t, seed)
	run(t, seed, "git", "init", "-q", "-b", "main")
	run(t, seed, "git", "config", "user.email", "wb@example.test")
	run(t, seed, "git", "config", "user.name", "WB Test")
	mustWriteFile(t, filepath.Join(seed, "README.md"), owner+"/"+name+"\n")
	run(t, seed, "git", "add", ".")
	run(t, seed, "git", "commit", "-qm", "init")

	remote := filepath.Join(remotesRoot, owner, name+".git")
	run(t, remotesRoot, "git", "clone", "-q", "--bare", seed, remote)

	canonical := filepath.Join(projectsRoot, owner, name)
	mustMkdirAll(t, filepath.Dir(canonical))
	run(t, filepath.Dir(canonical), "git", "clone", "-q", remote, canonical)
	run(t, canonical, "git", "config", "user.email", "wb@example.test")
	run(t, canonical, "git", "config", "user.name", "WB Test")

	return &fixture{t: t, projectsRoot: projectsRoot, owner: owner, name: name, canonical: canonical, remote: remote}
}

func (f *fixture) slug() string { return f.owner + "/" + f.name }

// installFakeGh writes a fake `gh` on PATH that answers `gh repo view <slug>
// --json isArchived --jq .isArchived` with stdout, or fails when ok is false.
func (f *fixture) installFakeGh(stdout string, ok bool) {
	f.t.Helper()
	binDir := f.t.TempDir()
	script := filepath.Join(binDir, "gh")
	exit := "0"
	if !ok {
		exit = "1"
	}
	content := "#!/bin/sh\nset -eu\n" +
		"if [ \"$1 $2 $4 $5 $6\" != \"repo view --json isArchived --jq\" ]; then\n" +
		"  echo \"unexpected gh command: $*\" >&2\n  exit 2\nfi\n" +
		"printf '%s\\n' '" + stdout + "'\nexit " + exit + "\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		f.t.Fatal(err)
	}
	f.t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func (f *fixture) archived()          { f.installFakeGh("true", true) }
func (f *fixture) notArchived()       { f.installFakeGh("false", true) }
func (f *fixture) githubUnreachable() { f.installFakeGh("", false) }

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=wb-test", "GIT_AUTHOR_EMAIL=wb-test@example.test",
		"GIT_COMMITTER_NAME=wb-test", "GIT_COMMITTER_EMAIL=wb-test@example.test",
		"HOME="+os.Getenv("HOME"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

// isolateWBHome points WB_HOME at a fresh empty directory so a claim-scan test
// never sees this machine's real fleet state.
func isolateWBHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("WB_HOME", home)
	return home
}

func cleanOne(ctx context.Context, t *testing.T, f *fixture, apply bool) Result {
	t.Helper()
	outcome, err := Clean(ctx, Options{ProjectsRoot: f.projectsRoot, Apply: apply})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 1 {
		t.Fatalf("Clean() returned %d results, want 1: %+v", len(outcome.Results), outcome.Results)
	}
	return outcome.Results[0]
}

// --- Happy path -------------------------------------------------------

func TestClean_DeletesCleanArchivedClone(t *testing.T) {
	isolateWBHome(t)
	f := newFixture(t, "acme", "widgets")
	f.archived()

	result := cleanOne(context.Background(), t, f, true)
	if !result.Eligible || !result.Applied {
		t.Fatalf("clean archived clone was not deleted: %+v", result)
	}
	if _, err := os.Stat(f.canonical); !os.IsNotExist(err) {
		t.Fatal("canonical clone still exists on disk after apply")
	}
}

func TestClean_DryRunPlansButNeverDeletes(t *testing.T) {
	isolateWBHome(t)
	f := newFixture(t, "acme", "widgets")
	f.archived()

	result := cleanOne(context.Background(), t, f, false)
	if !result.Eligible {
		t.Fatalf("dry-run should still report eligible: %+v", result)
	}
	if result.Applied {
		t.Fatal("dry-run (Apply=false) must never delete")
	}
	if _, err := os.Stat(f.canonical); err != nil {
		t.Fatal("dry-run deleted the clone")
	}
}

// --- Dangerous cases: each must be refused -----------------------------

// A clone with an unpushed commit on a branch that is not checked out is the
// scenario that cost this fleet 25 commits in one incident; a check that only
// looks at HEAD would miss it entirely.
func TestClean_RefusesUnpushedCommitOnNonCheckedOutBranch(t *testing.T) {
	isolateWBHome(t)
	f := newFixture(t, "acme", "widgets")
	f.archived()

	run(t, f.canonical, "git", "checkout", "-qb", "side")
	mustWriteFile(t, filepath.Join(f.canonical, "side.txt"), "side work\n")
	run(t, f.canonical, "git", "add", ".")
	run(t, f.canonical, "git", "commit", "-qm", "unpushed side work")
	run(t, f.canonical, "git", "checkout", "-q", "main")

	result := cleanOne(context.Background(), t, f, true)
	if result.Eligible || result.Applied {
		t.Fatalf("clone with unpushed commit on non-checked-out branch was not refused: %+v", result)
	}
	if !strings.Contains(result.Reason, "side") {
		t.Errorf("reason does not name the offending branch: %q", result.Reason)
	}
	if _, err := os.Stat(f.canonical); err != nil {
		t.Fatal("clone was deleted despite unpushed work")
	}
}

// Untracked files never show up in `git diff` and nearly cost a lane ~2,400
// lines of real work; the safety check must catch them explicitly.
func TestClean_RefusesUntrackedFiles(t *testing.T) {
	isolateWBHome(t)
	f := newFixture(t, "acme", "widgets")
	f.archived()

	mustWriteFile(t, filepath.Join(f.canonical, "untracked.txt"), "never added\n")

	result := cleanOne(context.Background(), t, f, true)
	if result.Eligible || result.Applied {
		t.Fatalf("clone with only untracked files was not refused: %+v", result)
	}
	if !strings.Contains(result.Reason, "untracked") {
		t.Errorf("reason does not mention untracked files: %q", result.Reason)
	}
}

// The stash stack is repo-global across worktrees; a blind drop is
// unrecoverable, so a stash must block deletion outright.
func TestClean_RefusesStash(t *testing.T) {
	isolateWBHome(t)
	f := newFixture(t, "acme", "widgets")
	f.archived()

	mustWriteFile(t, filepath.Join(f.canonical, "README.md"), "changed\n")
	run(t, f.canonical, "git", "stash", "-q")

	result := cleanOne(context.Background(), t, f, true)
	if result.Eligible || result.Applied {
		t.Fatalf("clone with a stash was not refused: %+v", result)
	}
	if !strings.Contains(result.Reason, "stash") {
		t.Errorf("reason does not mention the stash: %q", result.Reason)
	}
}

// A linked worktree shares this clone's object storage; removing the
// canonical directory would break or orphan it.
func TestClean_RefusesLinkedWorktree(t *testing.T) {
	isolateWBHome(t)
	f := newFixture(t, "acme", "widgets")
	f.archived()

	worktreePath := filepath.Join(f.t.TempDir(), "linked")
	run(t, f.canonical, "git", "worktree", "add", "-q", "-b", "task", worktreePath, "main")

	result := cleanOne(context.Background(), t, f, true)
	if result.Eligible || result.Applied {
		t.Fatalf("clone with a linked worktree was not refused: %+v", result)
	}
	if !strings.Contains(result.Reason, "linked worktree") {
		t.Errorf("reason does not mention the linked worktree: %q", result.Reason)
	}
	if _, err := os.Stat(f.canonical); err != nil {
		t.Fatal("canonical clone was deleted while a linked worktree still referenced it")
	}
}

// A repository that is not archived must never be touched, no matter how
// clean its clone is.
func TestClean_RefusesRepositoryThatIsNotArchived(t *testing.T) {
	isolateWBHome(t)
	f := newFixture(t, "acme", "widgets")
	f.notArchived()

	result := cleanOne(context.Background(), t, f, true)
	if result.Eligible || result.Applied {
		t.Fatalf("clone of a non-archived repository was not refused: %+v", result)
	}
	if !strings.Contains(result.Reason, "not archived") {
		t.Errorf("reason does not say not archived: %q", result.Reason)
	}
}

// When the live GitHub check itself cannot be completed, the clone must fail
// closed rather than be treated as safe.
func TestClean_RefusesWhenGitHubCheckFails(t *testing.T) {
	isolateWBHome(t)
	f := newFixture(t, "acme", "widgets")
	f.githubUnreachable()

	result := cleanOne(context.Background(), t, f, true)
	if result.Eligible || result.Applied {
		t.Fatalf("clone was treated as deletable despite a failed GitHub check: %+v", result)
	}
	if !strings.Contains(result.Reason, "could not confirm archived status") {
		t.Errorf("reason does not explain the failed check: %q", result.Reason)
	}
}

// A local-only branch has no counterpart on GitHub at all: deleting the clone
// would discard it even though none of its commits are individually
// "unpushed" by the ordinary definition (they may be identical to history
// reachable elsewhere).
func TestClean_RefusesLocalOnlyBranch(t *testing.T) {
	isolateWBHome(t)
	f := newFixture(t, "acme", "widgets")
	f.archived()

	run(t, f.canonical, "git", "branch", "local-only")

	result := cleanOne(context.Background(), t, f, true)
	if result.Eligible || result.Applied {
		t.Fatalf("clone with a local-only branch was not refused: %+v", result)
	}
	if !strings.Contains(result.Reason, "local-only") {
		t.Errorf("reason does not mention the local-only branch: %q", result.Reason)
	}
}

// A tag that was never pushed is likewise invisible to GitHub and must block
// deletion.
func TestClean_RefusesUnpushedTag(t *testing.T) {
	isolateWBHome(t)
	f := newFixture(t, "acme", "widgets")
	f.archived()

	run(t, f.canonical, "git", "tag", "v0.0.1-local")

	result := cleanOne(context.Background(), t, f, true)
	if result.Eligible || result.Applied {
		t.Fatalf("clone with an unpushed tag was not refused: %+v", result)
	}
	if !strings.Contains(result.Reason, "v0.0.1-local") {
		t.Errorf("reason does not mention the unpushed tag: %q", result.Reason)
	}
}

// A non-terminal Work Log claim means WB or its operator still considers a
// task against this repository open, even if its worktree directory no
// longer exists.
func TestClean_RefusesNonTerminalWorkLogClaim(t *testing.T) {
	home := isolateWBHome(t)
	f := newFixture(t, "acme", "widgets")
	f.archived()

	claimDir := filepath.Join(home, "worklogs", "some-task", "runs", "run-1", "claims")
	mustMkdirAll(t, claimDir)
	claim := map[string]any{
		"claim_id":   "claim",
		"repository": f.slug(),
		"task":       "some-task",
		"worktree":   "/wherever/it/was",
		"lifecycle":  "active",
	}
	raw, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(claimDir, "claim.json"), string(raw))

	result := cleanOne(context.Background(), t, f, true)
	if result.Eligible || result.Applied {
		t.Fatalf("clone with a non-terminal Work Log claim was not refused: %+v", result)
	}
	if !strings.Contains(result.Reason, "some-task") {
		t.Errorf("reason does not name the referencing task: %q", result.Reason)
	}
}

// A terminal claim for the same repository must not block anything: the task
// is finished, and every other check still has to pass on its own.
func TestClean_TerminalClaimDoesNotBlock(t *testing.T) {
	home := isolateWBHome(t)
	f := newFixture(t, "acme", "widgets")
	f.archived()

	claimDir := filepath.Join(home, "worklogs", "some-task", "runs", "run-1", "claims")
	mustMkdirAll(t, claimDir)
	claim := map[string]any{
		"claim_id":   "claim",
		"repository": f.slug(),
		"task":       "some-task",
		"worktree":   "/wherever/it/was",
		"lifecycle":  "terminal",
	}
	raw, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(claimDir, "claim.json"), string(raw))

	result := cleanOne(context.Background(), t, f, true)
	if !result.Eligible || !result.Applied {
		t.Fatalf("terminal claim incorrectly blocked deletion: %+v", result)
	}
}

func TestClean_TerminalSiblingSealOverridesStaleClaimLifecycle(t *testing.T) {
	home := isolateWBHome(t)
	f := newFixture(t, "acme", "widgets")
	f.archived()
	runDir := filepath.Join(home, "worklogs", "some-task", "runs", "run-1")
	if err := os.MkdirAll(filepath.Join(runDir, "claims"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(runDir, "terminals"), 0o700); err != nil {
		t.Fatal(err)
	}
	claimID := "claim-123"
	claim := map[string]any{"claim_id": claimID, "repository": f.slug(), "task": "some-task", "worktree": "/gone", "lifecycle": "active"}
	terminal := map[string]any{"claim_id": claimID, "repository": f.slug(), "task": "some-task", "worktree": "/gone", "lifecycle": "terminal", "worktree_disposition": "discarded"}
	for path, value := range map[string]map[string]any{
		filepath.Join(runDir, "claims", claimID+".json"):    claim,
		filepath.Join(runDir, "terminals", claimID+".json"): terminal,
	} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		mustWriteFile(t, path, string(raw))
	}

	result := cleanOne(context.Background(), t, f, true)
	if !result.Eligible || !result.Applied {
		t.Fatalf("terminal sibling seal incorrectly blocked deletion: %+v", result)
	}
}

func TestNonTerminalClaimsRefusesMalformedOrMismatchedTerminalSeal(t *testing.T) {
	home := isolateWBHome(t)
	claimDir := filepath.Join(home, "worklogs", "some-task", "runs", "run-1", "claims")
	terminalDir := filepath.Join(home, "worklogs", "some-task", "runs", "run-1", "terminals")
	if err := os.MkdirAll(claimDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(terminalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	claimID := "claim-456"
	claim := map[string]any{"claim_id": claimID, "repository": "acme/widgets", "task": "some-task", "worktree": "/gone", "lifecycle": "active"}
	raw, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(claimDir, claimID+".json"), string(raw))
	terminalPath := filepath.Join(terminalDir, claimID+".json")
	mustWriteFile(t, terminalPath, "{")
	if _, err := nonTerminalClaims(t.TempDir(), "acme/widgets"); err == nil || !strings.Contains(err.Error(), "parse terminal seal") {
		t.Fatalf("malformed sibling terminal seal error = %v", err)
	}
	mismatch := map[string]any{"claim_id": claimID, "repository": "acme/other", "task": "some-task", "worktree": "/gone", "lifecycle": "terminal"}
	raw, err = json.Marshal(mismatch)
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, terminalPath, string(raw))
	if _, err := nonTerminalClaims(t.TempDir(), "acme/widgets"); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched sibling terminal seal error = %v", err)
	}
}

// A repository explicitly marked wb.skip-sync is left alone even when
// archived and otherwise clean.
func TestClean_RefusesSkipSyncMarkedRepo(t *testing.T) {
	isolateWBHome(t)
	f := newFixture(t, "acme", "widgets")
	f.archived()
	run(t, f.canonical, "git", "config", "--local", "wb.skip-sync", "true")

	result := cleanOne(context.Background(), t, f, true)
	if result.Eligible || result.Applied {
		t.Fatalf("skip-sync marked clone was not refused: %+v", result)
	}
	if !strings.Contains(result.Reason, "skip-sync") {
		t.Errorf("reason does not mention wb.skip-sync: %q", result.Reason)
	}
}

// One repository's GitHub archived-status check failing must not abort
// evaluation of the rest of the fleet, and must not itself become eligible.
func TestClean_OneGitHubCheckFailureDoesNotAbortTheSweep(t *testing.T) {
	isolateWBHome(t)
	good := newFixture(t, "acme", "widgets")
	bad := newFixtureIn(t, good.projectsRoot, "acme", "gadgets")
	installMixedFakeGh(t, bad.slug())

	outcome, err := Clean(context.Background(), Options{ProjectsRoot: good.projectsRoot, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 2 {
		t.Fatalf("expected 2 results, got %d: %+v", len(outcome.Results), outcome.Results)
	}
	byRepo := map[string]Result{}
	for _, result := range outcome.Results {
		byRepo[result.Repository] = result
	}
	if !byRepo[good.slug()].Applied {
		t.Errorf("the healthy repository was not deleted: %+v", byRepo[good.slug()])
	}
	if byRepo[bad.slug()].Eligible || byRepo[bad.slug()].Applied {
		t.Errorf("the repository whose GitHub check failed was treated as deletable: %+v", byRepo[bad.slug()])
	}
	if _, err := os.Stat(bad.canonical); err != nil {
		t.Fatal("clone whose GitHub check failed was deleted")
	}
}

// installMixedFakeGh answers `gh repo view <slug> ...` with archived=true for
// every slug except failSlug, which fails the command entirely.
func installMixedFakeGh(t *testing.T, failSlug string) {
	t.Helper()
	binDir := t.TempDir()
	script := filepath.Join(binDir, "gh")
	content := "#!/bin/sh\nset -eu\n" +
		"if [ \"$3\" = \"" + failSlug + "\" ]; then exit 1; fi\n" +
		"printf 'true\\n'\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// newFixtureIn creates a second clone inside an existing projects root,
// reusing its own independent remote.
func newFixtureIn(t *testing.T, projectsRoot, owner, name string) *fixture {
	t.Helper()
	remotesRoot := t.TempDir()
	seed := filepath.Join(remotesRoot, name+"-seed")
	mustMkdirAll(t, seed)
	run(t, seed, "git", "init", "-q", "-b", "main")
	run(t, seed, "git", "config", "user.email", "wb@example.test")
	run(t, seed, "git", "config", "user.name", "WB Test")
	mustWriteFile(t, filepath.Join(seed, "README.md"), owner+"/"+name+"\n")
	run(t, seed, "git", "add", ".")
	run(t, seed, "git", "commit", "-qm", "init")

	remote := filepath.Join(remotesRoot, name+".git")
	run(t, remotesRoot, "git", "clone", "-q", "--bare", seed, remote)

	canonical := filepath.Join(projectsRoot, owner, name)
	mustMkdirAll(t, filepath.Dir(canonical))
	run(t, filepath.Dir(canonical), "git", "clone", "-q", remote, canonical)

	return &fixture{t: t, projectsRoot: projectsRoot, owner: owner, name: name, canonical: canonical, remote: remote}
}
