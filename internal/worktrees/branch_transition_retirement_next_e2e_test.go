//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestE2EBranchTransitionForkQueryNativeRefusal(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "absent-working-directory")
	// Execute must fail before gh can run: its native child chdir is impossible.
	if err := reviewedRemoteForkGuard(context.Background(), missing, "acme/app"); err == nil || !strings.Contains(err.Error(), "query repository fork status") {
		t.Fatalf("native observer execution refusal = %v", err)
	}
}

func TestE2EBranchTransitionReportSyncRefusals(t *testing.T) {
	t.Parallel()
	for _, final := range []bool{false, true} {
		name := "ancestry"
		if final {
			name = "after-publication"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := filepath.Join(t.TempDir(), "report")
			var owned *os.File
			nativeSync := func(path string) error {
				return syncDirectoryWithObservation(path, func(file *os.File) {
					owned = file
					if err := file.Close(); err != nil {
						t.Fatal(err)
					}
				})
			}
			ancestry, finish := syncDirectoryAndAncestors, syncDirectory
			if final {
				finish = nativeSync
			} else {
				ancestry = nativeSync
			}
			got, err := writeBranchCleanupReportWithSync(root, BranchCleanupOptions{Base: "main"}, time.Now(), nil, nil, ancestry, finish)
			if got != "" || !errors.Is(err, os.ErrClosed) || owned == nil {
				t.Fatalf("native closed sync = %q, %v, owned=%v", got, err, owned)
			}
			if _, err := owned.Stat(); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("sync failure leaked descriptor: %v", err)
			}
			raw, readErr := os.ReadFile(filepath.Join(root, "cleanup.json"))
			if final {
				if readErr != nil || !strings.Contains(string(raw), `"base": "main"`) {
					t.Fatalf("durable report lost after sync refusal: %q, %v", raw, readErr)
				}
			} else if !errors.Is(readErr, os.ErrNotExist) {
				t.Fatalf("ancestry refusal published a report: %v", readErr)
			}
		})
	}
}

//nolint:paralleltest // newGitFixture configures process-wide Git and WB environment.
func TestE2EBranchTransitionPatchProofRefusals(t *testing.T) {
	for _, boundary := range []string{"patch-id", "sealed-log", "landed-log"} {
		//nolint:paralleltest // Each case calls newGitFixture, which changes process-wide Git/WB environment.
		t.Run(boundary, func(t *testing.T) {
			fixture := newGitFixture(t)
			repository := fixture.canonical
			base := gitTestOutput(t, repository, "rev-parse", "HEAD")
			sealed := writeAndCommit(t, repository, "transition.txt", "patch\n", "transition")
			commands := []string{}
			observer := func(cmd *exec.Cmd) {
				operation := cmd.Args[3]
				commands = append(commands, operation)
				shouldDamage := boundary == "patch-id" && operation == "patch-id" || boundary == "sealed-log" && len(commands) == 1 || boundary == "landed-log" && len(commands) == 3
				if shouldDamage {
					if err := os.Rename(filepath.Join(repository, ".git"), filepath.Join(repository, ".git-held")); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(repository, ".git"), []byte("gitdir: /missing/transition-patch-admin\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if boundary == "patch-id" {
				ids, err := commitPatchIDs(context.Background(), repository, base+".."+sealed, observer)
				if ids != nil || err == nil || !strings.Contains(err.Error(), "compute patch-ids") || !reflect.DeepEqual(commands, []string{"log", "patch-id"}) {
					t.Fatalf("native second command refusal = %v, %v, %v", ids, err, commands)
				}
			} else {
				// Replay the same patch on a divergent native branch so the merge-base
				// remains the original base and both patch proofs execute.
				gitTest(t, repository, "checkout", "-b", "transition-current", base)
				current := writeAndCommit(t, repository, "transition.txt", "patch\n", "replayed transition")
				shared, err := commitsShareEveryPatchIDWithCommandObservation(context.Background(), repository, sealed, current, observer)
				if shared || err == nil || !strings.Contains(err.Error(), "list patches") {
					t.Fatalf("native %s refusal = %v, %v, %v", boundary, shared, err, commands)
				}
				want := []string{"log"}
				if boundary == "landed-log" {
					want = []string{"log", "patch-id", "log"}
				}
				if !reflect.DeepEqual(commands, want) {
					t.Fatalf("patch proof order = %v, want %v", commands, want)
				}
			}
		})
	}
}

//nolint:paralleltest // newGitFixture configures process-wide Git and WB environment.
func TestE2EBranchTransitionRetirementAuthorityRechecks(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "transition-claimed", WorkLog: WorkLogOptions{Model: "unknown"}})
	if err != nil || len(created) != 1 {
		t.Fatalf("create native live claim = %v, %v", created, err)
	}
	// The native linked registration is enumerated first, but the actual live
	// Work Log inventory refusal takes precedence over its checked-out map.
	result := BranchCleanupResult{BranchEntry: BranchEntry{Repository: "acme/app", Branch: created[0].Branch, Base: "main"}}
	if ok := recheckBranchRetirementGuards(context.Background(), fixture.projectsRoot, fixture.canonical, &result); ok || result.Outcome != "failed" || !strings.Contains(result.Error, "live WB work log") {
		t.Fatalf("live claim recheck = %v, %+v", ok, result)
	}
	// Corrupt a native claim namespace directory after establishing the valid
	// case; enumeration diagnostics must refuse before any branch mutation.
	blocker := filepath.Join(os.Getenv("HOME"), ".wb")
	if err := os.Symlink(".wb", blocker); err != nil {
		t.Fatal(err)
	}
	result = BranchCleanupResult{BranchEntry: BranchEntry{Repository: "acme/app", Branch: "unclaimed", Base: "main"}}
	if ok := recheckBranchRetirementGuards(context.Background(), fixture.projectsRoot, fixture.canonical, &result); ok || result.Outcome != "failed" || result.Error == "" {
		t.Fatalf("native unreadable claims recheck = %v, %+v", ok, result)
	}
}
