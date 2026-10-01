//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
)

// failRepositoryTransferGitOnce retains the real Git runner for every command
// except one named ordinary Git failure at the selected transfer phase.
type failRepositoryTransferGitOnce struct {
	runner.Runner
	directory string
	command   []string
	hit       bool
}

func (fault *failRepositoryTransferGitOnce) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if !fault.hit && name == "git" && dir == fault.directory && len(args) >= 2+len(fault.command) &&
		slices.Equal(args[2:2+len(fault.command)], fault.command) {
		fault.hit = true
		return runner.Result{}, errors.New("selected transfer Git failure")
	}
	return fault.Runner.RunOpts(ctx, dir, opts, name, args...)
}

//nolint:paralleltest // the native transfer fixture sets process-wide WB and Git environment.
func TestE2ERepositoryTransferGitFailuresPreserveSourceAndWithholdReceipt(t *testing.T) {
	for _, tc := range []struct {
		name, location, want string
		command              []string
	}{
		{"source status", "source", "inspect worktree", []string{"status", "--porcelain=v1"}},
		{"first destination origin rewrite", "destination", "selected transfer Git failure", []string{"remote", "set-url", "origin"}},
		{"destination push origin rewrite", "destination", "selected transfer Git failure", []string{"remote", "set-url", "--push", "origin"}},
		{"moved worktree repair", "destination", "selected transfer Git failure", []string{"worktree", "repair"}},
		{"destination default fetch", "destination", "fetch destination default branch", []string{"fetch", "--prune", "origin"}},
		{"fetched head resolution", "destination", "fetched destination default branch does not match", []string{"rev-parse", "refs/remotes/origin/main"}},
	} {
		//nolint:paralleltest // each native Git fixture sets process-wide WB and Git environment.
		t.Run(tc.name, func(t *testing.T) {
			fixture := newRepositoryTransferFixture(t)
			fixture.moveRemote(t)
			originalHead := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
			remoteHead := gitTestOutput(t, fixture.newRemote, "rev-parse", "refs/heads/main")
			options := RepositoryRelocateOptions{ProjectsRoot: fixture.projectsRoot, SourceRepository: "acme/app",
				DestinationRepository: "newco/renamed", RemoteURL: fixture.newRemote, DefaultBranch: "main", Apply: true}
			path := fixture.destination
			if tc.location == "source" {
				path = fixture.canonical
			}
			fault := &failRepositoryTransferGitOnce{Runner: runner.New(), directory: path, command: tc.command}
			result, err := RelocateRepository(withGitRunner(context.Background(), fault), options)
			if !fault.hit || err == nil || !strings.Contains(err.Error(), tc.want) || result.Applied || len(result.ReceiptPaths) != 0 {
				t.Fatalf("%s refusal = %#v, %v; selected failure reached=%t", tc.name, result, err, fault.hit)
			}
			if got := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD"); got != originalHead {
				t.Fatalf("refusal changed source checkout HEAD: %s", got)
			}
			if got := gitTestOutput(t, fixture.newRemote, "rev-parse", "refs/heads/main"); got != remoteHead {
				t.Fatalf("refusal changed destination remote ref: %s", got)
			}
			if _, err := os.Lstat(fixture.destination); !os.IsNotExist(err) {
				t.Fatalf("failed transfer retained a destination checkout: %v", err)
			}
		})
	}
}

//nolint:paralleltest // Create and the native transfer fixture set process-wide WB and Git environment.
func TestE2ERepositoryTransferPreReceiptFailureKeepsClaimAndIntent(t *testing.T) {
	fixture := newRepositoryTransferFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "transfer-pre-receipt", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceWorktree := created[0].WorktreeDir
	claim, _, _, err := activeWorkLogClaim(fixture.home, sourceWorktree)
	if err != nil {
		t.Fatal(err)
	}
	claimPath := filepath.Join(fixture.home, "worklogs", claim.EffortID, "runs", claim.RunID, "claims", claim.ClaimID+".json")
	claimBytes, err := os.ReadFile(claimPath)
	if err != nil {
		t.Fatal(err)
	}
	sourceHead := gitTestOutput(t, sourceWorktree, "rev-parse", "HEAD")
	fixture.moveRemote(t)
	remoteHead := gitTestOutput(t, fixture.newRemote, "rev-parse", "refs/heads/main")
	options := RepositoryRelocateOptions{ProjectsRoot: fixture.projectsRoot, SourceRepository: "acme/app",
		DestinationRepository: "newco/renamed", RemoteURL: fixture.newRemote, DefaultBranch: "main", Apply: true}
	fault := &failRepositoryTransferGitOnce{Runner: runner.New(), directory: fixture.destination, command: []string{"fetch", "--prune", "origin"}}
	result, err := RelocateRepository(withGitRunner(context.Background(), fault), options)
	if !fault.hit || err == nil || !strings.Contains(err.Error(), "fetch destination default branch") || result.Applied || len(result.ReceiptPaths) != 0 {
		t.Fatalf("pre-receipt fetch failure = %#v, %v; selected failure reached=%t", result, err, fault.hit)
	}
	if got := gitTestOutput(t, sourceWorktree, "rev-parse", "HEAD"); got != sourceHead {
		t.Fatalf("rollback changed claimed source HEAD: %s", got)
	}
	if got := gitTestOutput(t, fixture.newRemote, "rev-parse", "refs/heads/main"); got != remoteHead {
		t.Fatalf("rollback changed destination remote ref: %s", got)
	}
	if _, err := os.Lstat(fixture.destination); !os.IsNotExist(err) {
		t.Fatalf("pre-receipt failure retained destination checkout: %v", err)
	}
	retained, _, _, err := activeWorkLogClaim(fixture.home, sourceWorktree)
	if err != nil || retained.ClaimID != claim.ClaimID {
		t.Fatalf("rollback lost original active claim: %#v, %v", retained, err)
	}
	if after, err := os.ReadFile(claimPath); err != nil || string(after) != string(claimBytes) {
		t.Fatalf("rollback changed immutable claim bytes: %q, %v", after, err)
	}
	run, runPath, err := openWorkLogRun(fixture.home, claim.EffortID, claim.RunID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = run.Close() }()
	journal, err := openRelocationJournal(run, runPath, claim)
	if err != nil || len(journal.intents) != 1 || len(journal.receipts) != 0 {
		t.Fatalf("pre-receipt journal = intents %d, receipts %d, %v", len(journal.intents), len(journal.receipts), err)
	}
}

