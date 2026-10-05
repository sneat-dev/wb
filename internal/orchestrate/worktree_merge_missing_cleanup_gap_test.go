package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestMissingCleanupOwnerEmptySelectorAndNativeHeldLane(t *testing.T) {
	t.Parallel()
	consumed := false
	_, err := acknowledgeMissingWorktreeMergeCleanup(t.Context(), WorktreeMergeMissingCleanupAcknowledgementOptions{ProjectsRoot: t.TempDir()}, defaultRunner, func(string) (WorktreeMergeReceipt, error) {
		consumed = true
		return WorktreeMergeReceipt{}, errors.New("must not read")
	}, worktreeMergeReceiptSHA256, persistMissingCleanupAcknowledgement)
	if err == nil || !strings.Contains(err.Error(), "required") || consumed {
		t.Fatalf("empty selector=%v read=%t", err, consumed)
	}
	f, r := landedFailureOwnerFixture(t)
	lock, err := AcquireOperationLock(f.githubDir, r.Lane, true)
	if err != nil {
		t.Fatal(err)
	}
	released := false
	release := func() {
		if !released {
			if err := lock.Release(); err != nil {
				t.Error(err)
				return
			}
			released = true
		}
	}
	t.Cleanup(release)
	reads := 0
	_, err = acknowledgeMissingWorktreeMergeCleanup(t.Context(), WorktreeMergeMissingCleanupAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath}, defaultRunner, func(path string) (WorktreeMergeReceipt, error) { reads++; return readWorktreeMergeReceipt(path) }, worktreeMergeReceiptSHA256, persistMissingCleanupAcknowledgement)
	if err == nil || reads != 1 {
		t.Fatalf("actual held lane=%v reads=%d", err, reads)
	}
	release()
	if !released {
		t.Fatal("owned lock not released")
	}
}

