package orchestrate

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/gitcli"
	"github.com/sneat-dev/wb/internal/runner"
)

func TestPublishedRecoveryNativeAdvancePreservesPublicationState(t *testing.T) {
	t.Parallel()
	f := newConflictRecoveryFixture(t)
	receipt := f.receipt
	receipt.PublishedCandidateSHA = receipt.Candidate.SHA
	receipt.PullRequest = "https://example.test/acme/app/pull/41"
	receipt.Status = WorktreeMergeChecksFailed
	receipt.UpdatedAt = time.Unix(1700000000, 0).UTC()
	receipt.LocalSync = "prior local synchronization note"
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", receipt.PublishedCandidateSHA+":refs/heads/"+receipt.Candidate.Branch)
	before := receipt
	changed, err := advancePublishedWorktreeMergeCandidate(t.Context(), defaultGit, defaultRunner, &receipt)
	expected := before
	expected.Candidate.SHA = f.head
	if err != nil || !changed || !reflect.DeepEqual(receipt, expected) {
		t.Fatalf("native published advance changed=%v error=%v receipt=%+v expected=%+v", changed, err, receipt, expected)
	}
	// Equal HEAD clears only a stale convenience note, never durable publication.
	expected.LocalSync = ""
	changed, err = advancePublishedWorktreeMergeCandidate(t.Context(), defaultGit, defaultRunner, &receipt)
	if err != nil || changed || !reflect.DeepEqual(receipt, expected) {
		t.Fatalf("equal native head changed=%v error=%v receipt=%+v", changed, err, receipt)
	}
}

// publishedObservedRunner records exact dispatch while every result remains
// an actual native Git observation. Only the named error case refuses a read.
type publishedObservedRunner struct {
	runner.Runner
	reads               []string
	faultDir, faultArgs string
	fault               error
}

func (r *publishedObservedRunner) RunOpts(ctx context.Context, dir string, options runner.RunOptions, name string, args ...string) (runner.Result, error) {
	r.reads = append(r.reads, dir+"\x00"+name+" "+strings.Join(args, " "))
	if dir == r.faultDir && name == "git" && strings.Join(args, " ") == r.faultArgs {
		return runner.Result{}, r.fault
	}
	return r.Runner.RunOpts(ctx, dir, options, name, args...)
}

func TestPublishedRecoveryRoutesBothAncestryReadsThroughSuppliedRunner(t *testing.T) {
	t.Parallel()
	f := newConflictRecoveryFixture(t)
	receipt := f.receipt
	receipt.PublishedCandidateSHA = receipt.Candidate.SHA
	run := &publishedObservedRunner{Runner: runner.New()}
	changed, err := advancePublishedWorktreeMergeCandidate(t.Context(), gitcli.New(run), run, &receipt)
	want := []string{
		f.receipt.Candidate.Worktree + "\x00git rev-parse --verify HEAD^{commit}",
		f.receipt.Candidate.Worktree + "\x00git merge-base " + f.receipt.Candidate.SHA + " " + f.receipt.Candidate.SHA,
		f.receipt.Candidate.Worktree + "\x00git merge-base " + f.receipt.Candidate.SHA + " " + f.head,
	}
	if err != nil || !changed || receipt.Candidate.SHA != f.head || !reflect.DeepEqual(run.reads, want) {
		t.Fatalf("supplied native reads=%q want=%q changed=%v error=%v", run.reads, want, changed, err)
	}
	sentinel := errors.New("controlled exact published native read refused")
	for _, tc := range []struct{ name, args, prefix string }{
		{"HEAD", "rev-parse --verify HEAD^{commit}", "read published candidate HEAD"},
		{"published predecessor", "merge-base " + f.receipt.Candidate.SHA + " " + f.receipt.Candidate.SHA, "verify published candidate predecessor"},
		{"candidate descendant", "merge-base " + f.receipt.Candidate.SHA + " " + f.head, "verify published candidate descendant"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			original := f.receipt
			original.PublishedCandidateSHA = original.Candidate.SHA
			original.LocalSync = "keep prior note"
			before := original
			run := &publishedObservedRunner{Runner: runner.New(), faultDir: original.Candidate.Worktree, faultArgs: tc.args, fault: sentinel}
			changed, err := advancePublishedWorktreeMergeCandidate(t.Context(), gitcli.New(run), run, &original)
			if changed || !errors.Is(err, sentinel) || !strings.HasPrefix(err.Error(), tc.prefix+": ") || !reflect.DeepEqual(original, before) {
				t.Fatalf("named read %s changed=%v error=%v receipt=%+v", tc.name, changed, err, original)
			}
			want := original.Candidate.Worktree + "\x00git " + tc.args
			if len(run.reads) == 0 || run.reads[len(run.reads)-1] != want {
				t.Fatalf("named read not delivered to supplied Runner: %q", run.reads)
			}
		})
	}
}

