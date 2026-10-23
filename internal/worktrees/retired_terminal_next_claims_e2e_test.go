//go:build e2e

package worktrees

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

//nolint:paralleltest // The existing native fixture configures Git and agent environment.
func TestE2ERetiredTerminalNextClaimRereadsKeepNativeAuthority(t *testing.T) {
	f, worktree, _ := newSessionCheckpointFixture(t, "retired-read-next")
	claim, projection, err := retireReadClaim(f.home, worktree)
	if err != nil {
		t.Fatal(err)
	}
	runPath := filepath.Join(f.home, "worklogs", projection.EffortID, "runs", projection.RunID)
	claimPath := filepath.Join(runPath, "claims", projection.ClaimID+".json")
	claimBytes, err := os.ReadFile(claimPath)
	if err != nil {
		t.Fatal(err)
	}
	if claim.ClaimID != projection.ClaimID {
		t.Fatal("native reader did not return corroborated identity")
	}
	head := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	for _, phase := range []string{"after_corroboration", "before_claim_reread"} {
		//nolint:paralleltest // Mutations share this test's one native retained fixture and are restored sequentially.
		t.Run(phase, func(t *testing.T) {
			observed := false
			got, selected, err := retireReadClaimObserved(f.home, worktree, func(at string, p workLogProjection, held *os.File) {
				if at != phase {
					return
				}
				observed = true
				if p != projection {
					t.Fatal("reread selected different projection")
				}
				if phase == "after_corroboration" {
					if held != nil {
						t.Fatal("run opened before corroboration observer")
					}
					if err := os.Rename(runPath, runPath+".retained"); err != nil {
						t.Fatal(err)
					}
				} else {
					if held == nil {
						t.Fatal("claim reread has no owned run")
					}
					if err := os.WriteFile(claimPath, []byte("{invalid-private-claim\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			})
			if !observed || err == nil || !reflect.DeepEqual(got, workLogClaim{}) || selected != projection {
				t.Fatalf("reread=%+v,%+v,%v observed=%v", got, selected, err, observed)
			}
			if phase == "after_corroboration" {
				if err := os.Rename(runPath+".retained", runPath); err != nil {
					t.Fatal(err)
				}
			} else {
				if !strings.Contains(err.Error(), "invalid") {
					t.Fatalf("unexpected native JSON cause: %v", err)
				}
				if err := os.WriteFile(claimPath, claimBytes, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := retireReadClaim(f.home, worktree); err != nil {
				t.Fatalf("restored native authority rejected: %v", err)
			}
		})
	}
	if err := os.WriteFile(claimPath, []byte("{bad-corroboration\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := retireReadClaim(f.home, worktree); err == nil {
		t.Fatal("invalid private corroboration admitted")
	}
	if err := os.WriteFile(claimPath, claimBytes, 0600); err != nil {
		t.Fatal(err)
	}
	directory, err := openWorkLogProjectionDirectory(worktree, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	projectionPath := filepath.Join(worktree, workLogProjectionDirectory, workLogProjectionName)
	original, err := os.ReadFile(projectionPath)
	if err != nil {
		t.Fatal(err)
	}
	bad := projection
	bad.Lifecycle = "unsupported"
	raw, err := json.Marshal(bad)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectionPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := retireReadClaim(f.home, worktree); err == nil || !strings.Contains(err.Error(), "projection lifecycle") {
		t.Fatalf("upstream lifecycle admission=%v", err)
	}
	if err := os.WriteFile(projectionPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	if got := gitTestOutput(t, worktree, "rev-parse", "HEAD"); got != head {
		t.Fatalf("reader moved HEAD: %s", got)
	}
}

func TestE2ERetiredTerminalNextCandidateHeadUsesActualGit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if got := candidateBranchHead(context.Background(), filepath.Join(root, "missing-checkout")); got != "" {
		t.Fatalf("missing native checkout head=%q", got)
	}
	gitTest(t, root, "init")
	gitTest(t, root, "-c", "user.name=Retirement Test", "-c", "user.email=retired@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "native head")
	want := gitTestOutput(t, root, "rev-parse", "HEAD")
	if got := candidateBranchHead(context.Background(), root); got != want {
		t.Fatalf("head=%q want%q", got, want)
	}
}
