//go:build e2e

package worktrees

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

//nolint:paralleltest // The native Git fixture and HOME failure select process-wide environment.
func TestE2ESourceInventoryPreservesAdoptedOwnershipAndRefusesMissingOrContradictoryEvidence(t *testing.T) {
	fixture := newGitFixture(t)
	path := fixture.externalWorktree(t, "feature/source-adopted")
	adopted, err := Adopt(t.Context(), AdoptOptions{ProjectsRoot: fixture.projectsRoot, Path: path, Apply: true})
	if err != nil || len(adopted) != 1 || adopted[0].Action != AdoptAdopted {
		t.Fatalf("adopt: %+v %v", adopted, err)
	}
	head := strings.TrimSpace(gitTestOutput(t, path, "rev-parse", "HEAD"))
	options := SourceListOptions{ProjectsRoot: fixture.projectsRoot, Repository: "ACME/APP", Branch: "feature/source-adopted", HeadSHA: head}
	listed, err := ListSourceWithDiagnostics(t.Context(), options)
	if err != nil || len(listed.Diagnostics) != 0 || len(listed.Results) != 1 || !listed.Results[0].External {
		t.Fatalf("adopted producer: %+v %v", listed, err)
	}
	claim, _, _, err := activeWorkLogClaimReadOnly(fixture.home, path)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	malformed := claim
	malformed.AcquiredVia = "forged-acquisition"
	if _, _, err := sourceClaimLayout(fixture.projectsRoot, fixture.home, path, identity, malformed); err == nil {
		t.Fatal("forged source claim lineage granted placement")
	}
	common, err := os.Stat(filepath.Join(fixture.canonical, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	mismatched := options
	mismatched.Repository = "other/app"
	if _, _, err := inspectSourceRegistration(t.Context(), mismatched, fixture.projectsRoot, fixture.home, fixture.canonical, common, sourceRegistration{path: path, head: head, branch: options.Branch}); err == nil {
		t.Fatal("source claim for another repository accepted")
	}
	pointer := filepath.Join(fixture.home, "worktrees", claim.Task, "acme", "app", adoptedWorktreePointerName)
	if err := os.Rename(pointer, pointer+".retained"); err != nil {
		t.Fatal(err)
	}
	listed, err = ListSourceWithDiagnostics(t.Context(), options)
	if err != nil || len(listed.Diagnostics) != 1 || !strings.Contains(listed.Diagnostics[0].Message, "adoption registration") {
		t.Fatalf("missing adoption pointer: %+v %v", listed, err)
	}
	if err := os.Rename(pointer+".retained", pointer); err != nil {
		t.Fatal(err)
	}
	loop := filepath.Join(t.TempDir(), "home-loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", loop)
	if _, err := ListSourceWithDiagnostics(t.Context(), options); err == nil {
		t.Fatal("unreadable WB home accepted")
	}
}

//nolint:paralleltest // The native Git fixture configures process-wide environment.
func TestE2ESourceInventoryAdmitsOnlyExactSuccessfulFinalizedProducer(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(t.Context(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "finalized-source-producer",
		WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	producer := created[0]
	if err := os.WriteFile(filepath.Join(producer.WorktreeDir, "feature.txt"), []byte("reviewed source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, producer.WorktreeDir, "add", "feature.txt")
	gitTest(t, producer.WorktreeDir, "commit", "-m", "reviewed source")
	head := strings.TrimSpace(gitTestOutput(t, producer.WorktreeDir, "rev-parse", "HEAD"))
	gitTest(t, producer.WorktreeDir, "push", "-u", "origin", producer.Branch)
	if _, err := LogFinalize(t.Context(), LogFinalizeOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: producer.WorktreeDir,
		Result: "success", Report: []byte("Published for independent review; landing is pending.\n"), Apply: true,
	}); err != nil {
		t.Fatal(err)
	}
	projection, err := readWorkLogProjection(producer.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	terminalPath := filepath.Join(fixture.home, "worklogs", projection.EffortID, "runs", projection.RunID, "terminals", projection.ClaimID+".json")
	outboxPath := filepath.Join(fixture.home, "worklogs", projection.EffortID, "outbox", projection.RunID+"-"+projection.ClaimID+"-sealed.json")
	terminalBefore, err := os.ReadFile(terminalPath)
	if err != nil {
		t.Fatal(err)
	}
	outboxBefore, err := os.ReadFile(outboxPath)
	if err != nil {
		t.Fatal(err)
	}
	options := SourceListOptions{ProjectsRoot: fixture.projectsRoot, Repository: "acme/app", Branch: producer.Branch, HeadSHA: head}
	assertAdmitted := func() {
		t.Helper()
		listed, err := ListSourceWithDiagnostics(t.Context(), options)
		if err != nil || len(listed.Diagnostics) != 0 || len(listed.Results) != 1 || !listed.Results[0].Clean || listed.Results[0].HeadSHA != head {
			t.Fatalf("successful finalize source inventory = %+v, %v", listed, err)
		}
	}
	assertRefused := func(label string) {
		t.Helper()
		listed, err := ListSourceWithDiagnostics(t.Context(), options)
		if err != nil || len(listed.Diagnostics) == 0 || len(listed.Results) != 0 {
			t.Fatalf("%s admitted: %+v, %v", label, listed, err)
		}
	}
	assertAdmitted()
	for path, before := range map[string][]byte{terminalPath: terminalBefore, outboxPath: outboxBefore} {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(after, before) {
			t.Fatalf("inventory changed immutable evidence %s: %v", path, err)
		}
	}
	for _, scenario := range []struct {
		name string
		path string
		body []byte
	}{
		{"missing terminal", terminalPath, nil},
		{"corrupt terminal", terminalPath, []byte("{")},
		{"wrong commit", terminalPath, terminalWithChangedCommit(t, terminalBefore, strings.Repeat("a", 40))},
		{"successor", terminalPath, terminalWithSuccessor(t, terminalBefore)},
		{"failed result", terminalPath, terminalWithFailedResult(t, terminalBefore)},
		{"missing outbox", outboxPath, nil},
		{"corrupt outbox", outboxPath, []byte("{")},
		{"conflicting outbox", outboxPath, outboxWithChangedCommit(t, outboxBefore, strings.Repeat("a", 40))},
	} {
		//nolint:paralleltest // Scenarios sequentially mutate and restore the same immutable terminal and outbox fixtures.
		t.Run(scenario.name, func(t *testing.T) {
			original := terminalBefore
			if scenario.path == outboxPath {
				original = outboxBefore
			}
			if scenario.body == nil {
				if err := os.Remove(scenario.path); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(scenario.path, scenario.body, 0600); err != nil {
				t.Fatal(err)
			}
			assertRefused(scenario.name)
			if err := os.WriteFile(scenario.path, original, 0600); err != nil {
				t.Fatal(err)
			}
			assertAdmitted()
		})
	}
	mismatched := options
	mismatched.HeadSHA = strings.Repeat("a", 40)
	if listed, err := ListSourceWithDiagnostics(t.Context(), mismatched); err != nil || len(listed.Diagnostics) != 1 || len(listed.Results) != 0 {
		t.Fatalf("remote head mismatch admitted: %+v, %v", listed, err)
	}
	if err := os.WriteFile(filepath.Join(producer.WorktreeDir, "uncommitted.txt"), []byte("dirty"), 0600); err != nil {
		t.Fatal(err)
	}
	listed, err := ListSourceWithDiagnostics(t.Context(), options)
	if err != nil || len(listed.Results) != 1 || listed.Results[0].Clean {
		t.Fatalf("dirty source accepted as clean: %+v, %v", listed, err)
	}
}

func terminalWithChangedCommit(t *testing.T, body []byte, commit string) []byte {
	t.Helper()
	var record workLogTerminalRecord
	if err := json.Unmarshal(body, &record); err != nil {
		t.Fatal(err)
	}
	record.FinalCommit = commit
	changed, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return changed
}

func terminalWithSuccessor(t *testing.T, body []byte) []byte {
	t.Helper()
	var record workLogTerminalRecord
	if err := json.Unmarshal(body, &record); err != nil {
		t.Fatal(err)
	}
	record.SuccessorClaimID = "successor"
	changed, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return changed
}

func terminalWithFailedResult(t *testing.T, body []byte) []byte {
	t.Helper()
	var record workLogTerminalRecord
	if err := json.Unmarshal(body, &record); err != nil {
		t.Fatal(err)
	}
	record.FinalizeReport.Result = "failure"
	changed, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return changed
}

func outboxWithChangedCommit(t *testing.T, body []byte, commit string) []byte {
	t.Helper()
	var record workLogPublicEvent
	if err := json.Unmarshal(body, &record); err != nil {
		t.Fatal(err)
	}
	record.FinalCommit = commit
	changed, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return changed
}
