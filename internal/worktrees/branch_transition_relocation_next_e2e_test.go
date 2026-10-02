//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/sessionpark"
)

func TestE2EBranchTransitionSafetyAdmissionOrdering(t *testing.T) {
	t.Parallel()
	worktree := newJournalWorktree(t)
	seen := false
	reason, err := checkoutSafetyRefusalWithChecks(context.Background(), t.TempDir(), worktree, true, func(paths []string) string {
		seen = true
		if len(paths) != 1 || paths[0] != worktree {
			t.Fatalf("busy policy input = %v", paths)
		}
		// This is explicit busy-policy admission, not a Darwin native /proc claim.
		return "controlled busy observation"
	}, func(string, []string) string { t.Fatal("parked query ran after busy refusal"); return "" })
	if !seen || err != nil || reason != "controlled busy observation" {
		t.Fatalf("busy policy admission = %q, %v, seen=%v", reason, err, seen)
	}
	// Native Git operation state must outrank either later observation.
	mergeHead := filepath.Join(worktree, ".git", "MERGE_HEAD")
	if err := os.WriteFile(mergeHead, []byte(strings.Repeat("a", 40)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reason, err = checkoutSafetyRefusalWithChecks(context.Background(), t.TempDir(), worktree, true, func([]string) string { t.Fatal("busy query ran before Git-state refusal"); return "" }, func(string, []string) string { t.Fatal("parked query ran before Git-state refusal"); return "" })
	if err != nil || reason == "" {
		t.Fatalf("native Git operation precedence = %q, %v", reason, err)
	}
}

//nolint:paralleltest // inherited parked fixture configures process-wide Git and agent identity environment.
func TestE2EBranchTransitionNativeParkedCheckoutRefusal(t *testing.T) {
	fixture, worktree, _, _, bundle := newParkedNextLocalFixture(t, "transition-parked")
	if _, err := sessionpark.NewStore(filepath.Join(fixture.home, sessionpark.SourceDirName)).Create(bundle); err != nil {
		t.Fatal(err)
	}
	control := ParkedSessionReason(fixture.projectsRoot, []string{worktree})
	if control == "" {
		t.Fatal("actual parked store control has no refusal")
	}
	reason, err := checkoutSafetyRefusal(context.Background(), fixture.projectsRoot, worktree)
	if err != nil || reason != control {
		t.Fatalf("native parked checkout admission = %q, %v, control=%q", reason, err, control)
	}
	if reason, err := checkoutSafetyRefusalWithChecks(context.Background(), fixture.projectsRoot, worktree, true, func([]string) string { return "" }, ParkedSessionReason); err != nil || reason != control {
		t.Fatalf("empty busy observation lost native parked refusal: %q, %v", reason, err)
	}
}

//nolint:paralleltest // newGitFixture configures process-wide Git and WB environment.
func TestE2EBranchTransitionHeldGitLookupAdmission(t *testing.T) {
	for _, boundary := range []string{"canonical-identity", "self-lookup", "git-lookup"} {
		//nolint:paralleltest // Each case calls newGitFixture, which changes process-wide Git/WB environment.
		t.Run(boundary, func(t *testing.T) {
			fixture := newGitFixture(t)
			path := fixture.externalWorktree(t, "feature/held-git")
			held, err := openAbsoluteDirectoryNoFollow(path, false)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = held.Close() })
			self, gitPath := os.Executable, trustedGitExecutable
			var afterOpen func()
			_, cause := exec.LookPath(filepath.Join(t.TempDir(), "missing-executable"))
			if cause == nil {
				t.Fatal("actual executable lookup control succeeded")
			}
			if boundary == "self-lookup" {
				self = func() (string, error) { return "", cause }
			}
			if boundary == "git-lookup" {
				gitPath = func() (string, error) { return "", cause }
			}
			if boundary == "canonical-identity" {
				afterOpen = func() {
					gitDir := filepath.Join(fixture.canonical, ".git")
					if err := os.Rename(gitDir, gitDir+"-held"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(gitDir, 0700); err != nil {
						t.Fatal(err)
					}
				}
			}
			output, err := runSecureRenameGitWithObservation(context.Background(), fixture.canonical, filepath.Dir(path), path, held, afterOpen, self, gitPath, "status", "--porcelain=v1")
			if output != nil || err == nil {
				t.Fatalf("held native admission %s = %q, %v", boundary, output, err)
			}
			if boundary == "canonical-identity" {
				if !strings.Contains(err.Error(), "canonical repository path changed") {
					t.Fatalf("owned canonical refusal = %v", err)
				}
			} else if !errors.Is(err, cause) {
				t.Fatalf("lookup boundary lost actual cause: %v, %v", err, cause)
			}
			if boundary == "self-lookup" && !strings.Contains(err.Error(), "locate WB rename Git helper") {
				t.Fatalf("helper role diagnostic changed: %v", err)
			}
			if _, err := held.Stat(); err != nil {
				t.Fatalf("borrowed checkout descriptor consumed: %v", err)
			}
		})
	}
}

func TestE2EBranchTransitionCheckoutGitAdmission(t *testing.T) {
	t.Parallel()
	invalid := t.TempDir()
	if err := os.WriteFile(filepath.Join(invalid, ".git"), []byte("gitdir: /missing/branch-transition-admin\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if reason, err := checkoutSafetyRefusal(context.Background(), t.TempDir(), invalid); reason != "" || err == nil || !strings.Contains(err.Error(), "cannot inspect Git state") {
		t.Fatalf("native invalid checkout = %q, %v", reason, err)
	}
}
