package worktrees

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

//nolint:paralleltest // native Git/WB fixture owns process-wide environment.
func TestAdoptedTerminalHistoryRequiresCompletedNativeCleanupAndPreservesEvidence(t *testing.T) {
	fixture := newGitFixture(t)
	task, path := adoptAndCommit(t, fixture, "feature/adopt-terminal-history", "feature.txt")
	claim, _, _, err := activeWorkLogClaimReadOnly(fixture.home, path)
	if err != nil {
		t.Fatal(err)
	}
	claimPath := filepath.Join(fixture.home, "worklogs", claim.EffortID, "runs", claim.RunID, "claims", claim.ClaimID+".json")
	before, err := os.ReadFile(claimPath)
	if err != nil {
		t.Fatal(err)
	}
	if completed, err := completedAdoptedHistory(fixture.home, fixture.projectsRoot, claim); err != nil || completed {
		t.Fatalf("live adopted history became terminal: %t %v", completed, err)
	}
	head := gitTestOutput(t, path, "rev-parse", "HEAD")
	gitTest(t, path, "push", "-u", "origin", claim.Branch)
	gitTest(t, fixture.canonical, "merge", "--no-ff", claim.Branch, "-m", "merge adopted terminal")
	gitTest(t, fixture.canonical, "push", "origin", "main")
	installMergedPullRequestFixture(t, head, time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC))
	outcome, err := Cleanup(context.Background(), CleanupOptions{ProjectsRoot: fixture.projectsRoot, Task: task, Base: "main", Apply: true, Now: func() time.Time { return time.Date(2026, time.July, 3, 12, 0, 0, 0, time.UTC) }})
	if err != nil || len(outcome.Results) != 1 || !outcome.Results[0].Applied {
		t.Fatalf("native cleanup: %+v %v", outcome, err)
	}
	for i := 0; i < 2; i++ {
		completed, err := completedAdoptedHistory(fixture.home, fixture.projectsRoot, claim)
		listed, listErr := ListWithDiagnostics(context.Background(), ListOptions{ProjectsRoot: fixture.projectsRoot, Task: task})
		if err != nil || !completed || listErr != nil || len(listed.Results) != 0 || len(listed.Diagnostics) != 0 {
			t.Fatalf("completed history obstructs repeated inventory: %t %v %+v %v", completed, err, listed, listErr)
		}
	}
	terminalPath := filepath.Join(fixture.home, "worklogs", claim.EffortID, "runs", claim.RunID, "terminals", claim.ClaimID+".json")
	terminalBytes, err := os.ReadFile(terminalPath)
	if err != nil {
		t.Fatal(err)
	}
	entry := ListResult{Task: task, Repository: claim.Repository, CanonicalDir: fixture.canonical, WorktreeDir: path, Branch: claim.Branch, HeadSHA: head}
	backlogPath := lifecycleBacklogPath(fixture.home, lifecycleBacklogID(entry, "removed"))
	backlogBytes, err := os.ReadFile(backlogPath)
	if err != nil {
		t.Fatal(err)
	}
	outboxPath := filepath.Join(fixture.home, "worklogs", claim.EffortID, "outbox", claim.RunID+"-"+claim.ClaimID+"-sealed.json")
	evidencePaths := []string{claimPath, terminalPath, outboxPath, backlogPath}
	originalEvidence := make(map[string][]byte, len(evidencePaths))
	for _, evidencePath := range evidencePaths {
		raw, err := os.ReadFile(evidencePath)
		if err != nil {
			t.Fatal(err)
		}
		originalEvidence[evidencePath] = raw
	}
	for _, kind := range []string{"pass-through", "run-disappears-after-relocation"} {
		t.Run(kind, func(t *testing.T) {
			history := terminalHistoryPorts()
			realOpenRun := history.OpenRun
			runRoot := filepath.Dir(filepath.Dir(terminalPath))
			retained := runRoot + ".retained-race"
			moved := false
			t.Cleanup(func() {
				if moved {
					if err := os.Rename(retained, runRoot); err != nil {
						t.Error(err)
					}
				}
			})
			calls := 0
			history.OpenRun = func(home, effort, run string, create bool) (*os.File, string, error) {
				calls++
				if home != fixture.home || effort != claim.EffortID || run != claim.RunID || create {
					t.Fatalf("changed native read identity: %q %q %q create=%t", home, effort, run, create)
				}
				if kind == "run-disappears-after-relocation" && calls == 1 {
					if err := os.Rename(runRoot, retained); err != nil {
						t.Fatal(err)
					}
					moved = true
				}
				return realOpenRun(home, effort, run, create)
			}
			completed, proofErr := completedAdoptedHistoryWithHistoryPorts(fixture.home, fixture.projectsRoot, claim, history)
			if kind == "run-disappears-after-relocation" {
				if completed || !errors.Is(proofErr, os.ErrNotExist) || calls != 1 {
					t.Fatalf("real vanished run granted authority: completed=%t calls=%d err=%v", completed, calls, proofErr)
				}
				if _, err := os.Lstat(runRoot); !os.IsNotExist(err) {
					t.Fatalf("read recreated private run: %v", err)
				}
				retainedClaim, err := os.ReadFile(filepath.Join(retained, "claims", claim.ClaimID+".json"))
				if err != nil || !bytes.Equal(retainedClaim, before) {
					t.Fatalf("retained immutable claim changed: %v", err)
				}
				if err := os.Rename(retained, runRoot); err != nil {
					t.Fatal(err)
				}
				moved = false
			} else if proofErr != nil || !completed || calls != 2 {
				t.Fatalf("same native port did not cover reopen and history validation: completed=%t calls=%d err=%v", completed, calls, proofErr)
			}
			for _, evidencePath := range evidencePaths {
				after, err := os.ReadFile(evidencePath)
				if err != nil || !bytes.Equal(originalEvidence[evidencePath], after) {
					t.Fatalf("native evidence changed at %s: %v", evidencePath, err)
				}
			}
		})
	}
	for _, kind := range []string{"missing-terminal", "corrupt-terminal", "unfinished-backlog", "missing-outbox", "corrupt-outbox", "missing-backlog", "corrupt-backlog", "wrong-backlog-identity", "corrupt-relocation-journal", "enotdir-checkout-parent", "missing-backlog-directory", "inaccessible-checkout-parent", "ambiguous-canonical-stores"} {
		t.Run(kind, func(t *testing.T) {
			switch kind {
			case "ambiguous-canonical-stores":
				// Two genuine native clones make the unqualified coordinate
				// ambiguous at the owning repository-path boundary.
				for _, host := range []string{"github.com", "gitlab.example.com"} {
					clone := filepath.Join(fixture.projectsRoot, host, "acme", "app")
					if err := os.MkdirAll(filepath.Dir(clone), 0700); err != nil {
						t.Fatal(err)
					}
					gitTest(t, fixture.projectsRoot, "clone", fixture.remote, clone)
					t.Cleanup(func() {
						if err := os.RemoveAll(filepath.Join(fixture.projectsRoot, host)); err != nil {
							t.Error(err)
						}
					})
				}

			case "missing-outbox", "corrupt-outbox":
				out := filepath.Join(fixture.home, "worklogs", claim.EffortID, "outbox", claim.RunID+"-"+claim.ClaimID+"-sealed.json")
				raw, e := os.ReadFile(out)
				if e != nil {
					t.Fatal(e)
				}
				t.Cleanup(func() { _ = os.WriteFile(out, raw, 0600) })
				if kind == "missing-outbox" {
					e = os.Remove(out)
				} else {
					e = os.WriteFile(out, []byte("{"), 0600)
				}
				if e != nil {
					t.Fatal(e)
				}
			case "missing-backlog-directory":
				d := filepath.Dir(backlogPath)
				saved := d + ".review-saved"
				if e := os.Rename(d, saved); e != nil {
					t.Fatal(e)
				}
				t.Cleanup(func() { _ = os.Rename(saved, d) })
			case "inaccessible-checkout-parent":
				parent := filepath.Dir(path)
				if e := os.Chmod(parent, 0000); e != nil {
					t.Fatal(e)
				}
				t.Cleanup(func() { _ = os.Chmod(parent, 0700) })
			case "missing-backlog":
				if e := os.Remove(backlogPath); e != nil {
					t.Fatal(e)
				}
			case "corrupt-backlog":
				if e := os.WriteFile(backlogPath, []byte("{"), 0600); e != nil {
					t.Fatal(e)
				}
			case "wrong-backlog-identity":
				var b lifecycleBacklogRecord
				if e := json.Unmarshal(backlogBytes, &b); e != nil {
					t.Fatal(e)
				}
				b.HeadSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
				raw, e := json.Marshal(b)
				if e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(backlogPath, raw, 0600); e != nil {
					t.Fatal(e)
				}
			case "corrupt-relocation-journal":
				journal := filepath.Join(fixture.home, "worklogs", claim.EffortID, "runs", claim.RunID, "relocations")
				if e := os.MkdirAll(journal, 0700); e != nil {
					t.Fatal(e)
				}
				bad := filepath.Join(journal, claim.ClaimID+"-review-corrupt.intent.json")
				if e := os.WriteFile(bad, []byte("{"), 0600); e != nil {
					t.Fatal(e)
				}
				t.Cleanup(func() { _ = os.Remove(bad) })
			case "enotdir-checkout-parent":
				parent := filepath.Dir(path)
				saved := parent + ".review-saved"
				if e := os.Rename(parent, saved); e != nil {
					t.Fatal(e)
				}
				if e := os.WriteFile(parent, []byte("obstruction"), 0600); e != nil {
					t.Fatal(e)
				}
				t.Cleanup(func() { _ = os.Remove(parent); _ = os.Rename(saved, parent) })
			case "missing-terminal":
				if err := os.Remove(terminalPath); err != nil {
					t.Fatal(err)
				}
			case "corrupt-terminal":
				if err := os.WriteFile(terminalPath, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unfinished-backlog":
				var record lifecycleBacklogRecord
				if err := json.Unmarshal(backlogBytes, &record); err != nil {
					t.Fatal(err)
				}
				record.Stage = lifecycleStageRemovingLocalBranch
				raw, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(backlogPath, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			completed, proofErr := completedAdoptedHistory(fixture.home, fixture.projectsRoot, claim)
			var listed ListOutcome
			var listErr error
			if kind == "ambiguous-canonical-stores" {
				results, diagnostics := listTaskScopedClaimedRegistryWorktrees(context.Background(), fixture.projectsRoot, fixture.home, nil, map[string]bool{task: true}, "main", "", "", false, 1, nil, inspectPolicy{})
				listed.Results, listed.Diagnostics = results, diagnostics
				if proofErr == nil || !strings.Contains(proofErr.Error(), "more than one host") {
					t.Fatalf("canonical ambiguity lost exact refusal: %v", proofErr)
				}
			} else {
				listed, listErr = ListWithDiagnostics(context.Background(), ListOptions{ProjectsRoot: fixture.projectsRoot, Task: task})
			}
			if completed || proofErr == nil || listErr != nil || len(listed.Results) != 0 || len(listed.Diagnostics) != 1 || listed.Diagnostics[0].DiscoveryScope != "selected-proof" || listed.Diagnostics[0].Task != task {
				t.Fatalf("invalid terminal acquired authority: %t %v %+v %v", completed, proofErr, listed, listErr)
			}
			if err := os.WriteFile(terminalPath, terminalBytes, 0600); err != nil {
				t.Fatal(err)
			}
			if kind != "missing-backlog-directory" {
				if err := os.WriteFile(backlogPath, backlogBytes, 0600); err != nil {
					t.Fatal(err)
				}
			}
			after, err := os.ReadFile(claimPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("immutable claim changed: %v", err)
			}
			if _, err := os.Stat(path); kind != "enotdir-checkout-parent" && kind != "inaccessible-checkout-parent" && !os.IsNotExist(err) {
				t.Fatalf("history recreated checkout: %v", err)
			}
		})
	}
}

//nolint:paralleltest // native fixture owns process-wide Git/WB settings.
func TestAbsentActiveAdoptedCheckoutCannotBecomeCompletedHistory(t *testing.T) {
	fixture := newGitFixture(t)
	task, path := adoptAndCommit(t, fixture, "feature/unsealed-adopted-history", "feature.txt")
	claim, _, _, err := activeWorkLogClaimReadOnly(fixture.home, path)
	if err != nil {
		t.Fatal(err)
	}
	claimPath := filepath.Join(fixture.home, "worklogs", claim.EffortID, "runs", claim.RunID, "claims", claim.ClaimID+".json")
	before, err := os.ReadFile(claimPath)
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "worktree", "remove", "--force", path)
	completed, proofErr := completedAdoptedHistory(fixture.home, fixture.projectsRoot, claim)
	results, diagnostics := listTaskScopedClaimedRegistryWorktrees(context.Background(), fixture.projectsRoot, fixture.home, nil, map[string]bool{task: true}, "main", "", "", false, 1, nil, inspectPolicy{})
	if completed || proofErr == nil || len(results) != 0 || len(diagnostics) != 1 || diagnostics[0].Task != task || diagnostics[0].DiscoveryScope != "selected-proof" {
		t.Fatalf("unsealed absence gained authority: %t %v %+v %+v", completed, proofErr, results, diagnostics)
	}
	after, err := os.ReadFile(claimPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("active immutable claim changed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.home, "worklogs", claim.EffortID, "runs", claim.RunID, "terminals", claim.ClaimID+".json")); !os.IsNotExist(err) {
		t.Fatalf("discovery fabricated terminal: %v", err)
	}
}
