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
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/mergeack"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// This fixture has a genuine managed candidate/Work Log and native objects. Its
// persisted conflict DTO is controlled historical evidence, not a claim that
// Prepare previously observed its absent source. Original Prepare/Ack journeys
// remain the positive public chronology witnesses.
func absorbedAckOwnerFixture(t *testing.T) (engineFixture, WorktreeMergeReceipt, WorktreeMergeAbsorbedConflictAcknowledgementOptions) {
	t.Helper()
	f, c, view := createFlowManaged(t, false)
	claimBefore, err := os.ReadFile(view.Claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		claimAfter, err := os.ReadFile(view.Claim.ClaimPath)
		if err != nil || !bytes.Equal(claimBefore, claimAfter) {
			t.Errorf("native candidate claim changed: %v", err)
		}
	})
	home, err := wbhome.Root(f.githubDir)
	if err != nil {
		t.Fatal(err)
	}
	r := WorktreeMergeReceipt{SchemaVersion: WorktreeMergeSchemaVersion, ID: "absorbed-owner", Lane: worktreeMergeLaneID(f.repository.Slug, "main"), Phase: WorktreeMergePhasePrepare, Status: WorktreeMergeConflict, Repository: f.repository.Slug, Target: "main", TargetSHA: c.BaseSHA, Candidate: WorktreeMergeCandidate{Task: "pr-create-flow", Worktree: c.WorktreeDir, Branch: c.Branch}, Sources: []WorktreeMergeSource{{Task: "gone-source", Worktree: filepath.Join(f.githubDir, "gone-source"), Branch: "feature/gone-source", SHA: c.BaseSHA}}, ReceiptPath: filepath.Join(home, "reports", "worktree-merge", "absorbed-owner.json")}
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	return f, r, WorktreeMergeAbsorbedConflictAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Apply: true, Actor: "native reviewer", Reason: "controlled historical eligibility"}
}

