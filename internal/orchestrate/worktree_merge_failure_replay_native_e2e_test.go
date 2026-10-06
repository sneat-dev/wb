//go:build e2e

package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
)

// replayNativeReads observes the existing Git port; all positive outputs are
// native. Selected failures use the existing landedFailureOwnerRunner.
type replayNativeReads struct {
	runner.Runner
	path  string
	calls [][]string
}

func (r *replayNativeReads) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if dir == r.path && name == "git" {
		r.calls = append(r.calls, append([]string(nil), args...))
	}
	return r.Runner.RunOpts(ctx, dir, opts, name, args...)
}

func publishedFailureReplayFixture(t *testing.T) (conflictRecoveryFixture, WorktreeMergeReceipt) {
	t.Helper()
	f := newConflictRecoveryFixture(t)
	r := f.receipt
	r.Status = WorktreeMergeValidationFailed
	r.PullRequest = "https://example.test/pull/recorded"
	r.PublishedCandidateSHA = r.Candidate.SHA
	r.Candidate.SHA = f.head
	r.SourceRefreshes = []WorktreeMergeSourceRefresh{{RecordedAt: time.Now().UTC(), Sources: append([]WorktreeMergeSource(nil), r.Sources...)}}
	runEngineGit(t, r.Candidate.Worktree, "push", "origin", r.PublishedCandidateSHA+":refs/heads/"+r.Candidate.Branch)
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	return f, r
}

func TestE2EPublishedFailureReplayNativeRootsAndRefusals(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"native roots", "candidate missing", "source dirty", "target fetch", "blank root", "ancestry read", "remote read", "foreign root", "remote moved"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f, receipt := publishedFailureReplayFixture(t)
			before, err := os.ReadFile(receipt.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			claim, err := validateMergeAcknowledgementCandidate(t.Context(), f.engine.githubDir, receipt, receipt.Candidate)
			if err != nil {
				t.Fatal(err)
			}
			claimBefore, err := os.ReadFile(claim.ClaimPath)
			if err != nil {
				t.Fatal(err)
			}
			published := receipt.PublishedCandidateSHA
			want := ""
			sentinel := errors.New("owned replay " + kind)
			var fault *landedFailureOwnerRunner
			switch kind {
			case "candidate missing":
				receipt.Candidate.Worktree = filepath.Join(t.TempDir(), "absent")
				want = "guard candidate"
			case "source dirty":
				writeEngineFile(t, filepath.Join(receipt.Sources[0].Worktree, "dirty.txt"), "dirty\n")
				want = "changed during prepare"
			case "target fetch":
				fault = &landedFailureOwnerRunner{Runner: defaultRunner, path: receipt.Candidate.Worktree, args: []string{"fetch", "--no-tags", "origin", "+refs/heads/main:refs/remotes/origin/main"}, ordinal: 1, sentinel: sentinel}
			case "blank root":
				receipt.TargetSHA = ""
				want = "incomplete immutable ancestry root"
			case "ancestry read":
				fault = &landedFailureOwnerRunner{Runner: defaultRunner, path: receipt.Candidate.Worktree, args: []string{"merge-base", claim.BaseSHA, receipt.Candidate.SHA}, ordinal: 2, sentinel: sentinel}
			case "remote read":
				fault = &landedFailureOwnerRunner{Runner: defaultRunner, path: receipt.Candidate.Worktree, args: []string{"ls-remote", "--heads", "origin", "refs/heads/" + receipt.Candidate.Branch}, ordinal: 1, sentinel: sentinel}
			case "foreign root":
				runEngineGit(t, f.engine.canonical, "commit", "--allow-empty", "-m", "unintegrated native root")
				foreign := strings.TrimSpace(runEngineGit(t, f.engine.canonical, "rev-parse", "HEAD"))
				receipt.SourceRefreshes[0].Sources[0].SHA = foreign
				want = "does not contain immutable replay root " + foreign
			case "remote moved":
				runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", receipt.Candidate.SHA+":refs/heads/"+receipt.Candidate.Branch)
				published = receipt.Candidate.SHA
			}
			delegate := defaultRunner
			if fault != nil {
				delegate = fault
			}
			reads := &replayNativeReads{Runner: delegate, path: receipt.Candidate.Worktree}
			var ok bool
			if kind == "native roots" {
				// Cover the native default wrapper as well as its complete body.
				ok, err = isExactPublishedValidationFailureReplay(t.Context(), f.engine.githubDir, receipt, receipt.Sources)
				if err != nil || !ok {
					t.Fatalf("native default replay=%t, %v", ok, err)
				}
			}
			ok, err = isExactPublishedValidationFailureReplayWithRunner(t.Context(), reads, f.engine.githubDir, receipt, receipt.Sources)
			if fault != nil {
				if !errors.Is(err, sentinel) || ok || fault.seen != fault.ordinal {
					t.Fatalf("native %s replay=%t error=%v observed=%d", kind, ok, err, fault.seen)
				}
			} else if want != "" {
				if err == nil || ok || !strings.Contains(err.Error(), want) {
					t.Fatalf("native %s replay=%t error=%v", kind, ok, err)
				}
			} else if err != nil || ok != (kind == "native roots") {
				t.Fatalf("native %s replay=%t error=%v", kind, ok, err)
			}
			var roots []string
			remoteRead := false
			fetchRead := false
			for _, args := range reads.calls {
				if args[0] == "fetch" {
					fetchRead = true
				}
				if args[0] == "merge-base" {
					roots = append(roots, args[1])
					if args[1] == "" || args[2] != receipt.Candidate.SHA {
						t.Fatalf("invalid root query %q", args)
					}
				}
				if args[0] == "ls-remote" {
					remoteRead = true
				}
			}
			if kind == "native roots" || kind == "remote moved" || kind == "remote read" {
				wantRoots := []string{claim.BaseSHA, receipt.TargetSHA, receipt.TargetSHA, receipt.PublishedCandidateSHA, receipt.Sources[0].SHA, receipt.Sources[0].SHA}
				if !reflect.DeepEqual(roots, wantRoots) || !remoteRead {
					t.Fatalf("ordered duplicate roots lost: %v remote=%t", roots, remoteRead)
				}
			} else if remoteRead {
				t.Fatalf("%s failed proof still queried publication: %v", kind, reads.calls)
			}
			if (kind == "candidate missing" || kind == "source dirty") && (fetchRead || len(roots) != 0) {
				t.Fatalf("%s crossed proof stage order: %v", kind, reads.calls)
			}
			if kind == "blank root" && !reflect.DeepEqual(roots, []string{claim.BaseSHA}) {
				t.Fatalf("blank root queried or predecessors reordered: %v", roots)
			}
			if kind == "ancestry read" && !reflect.DeepEqual(roots, []string{claim.BaseSHA, receipt.TargetSHA}) {
				t.Fatalf("duplicate root refusal order lost: %v", roots)
			}
			after, readErr := os.ReadFile(receipt.ReceiptPath)
			currentClaim, claimErr := os.ReadFile(claim.ClaimPath)
			if readErr != nil || claimErr != nil || !reflect.DeepEqual(after, before) || !reflect.DeepEqual(currentClaim, claimBefore) {
				t.Fatalf("replay changed immutable evidence: %v %v", readErr, claimErr)
			}
			if actual := strings.TrimSpace(runEngineGit(t, f.engine.repository.CloneURL, "rev-parse", "refs/heads/"+receipt.Candidate.Branch)); actual != published {
				t.Fatalf("replay changed actual publication: %s != %s", actual, published)
			}
			if actual := strings.TrimSpace(runEngineGit(t, f.receipt.Candidate.Worktree, "rev-parse", "HEAD")); actual != f.head {
				t.Fatalf("replay moved native candidate %s", actual)
			}
		})
	}
}

