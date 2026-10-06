//go:build e2e

package orchestrate

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

//nolint:paralleltest // existing real landing fixture changes hosted-provider PATH/XDG state; cleanup records and lock inodes remain private
func TestE2ELandOwnerResumedCleanupTailPreservesNativeTerminalEvidenceOnFault(t *testing.T) {
	for _, stage := range []string{"terminal recovery save", "owned release", "native completion save"} {
		//nolint:paralleltest // Process-wide environment changes in installWorktreeMergeDirectGH, landedTerminalCleanupFixture, newEngineFixtureOnBranch; these rows share their parent environment and remain sequential.
		t.Run(stage, func(t *testing.T) {
			if stage == "owned release" && runtime.GOOS == "windows" {
				t.Skip("owned open lock inode replacement is Unix-only")
			}
			fixture, _, receipt, _ := landedTerminalCleanupFixture(t)
			if stage == "terminal recovery save" {
				externallyTerminalizeMergeCleanup(t, fixture, &receipt)
			}
			home, err := wbhome.Root(fixture.githubDir)
			if err != nil {
				t.Fatal(err)
			}
			lockPath := filepath.Join(home, "worktrees", receipt.Lane, ".lock")
			successor := []byte("owned resumed-cleanup successor\n")
			sentinel := errors.New("selected resumed " + stage + " refusal")
			refused, replaced := false, false
			durable, err := os.ReadFile(receipt.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			got, runErr := landWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Cleanup: true, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond, WaitSlice: 5 * time.Second, Progress: func(e progress.Event) {
				if stage == "owned release" && e.Phase == "cleanup" && e.State == progress.Started && !replaced {
					if err := os.Rename(lockPath, lockPath+".owned-preserved"); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(lockPath, successor, 0600); err != nil {
						t.Fatal(err)
					}
					replaced = true
				}
			}}, func(r WorktreeMergeReceipt) error {
				if stage != "owned release" && r.Status == WorktreeMergeComplete {
					refused = true
					if stage == "native completion save" {
						stored, err := readWorktreeMergeReceipt(r.ReceiptPath)
						if err != nil {
							t.Fatal(err)
						}
						if stored.Status != WorktreeMergeLanded || stored.LandingSHA != receipt.LandingSHA {
							t.Fatalf("native nested cleanup checkpoint missing: %+v", stored)
						}
						tasks := append([]string(nil), stored.CleanedTasks...)
						sort.Strings(tasks)
						if !reflect.DeepEqual(tasks, sortedUniqueMergeTasks(receipt)) || len(stored.CleanupReports) != len(tasks) {
							t.Fatalf("actual nested task/report set incomplete: %+v", stored)
						}
						if err := worktrees.ValidateTerminalCleanupReports(stored.CleanupReports, stored.Repository, tasks); err != nil {
							t.Fatalf("actual nested terminal reports: %v", err)
						}
						durable, err = os.ReadFile(r.ReceiptPath)
						if err != nil {
							t.Fatal(err)
						}
					}
					return sentinel
				}
				if err := persistWorktreeMergeReceipt(r); err != nil {
					return err
				}
				var err error
				durable, err = os.ReadFile(r.ReceiptPath)
				return err
			})
			if stage == "owned release" {
				if !replaced || runErr == nil || !strings.Contains(runErr.Error(), "operation lock changed before retirement") || got.Status != WorktreeMergeLanded {
					t.Fatalf("actual resumed release refusal=%+v %v replaced=%v", got, runErr, replaced)
				}
				if b, err := os.ReadFile(lockPath); err != nil || string(b) != string(successor) {
					t.Fatalf("successor changed: %q %v", b, err)
				}
				for _, p := range []string{receipt.Candidate.Worktree, receipt.Sources[0].Worktree} {
					if _, err := os.Stat(p); err != nil {
						t.Fatalf("release refusal removed native asset %s: %v", p, err)
					}
				}
				if len(got.CleanedTasks) != 0 {
					t.Fatalf("release refusal recorded cleanup: %+v", got)
				}
			} else {
				if !refused || !errors.Is(runErr, sentinel) || got.Status != WorktreeMergeComplete {
					t.Fatalf("actual resumed completion save refusal=%+v %v refused=%v", got, runErr, refused)
				}
				tasks := append([]string(nil), got.CleanedTasks...)
				sort.Strings(tasks)
				if !reflect.DeepEqual(tasks, sortedUniqueMergeTasks(receipt)) {
					t.Fatalf("exact completed native task set=%+v", got)
				}
				if stage == "terminal recovery save" {
					expectations, err := terminalWorkLogExpectations(receipt)
					if err != nil {
						t.Fatal(err)
					}
					if err := worktrees.ValidateRemovedTerminalWorkLogs(fixture.githubDir, expectations); err != nil {
						t.Fatalf("actual sealed native terminal evidence: %v", err)
					}
				} else {
					if len(got.CleanupReports) != len(tasks) {
						t.Fatalf("exact native cleanup report set=%+v", got)
					}
					if err := worktrees.ValidateTerminalCleanupReports(got.CleanupReports, got.Repository, tasks); err != nil {
						t.Fatalf("completed native terminal reports: %v", err)
					}
				}
				for _, p := range []string{receipt.Candidate.Worktree, receipt.Sources[0].Worktree} {
					if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("native cleaned asset remains %s: %v", p, err)
					}
				}
			}
			if b, err := os.ReadFile(receipt.ReceiptPath); err != nil || string(b) != string(durable) {
				t.Fatalf("selected refusal replaced last genuine durable checkpoint: %v", err)
			}
			if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); remote != receipt.LandingSHA {
				t.Fatalf("cleanup tail changed actual landed target: %s", remote)
			}
		})
	}
}