func TestE2EAbsorbedAcknowledgementRereadsPhysicalEvidenceUnderLaneLock(t *testing.T) {
	t.Parallel()
	f, r, o := absorbedAckOwnerFixture(t)
	verify := retirementOwnerReceiptBytes(t, r)
	for _, stage := range []string{"resolver", "first read", "first validation", "actor", "reason", "second read", "second validation", "held lock", "changed lane"} {
		//nolint:paralleltest // Rows mutate one private receipt or hold its actual lane lock, then restore before the next row.
		if !t.Run(stage, func(t *testing.T) {
			options := o
			reads := 0
			hashCalls, persistCalls := 0, 0
			changedTarget := "changed-owned-target"
			changedLane := worktreeMergeLaneID(r.Repository, changedTarget)
			changedBranchCreated := false
			backup := r.ReceiptPath + ".owned-held"
			restore := func() {
				if changedBranchCreated {
					if err := restoreAbsorbedAckOwnedRemoteRef(f.canonical, changedTarget); err != nil {
						t.Error(err)
					} else {
						changedBranchCreated = false
					}
				}
				if stage == "changed lane" {
					if err := os.Remove(absorbedConflictAcknowledgementPath(r.ReceiptPath)); err != nil && !os.IsNotExist(err) {
						t.Error(err)
					}
				}
				if _, err := os.Stat(backup); err == nil {
					if err := os.Rename(backup, r.ReceiptPath); err != nil {
						t.Error(err)
					}
				} else if err := persistWorktreeMergeReceipt(r); err != nil {
					t.Error(err)
				}
			}
			t.Cleanup(restore)
			if stage == "changed lane" {
				runEngineGit(t, f.canonical, "push", "origin", "main:refs/heads/"+changedTarget)
				changedBranchCreated = true
			}
			if stage == "resolver" {
				options.Receipt = ""
			}
			if stage == "actor" {
				options.Actor = " \t "
			}
			if stage == "reason" {
				options.Reason = " \n "
			}
			if stage == "first validation" {
				changed := r
				changed.Phase = WorktreeMergePhaseLand
				if err := persistWorktreeMergeReceipt(changed); err != nil {
					t.Fatal(err)
				}
			}
			var held OperationLock
			if stage == "held lock" {
				var err error
				held, err = AcquireOperationLock(f.githubDir, r.Lane, true)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := held.Release(); err != nil {
						t.Error(err)
					}
				})
			}
			read := func(path string) (WorktreeMergeReceipt, error) {
				reads++
				if path != r.ReceiptPath {
					t.Fatalf("read path=%q", path)
				}
				if reads == 2 {
					second, err := AcquireOperationLock(f.githubDir, r.Lane, true)
					if err == nil {
						_ = second.Release()
						t.Fatal("receipt reread occurred without exclusive lane custody")
					}
				}
				if stage == "first read" && reads == 1 || stage == "second read" && reads == 2 {
					if err := os.Rename(path, backup); err != nil {
						t.Fatal(err)
					}
				}
				if stage == "second validation" && reads == 2 {
					changed := r
					changed.Phase = WorktreeMergePhaseLand
					if err := persistWorktreeMergeReceipt(changed); err != nil {
						t.Fatal(err)
					}
				}
				if stage == "changed lane" && reads == 2 {
					changed := r
					changed.Target = changedTarget
					changed.Lane = changedLane
					if changedLane == r.Lane {
						t.Fatal("native distinct target has same fixture lane")
					}
					if err := persistWorktreeMergeReceipt(changed); err != nil {
						t.Fatal(err)
					}
					available, err := AcquireOperationLock(f.githubDir, changedLane, true)
					if err != nil {
						t.Fatalf("new lane not available: %v", err)
					}
					if err := available.Release(); err != nil {
						t.Fatal(err)
					}
				}
				return readWorktreeMergeReceipt(path)
			}
			hash := func(path string) (string, error) { hashCalls++; return worktreeMergeReceiptSHA256(path) }
			persist := func(path string, ack WorktreeMergeAbsorbedConflictAcknowledgement) error {
				persistCalls++
				return mergeack.Persist(path, ack)
			}
			got, err := acknowledgeAbsorbedConflict(t.Context(), options, read, hash, persist)
			if err == nil || !reflect.DeepEqual(got, WorktreeMergeAbsorbedConflictAcknowledgement{}) {
				t.Fatalf("stage=%s ack=%+v err=%v hash=%d persist=%d", stage, got, err, hashCalls, persistCalls)
			}
			wantReads := 1
			want := "want an unpublished prepare conflict"
			switch stage {
			case "resolver":
				wantReads = 0
				want = "candidate worktree or receipt is required"
			case "first read":
				want = ""
			case "actor", "reason":
				want = "--actor and --reason are required with --apply"
			case "second read":
				wantReads = 2
				want = ""
			case "second validation":
				wantReads = 2
			case "held lock":
				want = "already active"
			case "changed lane":
				wantReads = 2
				want = "changed lane while acquiring ownership"
			}
			if reads != wantReads {
				t.Fatalf("reads=%d want=%d", reads, wantReads)
			}
			if want != "" && !strings.Contains(err.Error(), want) {
				t.Fatalf("error=%v want %q", err, want)
			}
			if strings.HasSuffix(stage, "read") && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("physical read error lost: %v", err)
			}
			if hashCalls != 0 || persistCalls != 0 {
				t.Fatalf("admission refusal reached late IO: hash=%d persist=%d", hashCalls, persistCalls)
			}
			if _, err := os.Stat(absorbedConflictAcknowledgementPath(r.ReceiptPath)); !os.IsNotExist(err) {
				t.Fatalf("refusal wrote sidecar before restoration: %v", err)
			}
			restore()
			verify()
			if _, err := os.Stat(absorbedConflictAcknowledgementPath(r.ReceiptPath)); !os.IsNotExist(err) {
				t.Fatalf("refusal wrote acknowledgement: %v", err)
			}
			if stage == "changed lane" {
				retirementOwnerReleased(t, f.githubDir, changedLane)
				if got := strings.TrimSpace(runEngineGit(t, f.canonical, "ls-remote", "--heads", "origin", "refs/heads/"+changedTarget)); got != "" {
					t.Fatalf("owned target ref leaked: %s", got)
				}
				if got := strings.TrimSpace(runEngineGit(t, f.repository.CloneURL, "rev-parse", "refs/heads/main")); got != r.TargetSHA {
					t.Fatalf("original target ref changed: %s", got)
				}
			}
			if stage != "held lock" {
				retirementOwnerReleased(t, f.githubDir, r.Lane)
			}
		}) {
			t.Fatal("shared fixture row failed; stop before another mutation")
		}
	}
}