func TestPublishedRecoveryNativeDAGRefusalsRetainReceiptAndNote(t *testing.T) {
	t.Parallel()
	f := newConflictRecoveryFixture(t)
	extra := createMergeSource(t, f.engine, "published-unmerged", "feature/published-unmerged", "extra.txt", "extra\n")
	unrelated := strings.TrimSpace(runEngineGit(t, extra.WorktreeDir, "rev-parse", "HEAD"))
	for _, tc := range []struct{ name, published, recorded, want string }{
		{"no published predecessor", "", f.receipt.Candidate.SHA, "without an exact published predecessor"},
		{"published does not precede recorded", f.receipt.Sources[0].SHA, f.receipt.Candidate.SHA, "does not descend from published candidate"},
		{"HEAD does not descend from recorded", f.receipt.TargetSHA, unrelated, "is not a descendant of published candidate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			receipt := f.receipt
			receipt.Candidate.SHA = tc.recorded
			receipt.PublishedCandidateSHA = tc.published
			receipt.LocalSync = "  actual earlier sync failure  "
			before := receipt
			changed, err := advancePublishedWorktreeMergeCandidate(t.Context(), defaultGit, defaultRunner, &receipt)
			if changed || err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "(actual earlier sync failure)") || !reflect.DeepEqual(receipt, before) {
				t.Fatalf("native DAG refusal=%s changed=%v error=%v receipt=%+v", tc.name, changed, err, receipt)
			}
		})
	}
}

func TestPublishedRecoveryRecordedHistoryRepairsNativeLocalCheckout(t *testing.T) {
	t.Parallel()
	f := newConflictRecoveryFixture(t)
	previous := f.receipt.Candidate.SHA
	receipt := f.receipt
	receipt.Candidate.SHA = f.head
	receipt.PublishedCandidateSHA = previous
	receipt.TargetRefreshes = []WorktreeMergeTargetRefresh{{PreviousCandidateSHA: previous, NewCandidateSHA: f.head}}
	receipt.LocalSync = "stale old synchronization note"
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", f.head+":refs/heads/"+receipt.Candidate.Branch)
	runEngineGit(t, receipt.Candidate.Worktree, "reset", "--hard", previous)
	before := receipt
	changed, err := advancePublishedWorktreeMergeCandidate(t.Context(), defaultGit, defaultRunner, &receipt)
	expected := before
	expected.LocalSync = "fast-forwarded worktree " + receipt.Candidate.Worktree + " to " + shortMergeRevision(f.head)
	if changed || err != nil || !reflect.DeepEqual(receipt, expected) || strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD")) != f.head {
		t.Fatalf("actual recorded-history repair changed=%v error=%v receipt=%+v", changed, err, receipt)
	}
	// A later ordinary resume sees the recorded HEAD and clears the prior note.
	changed, err = advancePublishedWorktreeMergeCandidate(t.Context(), defaultGit, defaultRunner, &receipt)
	expected.LocalSync = ""
	if changed || err != nil || !reflect.DeepEqual(receipt, expected) {
		t.Fatalf("repaired equal-head resume changed=%v error=%v receipt=%+v", changed, err, receipt)
	}
	// An actual dirty private checkout retains its data and records the refusal.
	runEngineGit(t, receipt.Candidate.Worktree, "reset", "--hard", previous)
	writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "dirty.txt"), "retain native uncommitted data\n")
	changed, err = advancePublishedWorktreeMergeCandidate(t.Context(), defaultGit, defaultRunner, &receipt)
	if changed || err != nil || !strings.Contains(receipt.LocalSync, "uncommitted changes") || strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD")) != previous {
		t.Fatalf("dirty history repair changed=%v error=%v note=%q", changed, err, receipt.LocalSync)
	}
}

