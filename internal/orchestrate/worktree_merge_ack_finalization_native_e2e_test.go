//go:build e2e

package orchestrate

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
)

func TestE2ECollisionReplayReauthenticatesNativeCustodyAndFreshClaimBytes(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"exact wrapper", "exact runner", "missing acknowledgement", "dirty candidate", "late physical read", "claim hash", "claim base"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f, options := protocolCollisionFixture(t)
			options.Apply, options.Actor, options.Reason = true, "private reviewer", "native replay authentication"
			ack, err := AcknowledgeWorktreeMergeReceiptCollision(t.Context(), options)
			if err != nil {
				t.Fatal(err)
			}
			r := f.receipt
			claim, err := validateMergeAcknowledgementCandidate(t.Context(), f.engine.githubDir, r, r.Candidate)
			if err != nil {
				t.Fatal(err)
			}
			receiptBefore, err := os.ReadFile(r.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			claimBefore, err := os.ReadFile(claim.ClaimPath)
			if err != nil {
				t.Fatal(err)
			}
			path := receiptCollisionAcknowledgementPath(r.ReceiptPath)
			hash := worktreeMergeReceiptSHA256
			observed := false
			restore := func() {}
			switch mode {
			case "missing acknowledgement":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "dirty candidate":
				writeEngineFile(t, filepath.Join(r.Candidate.Worktree, "private-untracked.txt"), "native dirty custody\n")
			case "late physical read":
				hash = func(input string) (string, error) {
					if input != claim.ClaimPath {
						t.Fatalf("late hash targeted %s, want actual immutable claim %s", input, claim.ClaimPath)
					}
					observed = true
					saved := input + ".private-retained"
					if err := os.Rename(input, saved); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(input, 0700); err != nil {
						t.Fatal(err)
					}
					restore = func() {
						if err := os.Remove(input); err != nil {
							t.Error(err)
						}
						if err := os.Rename(saved, input); err != nil {
							t.Error(err)
						}
					}
					t.Cleanup(func() { restore() })
					// A real directory read refusal occurs only after the native Guard,
					// HEAD and Work Log custody checks have consumed the original claim.
					return worktreeMergeReceiptSHA256(input)
				}
			case "claim hash":
				ack.ImmutableClaimSHA256 = "different-bytes"
				ack.ID = receiptCollisionAcknowledgementID(ack)
				writeAcknowledgementReadJSON(t, path, ack)
			case "claim base":
				ack.ClaimBaseSHA = "different-base"
				ack.ID = receiptCollisionAcknowledgementID(ack)
				writeAcknowledgementReadJSON(t, path, ack)
			}
			var before []byte
			if mode != "missing acknowledgement" {
				before, err = os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			var got WorktreeMergeReceiptCollisionAcknowledgement
			if mode == "exact wrapper" {
				got, err = validateReceiptCollisionAcknowledgement(t.Context(), f.engine.githubDir, r)
			} else {
				got, err = validateReceiptCollisionAcknowledgementWithRunner(t.Context(), runner.New(), hash, f.engine.githubDir, r)
			}
			if mode == "exact wrapper" || mode == "exact runner" {
				if err != nil || got.ID != ack.ID {
					t.Fatalf("native replay=%+v %v", got, err)
				}
			} else {
				if err == nil || got.ID != "" {
					t.Fatalf("native replay accepted %s: %+v %v", mode, got, err)
				}
				switch mode {
				case "missing acknowledgement":
					if !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("ack read sentinel=%v", err)
					}
				case "dirty candidate":
					if !strings.Contains(err.Error(), "validate collision acknowledgement candidate:") {
						t.Fatalf("custody diagnostic=%v", err)
					}
				case "late physical read":
					var pathErr *os.PathError
					if !observed || !errors.As(err, &pathErr) || !errors.Is(err, syscall.EISDIR) || pathErr.Path != claim.ClaimPath || !strings.HasPrefix(err.Error(), "read collision acknowledgement immutable claim: ") {
						t.Fatalf("native late read=%v observed=%t", err, observed)
					}
					restore()
					// Cleanup has already restored the exact original claim.
					restore = func() {}
				case "claim hash", "claim base":
					if !strings.Contains(err.Error(), "immutable claim SHA256 or base") {
						t.Fatalf("claim evidence refusal=%v", err)
					}
				}
			}
			protocolUnchangedBytes(t, r.ReceiptPath, receiptBefore)
			protocolUnchangedBytes(t, claim.ClaimPath, claimBefore)
			if mode == "missing acknowledgement" {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("read recreated missing ack: %v", err)
				}
			} else {
				protocolUnchangedBytes(t, path, before)
			}
		})
	}
}