func TestE2EAbsorbedAcknowledgementPhysicalStatHashAndPersistRefusals(t *testing.T) {
	t.Parallel()
	f, r, o := absorbedAckOwnerFixture(t)
	verify := retirementOwnerReceiptBytes(t, r)
	beforeHead := strings.TrimSpace(runEngineGit(t, r.Candidate.Worktree, "rev-parse", "HEAD"))
	for _, stage := range []string{"source inspect", "candidate inspect", "published candidate", "target fetch", "hash", "persist"} {
		//nolint:paralleltest // Rows stage real filesystem faults against one private receipt/candidate and restore synchronously.
		if !t.Run(stage, func(t *testing.T) {
			backup := r.ReceiptPath + ".owned-held"
			candidateBackup := r.Candidate.Worktree + ".owned-held"
			ackPath := absorbedConflictAcknowledgementPath(r.ReceiptPath)
			calls := 0
			lane := r.Lane
			published := false
			restore := func() {
				if published {
					if err := restoreAbsorbedAckOwnedRemoteRef(f.canonical, r.Candidate.Branch); err != nil {
						t.Error(err)
					} else {
						published = false
					}
				}
				if _, err := os.Stat(candidateBackup); err == nil {
					if err := os.Remove(r.Candidate.Worktree); err != nil {
						t.Error(err)
					}
					if err := os.Rename(candidateBackup, r.Candidate.Worktree); err != nil {
						t.Error(err)
					}
				}
				if _, err := os.Stat(backup); err == nil {
					if err := os.Rename(backup, r.ReceiptPath); err != nil {
						t.Error(err)
					}
				} else if err := persistWorktreeMergeReceipt(r); err != nil {
					t.Error(err)
				}
				if err := os.Remove(ackPath); err != nil && !os.IsNotExist(err) {
					t.Error(err)
				}
			}
			t.Cleanup(restore)
			if stage == "published candidate" {
				runEngineGit(t, r.Candidate.Worktree, "push", "origin", "HEAD:refs/heads/"+r.Candidate.Branch)
				published = true
			}
			if stage == "target fetch" {
				changed := r
				changed.Target = "missing-native-target"
				changed.Lane = worktreeMergeLaneID(changed.Repository, changed.Target)
				lane = changed.Lane
				if err := persistWorktreeMergeReceipt(changed); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "candidate inspect" {
				if err := os.Rename(r.Candidate.Worktree, candidateBackup); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(r.Candidate.Worktree, []byte("owned invalid candidate file"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "source inspect" {
				parent := filepath.Join(t.TempDir(), "regular-parent")
				if err := os.WriteFile(parent, []byte("owned regular file"), 0600); err != nil {
					t.Fatal(err)
				}
				changed := r
				changed.Sources = append([]WorktreeMergeSource(nil), r.Sources...)
				changed.Sources[0].Worktree = filepath.Join(parent, "child")
				if _, err := os.Stat(changed.Sources[0].Worktree); err == nil || errors.Is(err, os.ErrNotExist) {
					t.Fatalf("native non-ENOENT Stat precondition: %v", err)
				}
				if err := persistWorktreeMergeReceipt(changed); err != nil {
					t.Fatal(err)
				}
			}
			hash := func(path string) (string, error) {
				if path != r.ReceiptPath {
					t.Fatalf("hash path=%q", path)
				}
				if stage == "hash" {
					calls++
					if err := os.Rename(path, backup); err != nil {
						t.Fatal(err)
					}
				}
				return worktreeMergeReceiptSHA256(path)
			}
			persist := func(path string, a WorktreeMergeAbsorbedConflictAcknowledgement) error {
				if path != ackPath || a.CurrentTargetSHA != r.TargetSHA || len(a.SourceProofs) != 1 || a.SourceProofs[0].Method != "ancestor" {
					t.Fatalf("native proof lost at persist: %+v", a)
				}
				calls++
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("expected absent sidecar before physical collision: %v", err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				return mergeack.Persist(path, a)
			}
			got, err := acknowledgeAbsorbedConflict(t.Context(), o, readWorktreeMergeReceipt, hash, persist)
			if err == nil || !reflect.DeepEqual(got, WorktreeMergeAbsorbedConflictAcknowledgement{}) {
				t.Fatalf("stage=%s ack=%+v err=%v", stage, got, err)
			}
			if stage == "source inspect" {
				if !strings.Contains(err.Error(), "inspect receipted source") || calls != 0 {
					t.Fatalf("stat stage %v calls=%d", err, calls)
				}
			} else if stage == "candidate inspect" {
				if !strings.Contains(err.Error(), "is not a directory") || calls != 0 {
					t.Fatalf("candidate stage %v calls=%d", err, calls)
				}
			} else if stage == "published candidate" {
				if !strings.Contains(err.Error(), "is published;") || calls != 0 {
					t.Fatalf("publication stage %v calls=%d", err, calls)
				}
			} else if stage == "target fetch" {
				if !strings.Contains(err.Error(), "missing-native-target") || calls != 0 {
					t.Fatalf("fetch stage %v calls=%d", err, calls)
				}
			} else if calls != 1 {
				t.Fatalf("stage=%s calls=%d", stage, calls)
			}
			if stage == "hash" && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("native hash absence lost: %v", err)
			}
			if stage == "persist" {
				var pathErr *os.LinkError
				if !errors.As(err, &pathErr) || pathErr.Op != "rename" || pathErr.New != ackPath {
					t.Fatalf("real rename refusal lost: %v", err)
				}
				entries, err := os.ReadDir(filepath.Dir(ackPath))
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), ".absorbed-conflict-ack-") {
						t.Fatalf("owned staging file leaked: %s", entry.Name())
					}
				}
			}
			restore()
			verify()
			retirementOwnerReleased(t, f.githubDir, lane)
			if got := strings.TrimSpace(runEngineGit(t, r.Candidate.Worktree, "rev-parse", "HEAD")); got != beforeHead {
				t.Fatalf("candidate HEAD changed: %s", got)
			}
		}) {
			t.Fatal("shared fixture row failed; stop before another mutation")
		}
	}
}

func TestE2EAbsorbedAcknowledgementAuthenticatesExistingHistoricalSidecar(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"corrupt sidecar", "advanced target"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			f, r, o := absorbedAckOwnerFixture(t)
			verify := retirementOwnerReceiptBytes(t, r)
			ack, err := AcknowledgeAbsorbedConflict(t.Context(), o)
			if err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(ack.AcknowledgementPath)
			if err != nil {
				t.Fatal(err)
			}
			expected := original
			want := "binds different immutable evidence"
			if stage == "corrupt sidecar" {
				expected = []byte("not JSON\n")
				if err := os.WriteFile(ack.AcknowledgementPath, expected, 0600); err != nil {
					t.Fatal(err)
				}
				want = "absorbed-conflict acknowledgement"
			} else {
				writeEngineFile(t, filepath.Join(f.canonical, "later-target.txt"), "native target advance\n")
				runEngineGit(t, f.canonical, "add", "later-target.txt")
				runEngineGit(t, f.canonical, "commit", "-m", "advance target after historical acknowledgement")
				runEngineGit(t, f.canonical, "push", "origin", "main")
				advanced := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
				if advanced == ack.CurrentTargetSHA {
					t.Fatal("target did not advance")
				}
			}
			got, err := AcknowledgeAbsorbedConflict(t.Context(), o)
			if err == nil || !strings.Contains(err.Error(), want) || !reflect.DeepEqual(got, WorktreeMergeAbsorbedConflictAcknowledgement{}) {
				t.Fatalf("ack=%+v error=%v want %q", got, err, want)
			}
			after, err := os.ReadFile(ack.AcknowledgementPath)
			if err != nil || !bytes.Equal(expected, after) {
				t.Fatalf("historical sidecar overwritten: %v", err)
			}
			verify()
			retirementOwnerReleased(t, f.githubDir, r.Lane)
		})
	}
}