func TestPublishedRecoveryDriftErrorsPreserveCauseAndTrimNotes(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("original drift")
	for _, tc := range []struct{ note, want string }{{"", "original drift"}, {" \t ", "original drift"}, {"  prior local failure \n", "original drift (prior local failure)"}} {
		t.Run(tc.want+tc.note, func(t *testing.T) {
			t.Parallel()
			receipt := WorktreeMergeReceipt{LocalSync: tc.note}
			err := worktreeMergeDriftError(&receipt, sentinel)
			if !errors.Is(err, sentinel) || err.Error() != tc.want || receipt.LocalSync != tc.note {
				t.Fatalf("drift identity=%v note=%q", err, receipt.LocalSync)
			}
			if strings.TrimSpace(tc.note) == "" && err != sentinel {
				t.Fatal("empty note replaced error identity")
			}
		})
	}
}

// publishedPrivateDirectoryRunner supplies an explicit private process cwd
// only when the command requests its ordinary empty-directory default. Every
// HEAD result is native; this contract never substitutes commit/custody facts.
type publishedPrivateDirectoryRunner struct {
	runner.Runner
	directory string
	requested []string
}

func (r *publishedPrivateDirectoryRunner) RunOpts(ctx context.Context, dir string, options runner.RunOptions, name string, args ...string) (runner.Result, error) {
	r.requested = append(r.requested, dir+"\x00"+name+" "+strings.Join(args, " "))
	if dir == "" {
		dir = r.directory
	}
	return r.Runner.RunOpts(ctx, dir, options, name, args...)
}

func TestPublishedRecoveryHistoryWithoutLocalCheckoutRetainsPriorNote(t *testing.T) {
	t.Parallel()
	f := newConflictRecoveryFixture(t)
	previous := f.receipt.Candidate.SHA
	receipt := f.receipt
	receipt.Candidate.SHA = f.head
	receipt.PublishedCandidateSHA = previous
	receipt.TargetRefreshes = []WorktreeMergeTargetRefresh{{PreviousCandidateSHA: previous, NewCandidateSHA: f.head}}
	receipt.LocalSync = "retain prior local synchronization failure"
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", f.head+":refs/heads/"+receipt.Candidate.Branch)
	runEngineGit(t, receipt.Candidate.Worktree, "reset", "--hard", previous)
	// An absent local checkout is a presentation/convenience contract. Bind
	// the empty-directory HEAD command to an actual private Git observation,
	// while the existing fast-forward owner correctly has no checkout to repair.
	receipt.Candidate.Worktree = ""
	before := receipt
	run := &publishedPrivateDirectoryRunner{Runner: runner.New(), directory: f.receipt.Candidate.Worktree}
	changed, err := advancePublishedWorktreeMergeCandidate(t.Context(), gitcli.New(run), run, &receipt)
	if changed || err != nil || !reflect.DeepEqual(receipt, before) {
		t.Fatalf("no local repair note changed=%v error=%v receipt=%+v", changed, err, receipt)
	}
	if want := []string{"\x00git rev-parse --verify HEAD^{commit}"}; !reflect.DeepEqual(run.requested, want) {
		t.Fatalf("blank-path contract ran unrelated operations: %q", run.requested)
	}
	if head := strings.TrimSpace(runEngineGit(t, f.receipt.Candidate.Worktree, "rev-parse", "HEAD")); head != previous {
		t.Fatalf("private native observation checkout changed to %s", head)
	}
}