//nolint:paralleltest // Serial rows install the existing process-wide GH executable fixture.
func TestE2EPublishedRebatchReprovesNativeRefAndDurableSupersession(t *testing.T) {
	for _, mode := range []string{"open", "origin unavailable", "ref drift", "PR read", "base mismatch", "head mismatch", "closed own intent", "closed other intent", "merged"} {
		//nolint:paralleltest // The existing private GH executable fixture uses t.Setenv.
		t.Run(mode, func(t *testing.T) {
			f, r, replacement, _ := preparedRebatchNativePair(t)
			runEngineGit(t, r.Candidate.Worktree, "push", "origin", "HEAD:refs/heads/"+r.Candidate.Branch)
			r.Status, r.Phase, r.PullRequest, r.PublishedCandidateSHA = WorktreeMergeChecksFailed, WorktreeMergePhaseLand, "41", r.Candidate.SHA
			if err := persistWorktreeMergeReceipt(r); err != nil {
				t.Fatal(err)
			}
			installWorktreeMergeDirectGH(t)
			t.Setenv("WB_TEST_CANDIDATE_SHA", r.Candidate.SHA)
			t.Setenv("WB_TEST_PR_STATE", "open")
			closedLog := filepath.Join(t.TempDir(), "closed.log")
			t.Setenv("WB_TEST_CLOSED_PR_LOG", closedLog)
			switch mode {
			case "origin unavailable":
				runEngineGit(t, r.Candidate.Worktree, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))
			case "ref drift":
				runEngineGit(t, r.Candidate.Worktree, "push", "origin", r.TargetSHA+":refs/heads/"+r.Candidate.Branch, "--force")
			case "PR read":
				r.PullRequest = "not-a-selector"
			case "base mismatch":
				r.Target = "other-target"
			case "head mismatch":
				t.Setenv("WB_TEST_CANDIDATE_SHA", r.TargetSHA)
			case "closed own intent", "closed other intent":
				t.Setenv("WB_TEST_PR_STATE", "closed")
				replacement.SupersededPullRequest = r.PullRequest
				if mode == "closed other intent" {
					replacement.RebatchOf = "another-original"
				}
				if err := persistWorktreeMergeReceipt(replacement); err != nil {
					t.Fatal(err)
				}
			case "merged":
				t.Setenv("WB_TEST_PR_STATE", "closed")
				t.Setenv("WB_TEST_PR_MERGED", "true")
			}
			// These owners are read-only: even the exact, persisted-close replay
			// must not PATCH a PR or change any existing acknowledgement bytes.
			receiptBefore, err := os.ReadFile(r.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			ackPath := rebatchPath(r.ReceiptPath)
			ackBefore, err := os.ReadFile(ackPath)
			if err != nil {
				t.Fatal(err)
			}
			err = validatePublishedUnlandedRebatch(t.Context(), f.githubDir, r, r.Repository, r.Target, replacement.Sources)
			valid := mode == "open" || mode == "closed own intent"
			if (err == nil) != valid {
				t.Fatalf("published %s result=%v", mode, err)
			}
			if mode == "origin unavailable" && !strings.HasPrefix(err.Error(), "read checks-failed candidate ref:") {
				t.Fatalf("native remote refusal=%v", err)
			}
			if mode == "PR read" && !strings.HasPrefix(err.Error(), "read checks-failed pull request:") {
				t.Fatalf("PR read refusal=%v", err)
			}
			protocolUnchangedBytes(t, r.ReceiptPath, receiptBefore)
			protocolUnchangedBytes(t, ackPath, ackBefore)
			if _, err := os.Stat(closedLog); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("read issued close: %v", err)
			}
		})
	}
}

//nolint:paralleltest // Uses the existing process-wide GH executable fixture.
func TestE2ESupersededPullRequestMustVerifyClosedAfterSuccessfulPatch(t *testing.T) {
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_CANDIDATE_SHA", "private-head")
	t.Setenv("WB_TEST_PR_STATE", "open")
	closedLog := filepath.Join(t.TempDir(), "closed.log")
	t.Setenv("WB_TEST_CLOSED_PR_LOG", closedLog)
	sleeps := 0
	err := closeSupersededWorktreeMergePullRequest(t.Context(), "acme/app", "41", func(time.Duration) { sleeps++ })
	if err == nil || !strings.Contains(err.Error(), "did not close (state is open)") || sleeps != 0 {
		t.Fatalf("close verification=%v sleeps=%d", err, sleeps)
	}
	calls, readErr := os.ReadFile(closedLog)
	if readErr != nil || !bytes.Equal(calls, []byte("api --method PATCH repos/acme/app/pulls/41 -f state=closed\n")) {
		t.Fatalf("close protocol calls=%q %v", calls, readErr)
	}
}