// restoreAbsorbedAckOwnedRemoteRef is nonfatal so an IO restorer always reaches
// its filesystem steps. The same bounded context covers native read/delete/read;
// nil means absence was actually observed, including a repeated fallback call.
func restoreAbsorbedAckOwnedRemoteRef(repo, branch string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	observe := func() (string, error) {
		result, err := defaultRunner.RunOpts(ctx, repo, runner.RunOptions{CaptureCombined: true}, "git", "ls-remote", "origin", "refs/heads/"+branch)
		if err != nil || result.ExitCode != 0 {
			return "", fmt.Errorf("inspect owned cleanup ref %s: result=%+v: %w", branch, result, err)
		}
		return strings.TrimSpace(result.CombinedOutput), nil
	}
	before, err := observe()
	if err != nil {
		return err
	}
	if before == "" {
		return nil
	}
	result, err := defaultRunner.RunOpts(ctx, repo, runner.RunOptions{CaptureCombined: true}, "git", "push", "origin", ":"+branch)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("delete owned cleanup ref %s: result=%+v: %w", branch, result, err)
	}
	after, err := observe()
	if err != nil {
		return err
	}
	if after != "" {
		return fmt.Errorf("owned cleanup ref %s survives native deletion: %s", branch, after)
	}
	return nil
}