//nolint:paralleltest // the native transfer fixture sets process-wide WB and Git environment.
func TestE2ERepositoryTransferRefusesUnpublishedDestinationRefs(t *testing.T) {
	fixture := newRepositoryTransferFixture(t)
	fixture.moveRemote(t)
	fixture.cloneDestination(t)
	head := gitTestOutput(t, fixture.destination, "rev-parse", "HEAD")
	options := RepositoryRelocateOptions{ProjectsRoot: fixture.projectsRoot, SourceRepository: "acme/app",
		DestinationRepository: "newco/renamed", RemoteURL: fixture.newRemote, DefaultBranch: "main"}
	for _, tc := range []struct {
		name, want  string
		add, remove []string
	}{
		{"branch", "local-only or unpushed branch", []string{"branch", "unpublished"}, []string{"branch", "-D", "unpublished"}},
		{"tag", "local-only or changed tag", []string{"tag", "unpublished"}, []string{"tag", "-d", "unpublished"}},
	} {
		//nolint:paralleltest // cases mutate and restore the same destination Git refs.
		t.Run(tc.name, func(t *testing.T) {
			gitTest(t, fixture.destination, tc.add...)
			if reason := disposableDestinationReason(context.Background(), fixture.destination, options, head); !strings.Contains(reason, tc.want) {
				t.Fatalf("unpublished %s destination refusal = %q", tc.name, reason)
			}
			plan, err := RelocateRepository(context.Background(), options)
			if err != nil || plan.Eligible || !strings.Contains(plan.Reason, tc.want) {
				t.Fatalf("unpublished %s transfer plan = %#v, %v", tc.name, plan, err)
			}
			if got := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD"); got != head {
				t.Fatalf("refusal changed source HEAD: %s", got)
			}
			gitTest(t, fixture.destination, tc.remove...)
		})
	}
}

//nolint:paralleltest // the native Git tests share no path but invoke real Git process setup.
func TestE2ERepositoryTransferWorktreeInventoryRetainsUnbornHead(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	gitTest(t, root, "init", "--initial-branch=main", source)
	entries, err := repositoryRelocateWorktrees(context.Background(), source, destination)
	if err != nil || len(entries) != 1 || entries[0].source != source || entries[0].destination != destination || entries[0].head != "" {
		t.Fatalf("unborn worktree mapping = %#v, %v", entries, err)
	}
}

//nolint:paralleltest // the native transfer fixture sets process-wide WB and Git environment.
func TestE2ERepositoryTransferRefusesDanglingSourceHeadBeforeMove(t *testing.T) {
	fixture := newRepositoryTransferFixture(t)
	fixture.moveRemote(t)
	remoteHead := gitTestOutput(t, fixture.newRemote, "rev-parse", "refs/heads/main")
	brokenHead := strings.Repeat("a", 40) + "\n"
	if err := os.WriteFile(filepath.Join(fixture.canonical, ".git", "HEAD"), []byte(brokenHead), 0o600); err != nil {
		t.Fatal(err)
	}
	options := RepositoryRelocateOptions{ProjectsRoot: fixture.projectsRoot, SourceRepository: "acme/app",
		DestinationRepository: "newco/renamed", RemoteURL: fixture.newRemote, DefaultBranch: "main", Apply: true}
	result, err := RelocateRepository(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "inspect worktree") || result.Applied || len(result.ReceiptPaths) != 0 {
		t.Fatalf("dangling source HEAD transfer = %#v, %v", result, err)
	}
	if raw, err := os.ReadFile(filepath.Join(fixture.canonical, ".git", "HEAD")); err != nil || string(raw) != brokenHead {
		t.Fatalf("refusal changed invalid HEAD evidence: %q, %v", raw, err)
	}
	if got := gitTestOutput(t, fixture.newRemote, "rev-parse", "refs/heads/main"); got != remoteHead {
		t.Fatalf("refusal changed destination remote head: %s", got)
	}
	if _, err := os.Lstat(fixture.destination); !os.IsNotExist(err) {
		t.Fatalf("dangling source HEAD reached destination move: %v", err)
	}
}