//nolint:paralleltest // Real Land/terminal fixture installs process-wide GH/PATH and WB_PROJECTS_ROOT; reversible rows reuse one native missing-cleanup baseline.
func TestMissingCleanupNativeCollisionCanonicalAndAcknowledgedTargetRefusals(t *testing.T) {
	f, r := missingCleanupOwnerFixture(t)
	o := WorktreeMergeMissingCleanupAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Actor: "reviewer", Reason: "native terminal cleanup absence", Apply: true}
	receiptBytes, err := os.ReadFile(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	// Advance the real canonical target before acknowledgement, so rewinding to
	// the landing retains genuine landing ancestry but loses acknowledged T1.
	runEngineGit(t, f.canonical, "commit", "--allow-empty", "-m", "private acknowledged target advance")
	acknowledgedTarget := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, f.canonical, "push", "origin", r.Target)
	if acknowledgedTarget == r.LandingSHA {
		t.Fatal("native target did not advance")
	}
	ack, err := AcknowledgeMissingWorktreeMergeCleanup(t.Context(), o)
	if err != nil || ack.CurrentTargetSHA != acknowledgedTarget {
		t.Fatalf("actual advance acknowledgement=%+v %v", ack, err)
	}
	ackBytes, err := os.ReadFile(ack.AcknowledgementPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"collision decode", "initial canonical", "second canonical", "acknowledged ancestry error", "acknowledged target rewind"} {
		t.Run(mode, func(t *testing.T) {
			switch mode {
			case "collision decode":
				restored := false
				restore := func() {
					if !restored {
						if err := os.WriteFile(ack.AcknowledgementPath, ackBytes, 0600); err != nil {
							t.Error(err)
							return
						}
						restored = true
					}
				}
				t.Cleanup(restore)
				if err := os.WriteFile(ack.AcknowledgementPath, []byte("{invalid"), 0600); err != nil {
					t.Fatal(err)
				}
				got, e := AcknowledgeMissingWorktreeMergeCleanup(t.Context(), o)
				damaged, e2 := os.ReadFile(ack.AcknowledgementPath)
				restore()
				if e == nil || !strings.Contains(e.Error(), "decode missing-cleanup acknowledgement") || got.ID != "" || e2 != nil || string(damaged) != "{invalid" {
					t.Fatalf("native Link collision=%+v %v retained=%q %v", got, e, damaged, e2)
				}
			case "initial canonical":
				changed := r
				changed.Repository = "invalid"
				_, e := inspectMissingWorktreeMergeCleanup(t.Context(), defaultRunner, worktreeMergeReceiptSHA256, f.githubDir, changed, o.Actor, o.Reason, 0, 0)
				if e == nil || !strings.Contains(e.Error(), "repository") {
					t.Fatalf("canonical coordinate refusal=%v", e)
				}
			case "second canonical":
				parent := filepath.Dir(f.githubDir)
				if filepath.Base(f.githubDir) != "projects" {
					t.Fatalf("fixture root is not owned projects suffix: %s", f.githubDir)
				}
				held := parent + "-held-for-canonical-observation"
				moved, blocked, installed := false, false, false
				restore := func() {
					if blocked {
						if err := os.Remove(parent); err != nil {
							t.Error(err)
							return
						}
						blocked = false
					}
					if moved {
						if err := os.Rename(held, parent); err != nil {
							t.Error(err)
							return
						}
						moved = false
					}
				}
				t.Cleanup(restore)
				consumed := false
				got, e := validateMissingCleanupAcknowledgementWithRunner(t.Context(), defaultRunner, func(path string) (string, error) {
					value, err := worktreeMergeReceiptSHA256(path)
					if err != nil {
						return value, err
					}
					if path != r.ReceiptPath || consumed {
						t.Fatalf("unexpected hash path/ordinal %s %t", path, consumed)
					}
					consumed = true
					if err := os.Rename(parent, held); err != nil {
						return "", err
					}
					moved = true
					if err := os.WriteFile(parent, []byte("private canonical ancestor blocker"), 0600); err != nil {
						return "", err
					}
					blocked = true
					installed = true
					return value, nil
				}, f.githubDir, r, ack.AcknowledgementPath, 0, 0)
				restore()
				if moved || blocked {
					t.Fatal("native fixture root restoration failed")
				}
				if !consumed || !installed || !errors.Is(e, syscall.ENOTDIR) || got.ID != ack.ID {
					t.Fatalf("late native canonical refusal=%+v %v consumed=%t", got, e, consumed)
				}
				if canonical, e := worktrees.CanonicalRepositoryPath(f.githubDir, r.Repository); e != nil || canonical != f.canonical {
					t.Fatalf("restored native root=%s %v", canonical, e)
				}
			case "acknowledged ancestry error", "acknowledged target rewind":
				restore := func() {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					result, e := defaultRunner.RunOpts(ctx, f.canonical, runner.RunOptions{CaptureCombined: true}, "git", "push", "--force", "origin", acknowledgedTarget+":refs/heads/"+r.Target)
					if e != nil || result.ExitCode != 0 {
						t.Errorf("restore native target: %+v %v", result, e)
						return
					}
					result, e = defaultRunner.RunOpts(ctx, f.canonical, runner.RunOptions{CaptureCombined: true}, "git", "ls-remote", "--heads", "origin", "refs/heads/"+r.Target)
					fields := strings.Fields(result.CombinedOutput)
					if e != nil || result.ExitCode != 0 || len(fields) != 2 || fields[0] != acknowledgedTarget {
						t.Errorf("restored native target differs: %+v %v", result, e)
					}
				}
				t.Cleanup(restore)
				runEngineGit(t, f.canonical, "push", "--force", "origin", r.LandingSHA+":refs/heads/"+r.Target)
				if contains, e := isMergeAncestor(t.Context(), f.canonical, r.LandingSHA, acknowledgedTarget); e != nil || !contains {
					t.Fatalf("genuine forward graph=%t %v", contains, e)
				}
				sentinel := errors.New("selected acknowledged target query")
				run := &landedFailureOwnerRunner{Runner: defaultRunner, path: f.canonical, args: []string{"merge-base", acknowledgedTarget, r.LandingSHA}, ordinal: 1, sentinel: sentinel}
				var got WorktreeMergeMissingCleanupAcknowledgement
				var e error
				if mode == "acknowledged ancestry error" {
					got, e = validateMissingCleanupAcknowledgementWithRunner(t.Context(), run, worktreeMergeReceiptSHA256, f.githubDir, r, ack.AcknowledgementPath, 0, 0)
				} else {
					got, e = validateMissingCleanupAcknowledgement(t.Context(), f.githubDir, r, ack.AcknowledgementPath, 0, 0)
				}
				restore()
				if got.ID != ack.ID || e == nil {
					t.Fatalf("partial authentic ACK=%+v %v", got, e)
				}
				if mode == "acknowledged ancestry error" {
					if !errors.Is(e, sentinel) || run.seen != 1 {
						t.Fatalf("exact query=%v consumed=%d", e, run.seen)
					}
				} else if !strings.Contains(e.Error(), "no longer contains acknowledged target") {
					t.Fatalf("native false ancestry=%v", e)
				}
			}
			after, err := os.ReadFile(r.ReceiptPath)
			if err != nil || string(after) != string(receiptBytes) {
				t.Fatalf("historical receipt changed: %v", err)
			}
			stored, e := readMissingCleanupAcknowledgement(ack.AcknowledgementPath, r)
			if e != nil || stored.ID != ack.ID {
				t.Fatalf("restored authentic sidecar=%+v %v", stored, e)
			}
		})
	}
}
