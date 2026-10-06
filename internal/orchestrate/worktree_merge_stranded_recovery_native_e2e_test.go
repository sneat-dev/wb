//go:build e2e

package orchestrate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubchecks"
	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestE2EStrandedRecoveryUsesNativeRemoteProofAndPreservesFailedPersistence(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"landed", "previous target", "squash", "provider refusal", "receipt destination"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f, r := landOwnerNativeFixture(t)
			claim, err := validateMergeAcknowledgementCandidate(t.Context(), f.githubDir, r, r.Candidate)
			if err != nil {
				t.Fatal(err)
			}
			candidateClaim, err := os.ReadFile(claim.ClaimPath)
			if err != nil {
				t.Fatal(err)
			}
			source := r.Sources[0]
			guard, err := worktrees.Guard(t.Context(), source.Worktree, worktrees.GuardOptions{ProjectsRoot: f.githubDir, Base: r.Target})
			if err != nil || guard.Kind != "linked" || guard.Branch != source.Branch {
				t.Fatalf("source native custody=%+v %v", guard, err)
			}
			sourceView, err := worktrees.LoadWorkLogView(t.Context(), worktrees.LoadWorkLogOptions{ProjectsRoot: f.githubDir, Worktree: source.Worktree})
			if err != nil || sourceView.Claim == nil {
				t.Fatalf("source immutable claim=%+v %v", sourceView, err)
			}
			sourceClaim, err := os.ReadFile(sourceView.Claim.ClaimPath)
			if err != nil {
				t.Fatal(err)
			}
			sourceHead := strings.TrimSpace(runEngineGit(t, source.Worktree, "rev-parse", "HEAD"))
			r.Phase, r.Status, r.PullRequest, r.PublishedCandidateSHA = WorktreeMergePhaseLand, WorktreeMergePublished, "91", r.Candidate.SHA
			r.Checks = githubchecks.PullRequestWaitResult{Status: githubchecks.PullRequestWaitFailed, Head: r.Candidate.SHA}
			r.Failure = "published before landing-result observation"
			r.UpdatedAt = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
			if mode == "previous target" {
				r.PreviousTargetSHA = sourceHead
			}
			runEngineGit(t, r.Candidate.Worktree, "push", "origin", r.Candidate.SHA+":refs/heads/"+r.Candidate.Branch)
			landing := r.Candidate.SHA
			if mode == "squash" {
				tree := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", r.Candidate.SHA+"^{tree}"))
				landing = strings.TrimSpace(runEngineGit(t, f.canonical, "commit-tree", tree, "-p", r.TargetSHA, "-m", "private native squash landing"))
				runEngineGit(t, f.canonical, "update-ref", "refs/heads/main", landing)
			} else {
				runEngineGit(t, f.canonical, "merge", "--ff-only", r.Candidate.SHA)
			}
			runEngineGit(t, f.canonical, "push", "origin", "main")
			target := strings.TrimSpace(runEngineGit(t, f.repository.CloneURL, "rev-parse", "refs/heads/main"))
			if target != landing {
				t.Fatalf("native target=%s landing=%s", target, landing)
			}
			if err := persistWorktreeMergeReceipt(r); err != nil {
				t.Fatal(err)
			}
			before := r
			durableBefore, err := os.ReadFile(r.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			preserved := r.Candidate.Worktree + ".private-preserved"
			if err := os.Rename(r.Candidate.Worktree, preserved); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.Rename(preserved, r.Candidate.Worktree); err != nil {
					t.Error(err)
				}
			})
			sentinel := errors.New("private hosted landing read")
			gets, reads, trees, finalProofs := 0, 0, 0, 0
			saved := r.ReceiptPath + ".private-original"
			destinationFault := false
			restoreReceipt := func() {}
			t.Cleanup(func() { restoreReceipt() })
			ctx := githubobserver.WithReader(t.Context(), githubobserver.Reader{
				Read: func(_ context.Context, dir string, args ...string) ([]byte, error) {
					reads++
					want := []string{"pr", "view", r.PullRequest, "--repo", r.Repository, "--json", "state,mergedAt,mergeCommit,headRefOid,baseRefName"}
					if dir != "" || !reflect.DeepEqual(args, want) {
						t.Fatalf("stranded PR observation=%s %q", dir, args)
					}
					if mode == "provider refusal" {
						return nil, sentinel
					}
					actualHead := strings.TrimSpace(runEngineGit(t, f.repository.CloneURL, "rev-parse", "refs/heads/"+r.Candidate.Branch))
					actualTarget := strings.TrimSpace(runEngineGit(t, f.repository.CloneURL, "rev-parse", "refs/heads/main"))
					if actualHead != before.Candidate.SHA || actualTarget != landing {
						t.Fatalf("native PR identities head=%s target=%s", actualHead, actualTarget)
					}
					// PR metadata is fixture protocol. Every commit/ref/ancestry/tree it
					// names is independently observed from this owned bare origin.
					return []byte(fmt.Sprintf(`{"state":"MERGED","mergedAt":"2026-09-01T00:00:00Z","headRefOid":%q,"baseRefName":"main","mergeCommit":{"oid":%q}}`, actualHead, actualTarget)), nil
				},
				Get: func(_ context.Context, req githubobserver.GetRequest) (githubobserver.Response, error) {
					gets++
					if req.Dir != "" || req.Repository != r.Repository || req.FreshWindow != 0 {
						t.Fatalf("native stranded request=%+v", req)
					}
					var body []byte
					switch req.Endpoint {
					case "repos/" + r.Repository + "/git/ref/heads/main":
						if req.Target != "main" || req.Head != "" {
							t.Fatalf("native target request=%+v", req)
						}
						actual := strings.TrimSpace(runEngineGit(t, f.repository.CloneURL, "rev-parse", "refs/heads/main"))
						body = []byte(fmt.Sprintf(`{"object":{"sha":%q}}`, actual))
					case "repos/" + r.Repository + "/compare/" + req.Target + "..." + req.Head:
						base := strings.TrimSpace(runEngineGit(t, f.repository.CloneURL, "rev-parse", req.Target))
						head := strings.TrimSpace(runEngineGit(t, f.repository.CloneURL, "rev-parse", req.Head))
						mergeBase := strings.TrimSpace(runEngineGit(t, f.repository.CloneURL, "merge-base", base, head))
						status := "diverged"
						if base == head {
							status = "identical"
						} else if base == mergeBase {
							status = "ahead"
						} else if head == mergeBase {
							status = "behind"
						}
						body = []byte(fmt.Sprintf(`{"status":%q,"base_commit":{"sha":%q},"merge_base_commit":{"sha":%q}}`, status, base, mergeBase))
						if req.Target == before.TargetSHA && req.Head == before.Candidate.SHA {
							finalProofs++
							if mergeBase != before.TargetSHA || status != "ahead" {
								t.Fatalf("final real candidate/target proof=%s %s", status, mergeBase)
							}
							if mode == "receipt destination" && !destinationFault {
								// The last real remote proof has completed. Preserve original
								// bytes, then fail only atomic publication at the exact destination.
								if err := os.Rename(before.ReceiptPath, saved); err != nil {
									t.Fatal(err)
								}
								restoreReceipt = func() {
									if err := os.Remove(before.ReceiptPath); err != nil {
										t.Error(err)
									}
									if err := os.Rename(saved, before.ReceiptPath); err != nil {
										t.Error(err)
									}
								}
								if err := os.Mkdir(before.ReceiptPath, 0700); err != nil {
									t.Fatal(err)
								}
								destinationFault = true
							}
						}
					case "repos/" + r.Repository + "/git/commits/" + req.Target:
						if req.Head != "" {
							t.Fatalf("native tree request=%+v", req)
						}
						trees++
						tree := strings.TrimSpace(runEngineGit(t, f.repository.CloneURL, "rev-parse", req.Target+"^{tree}"))
						body = []byte(fmt.Sprintf(`{"tree":{"sha":%q}}`, tree))
					default:
						t.Fatalf("unexpected native stranded endpoint=%+v", req)
					}
					return githubobserver.Response{Body: body}, nil
				},
				Execute: func(context.Context, string, ...string) githubobserver.CommandResponse {
					t.Fatal("stranded proof attempted hosted mutation")
					return githubobserver.CommandResponse{}
				},
			})
			recovered, err := recoverAlreadyMergedPublishedWorktreeMerge(ctx, &r)
			if mode == "provider refusal" {
				if recovered || !errors.Is(err, sentinel) || !reflect.DeepEqual(r, before) || reads != 1 || gets != 0 {
					t.Fatalf("provider refusal=%t %v reads=%d gets=%d mutated=%t", recovered, err, reads, gets, !reflect.DeepEqual(r, before))
				}
				protocolUnchangedBytes(t, r.ReceiptPath, durableBefore)
			} else {
				if mode == "receipt destination" {
					var rename *os.LinkError
					if recovered || !destinationFault || finalProofs != 1 || !errors.As(err, &rename) || rename.Op != "rename" || rename.New != before.ReceiptPath || filepath.Dir(rename.Old) != filepath.Dir(before.ReceiptPath) || !strings.HasPrefix(filepath.Base(rename.Old), ".merge-receipt-") || !errors.Is(err, syscall.EISDIR) && !errors.Is(err, os.ErrExist) {
						t.Fatalf("exact late publication refusal=%t %v fault=%t finalProofs=%d", recovered, err, destinationFault, finalProofs)
					}
					protocolUnchangedBytes(t, saved, durableBefore)
					if entries, e := filepath.Glob(filepath.Join(filepath.Dir(before.ReceiptPath), ".merge-receipt-*.tmp")); e != nil || len(entries) != 0 {
						t.Fatalf("failed writer leaked temporary files=%v %v", entries, e)
					}
					// Completed proof mutates the live receipt even when publication fails.
					if r.Status != WorktreeMergeLanded || r.LandingSHA != target || r.Failure != "" || r.Checks.Status != "" {
						t.Fatalf("failed publication lost completed proof=%+v", r)
					}
					readCount := reads
					if again, e := recoverAlreadyMergedPublishedWorktreeMerge(ctx, &r); again || e != nil || reads != readCount {
						t.Fatalf("landed in-memory retry=%t %v reads=%d", again, e, reads)
					}
					restoreReceipt()
					restoreReceipt = func() {}
					r, err = readWorktreeMergeReceipt(before.ReceiptPath)
					if err != nil {
						t.Fatal(err)
					}
					recovered, err = recoverAlreadyMergedPublishedWorktreeMerge(ctx, &r)
				}
				if err != nil || !recovered || r.Status != WorktreeMergeLanded || r.LandingSHA != target || r.Failure != "" || !reflect.DeepEqual(r.Checks, githubchecks.PullRequestWaitResult{}) || !r.UpdatedAt.After(before.UpdatedAt) || r.UpdatedAt.Location() != time.UTC {
					t.Fatalf("native recovery=%t %v receipt=%+v", recovered, err, r)
				}
				wantPrevious := before.PreviousTargetSHA
				if wantPrevious == "" {
					wantPrevious = before.TargetSHA
				}
				if r.PreviousTargetSHA != wantPrevious || r.TargetSHA != before.TargetSHA || r.Candidate != before.Candidate || !reflect.DeepEqual(r.Sources, before.Sources) {
					t.Fatalf("recovery lost immutable identities=%+v", r)
				}
				stored, e := readWorktreeMergeReceipt(r.ReceiptPath)
				if e != nil || !reflect.DeepEqual(stored, r) {
					t.Fatalf("terminal recovery was not durable=%+v %v", stored, e)
				}
				if mode == "squash" && trees != 2 {
					t.Fatalf("native squash did not read both real remote trees: %d", trees)
				}
				if mode != "squash" && trees != 0 {
					t.Fatalf("native ancestry unnecessarily used tree fallback: %d", trees)
				}
			}
			protocolUnchangedBytes(t, claim.ClaimPath, candidateClaim)
			protocolUnchangedBytes(t, sourceView.Claim.ClaimPath, sourceClaim)
			if got := strings.TrimSpace(runEngineGit(t, source.Worktree, "rev-parse", "HEAD")); got != sourceHead {
				t.Fatalf("recovery moved native source %s", got)
			}
			if got := strings.TrimSpace(runEngineGit(t, f.repository.CloneURL, "rev-parse", "refs/heads/main")); got != target {
				t.Fatalf("recovery moved remote target %s", got)
			}
			// The moved candidate is restored by cleanup before fixture retirement;
			// its Git object, branch and immutable claim remained present throughout.
			retained, err := os.ReadFile(claim.ClaimPath)
			if err != nil || !bytes.Equal(retained, candidateClaim) {
				t.Fatalf("retained candidate claim=%v", err)
			}
		})
	}
}