func TestE2ELandedFailureSourceHeadNativeCustodyAndDescendants(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"native outputs", "guard missing", "linked identity", "HEAD read", "descendant read", "non-descendant", "dirty", "unallowed advance", "Work Log read", "claim identity", "preserved ancestry read"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f := newPrepareOwnerFixture(t)
			source := f.sources[0]
			receipt := f.receipt
			original := source.SHA
			remoteSource := strings.TrimSpace(runEngineGit(t, f.engine.repository.CloneURL, "for-each-ref", "--format=%(objectname)", "refs/heads/"+f.source.Branch))
			allowed := ""
			preserved := false
			want := ""
			sentinel := errors.New("owned source observation " + kind)
			var fault *landedFailureOwnerRunner
			switch kind {
			case "guard missing":
				source.Worktree = filepath.Join(t.TempDir(), "missing")
				want = "guard receipted source"
			case "linked identity":
				source.Worktree = f.engine.canonical
				source.Branch = "main"
				want = "no longer has its exact linked-worktree identity"
			case "HEAD read":
				fault = &landedFailureOwnerRunner{Runner: defaultRunner, path: source.Worktree, args: []string{"rev-parse", "--verify", "HEAD^{commit}"}, ordinal: 1, sentinel: sentinel}
				want = "read receipted source"
			case "descendant read", "unallowed advance":
				runEngineGit(t, source.Worktree, "commit", "--allow-empty", "-m", "native source descendant")
				head := strings.TrimSpace(runEngineGit(t, source.Worktree, "rev-parse", "HEAD"))
				if kind == "descendant read" {
					allowed = head
					fault = &landedFailureOwnerRunner{Runner: defaultRunner, path: source.Worktree, args: []string{"merge-base", source.SHA, head}, ordinal: 1, sentinel: sentinel}
					want = "verify receipted source descendant ancestry"
				} else {
					want = "does not match"
				}
			case "non-descendant":
				runEngineGit(t, source.Worktree, "reset", "--hard", receipt.TargetSHA)
				allowed = receipt.TargetSHA
				want = "is not a descendant of"
			case "dirty":
				writeEngineFile(t, filepath.Join(source.Worktree, "dirty.txt"), "dirty\n")
				want = "is not clean"
			case "Work Log read":
				prompts := filepath.Join(source.Worktree, ".wb", "local", "prompts")
				if err := os.RemoveAll(prompts); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(prompts, []byte("owned non-directory"), 0600); err != nil {
					t.Fatal(err)
				}
				want = "load receipted source Work Log"
			case "claim identity":
				source.Task = "different-task"
				want = "no matching active Work Log claim"
			case "preserved ancestry read":
				preserved = true
				fault = &landedFailureOwnerRunner{Runner: defaultRunner, path: source.Worktree, args: []string{"merge-base", source.SHA, source.SHA}, ordinal: 1, sentinel: sentinel}
				want = "was rewritten after landing"
			}
			expectedHead := strings.TrimSpace(runEngineGit(t, f.source.WorktreeDir, "rev-parse", "HEAD"))
			claimBefore, err := os.ReadFile(f.source.WorkLogPath)
			if err != nil {
				t.Fatal(err)
			}
			run := defaultRunner
			if fault != nil {
				run = fault
			}
			validated := "unmodified"
			err = validateLandedFailureAcknowledgementSourceHeadWithRunner(t.Context(), run, f.engine.githubDir, receipt, source, allowed, preserved, &validated)
			if kind == "native outputs" {
				if err != nil || validated != original {
					t.Fatalf("native exact source head=%q err=%v", validated, err)
				}
				if err := validateLandedFailureAcknowledgementSource(t.Context(), f.engine.githubDir, receipt, source, ""); err != nil {
					t.Fatalf("native exact wrapper: %v", err)
				}
				if err := validateLandedFailureAcknowledgementSourceHead(t.Context(), f.engine.githubDir, receipt, source, "", false, nil); err != nil {
					t.Fatalf("nil output changed proof: %v", err)
				}
				runEngineGit(t, source.Worktree, "commit", "--allow-empty", "-m", "native allowed source descendant")
				expectedHead = strings.TrimSpace(runEngineGit(t, source.Worktree, "rev-parse", "HEAD"))
				if err := validateLandedFailureAcknowledgementSource(t.Context(), f.engine.githubDir, receipt, source, expectedHead); err != nil {
					t.Fatalf("native allowed descendant wrapper: %v", err)
				}
				if err := validatePreservedLandedFailureAcknowledgementSource(t.Context(), f.engine.githubDir, receipt, source); err != nil {
					t.Fatalf("native preserved wrapper: %v", err)
				}
				if actual, err := validatePreservedLandedFailureAcknowledgementSourceWithHead(t.Context(), f.engine.githubDir, receipt, source); err != nil || actual != expectedHead {
					t.Fatalf("native preserved head=%q expected=%q err=%v", actual, expectedHead, err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), want) || validated != "unmodified" {
					t.Fatalf("native %s head=%q error=%v", kind, validated, err)
				}
				if fault != nil && (!errors.Is(err, sentinel) || fault.seen != fault.ordinal) {
					t.Fatalf("native %s error identity/order lost: %v seen=%d", kind, err, fault.seen)
				}
			}
			claimAfter, err := os.ReadFile(f.source.WorkLogPath)
			if err != nil || !reflect.DeepEqual(claimAfter, claimBefore) {
				t.Fatalf("source validation changed immutable claim: %v", err)
			}
			if actual := strings.TrimSpace(runEngineGit(t, f.source.WorktreeDir, "rev-parse", "HEAD")); actual != expectedHead {
				t.Fatalf("source validation changed checkout: %s != %s", actual, expectedHead)
			}
			if actual := strings.TrimSpace(runEngineGit(t, f.engine.repository.CloneURL, "for-each-ref", "--format=%(objectname)", "refs/heads/"+f.source.Branch)); actual != remoteSource {
				t.Fatalf("source validation changed publication: %s != %s", actual, remoteSource)
			}
		})
	}
}

// recheckWorktreeMergeSources retains the native default binding used only by
// historical e2e tests; production callers supply their invocation runner.
func recheckWorktreeMergeSources(ctx context.Context, sources []WorktreeMergeSource) error {
	return recheckWorktreeMergeSourcesWithRunner(ctx, defaultRunner, sources)
}
