package worktreeclaims

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/worktreelayout"
	"github.com/sneat-dev/wb/internal/worktreeproof"
	"github.com/sneat-dev/wb/internal/worktreesecure"
)

func terminalTestPorts() TerminalPorts {
	return TerminalPorts{
		OpenPrivateChild: func(parent *os.File, name string, create bool) (*os.File, error) {
			return worktreesecure.OpenPrivateChild(parent, name, create, worktreelayout.ValidSafeSegment)
		},
		ReadJSONAt: filewrite.ReadJSONAt,
		WriteJSONImmutable: func(directory *os.File, name string, value any, idempotent bool) error {
			return filewrite.WriteJSONImmutableAt(directory, name, value, idempotent, nil)
		},
		OpenOutbox: func(home, effort string, create bool) (*os.File, error) {
			return OpenWorkLogOutbox(home, effort, create, worktreelayout.ValidSafeSegment)
		},
		Now: func() time.Time { return time.Unix(123, 0) },
	}
}

func terminalTestRun(t *testing.T) (string, *os.File, TerminalSealRequest) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := OpenWorkLogRun(home, "task", "run", true, worktreelayout.ValidSafeSegment)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Close() })
	return home, run, TerminalSealRequest{
		Claim: Claim{Version: 2, EffortID: "task", RunID: "run", ClaimID: strings.Repeat("a", 64),
			Repository: "acme/app", Branch: "wb/task", Base: "main", BaseSHA: strings.Repeat("b", 40), Lifecycle: "active"},
		FinalCommit: strings.Repeat("c", 40), Disposition: "landed",
	}
}

func TestTerminalSealPublishesFlattenedRecordAndStableRetry(t *testing.T) {
	t.Parallel()
	home, run, request := terminalTestRun(t)
	ports := terminalTestPorts()
	request.Evidence.FinalizeReport = &FinalizeReport{Result: "success", ReportPath: filepath.Join(home, "private.md")}
	first, err := ports.SealTerminal(home, run, request)
	if err != nil {
		t.Fatal(err)
	}
	terminalPath := filepath.Join(home, "worklogs", "task", "runs", "run", "terminals", request.Claim.ClaimID+".json")
	content, err := os.ReadFile(terminalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(content, []byte(`"claim_id"`)) || bytes.Contains(content, []byte(`"Claim"`)) {
		t.Fatalf("terminal lost its flattened claim JSON: %s", content)
	}
	ports.Now = func() time.Time { return first.Add(time.Hour) }
	again, err := ports.SealTerminal(home, run, request)
	if err != nil || !again.Equal(first) {
		t.Fatalf("retry seal = %s, %v; want %s", again, err, first)
	}
	mutated := request
	mutated.FinalCommit = strings.Repeat("d", 40)
	if _, err := ports.SealTerminal(home, run, mutated); !errors.Is(err, ErrImmutableTerminalConflict) {
		t.Fatalf("changed terminal transition: %v", err)
	}
	mutated = request
	mutated.Evidence.FinalizeReport = &FinalizeReport{Result: "failure"}
	if _, err := ports.SealTerminal(home, run, mutated); !errors.Is(err, ErrImmutableTerminalConflict) {
		t.Fatalf("changed finalize report: %v", err)
	}
}

func TestOrphanedSealRequiresNegativeEvidenceBeforeStorage(t *testing.T) {
	t.Parallel()
	home, run, request := terminalTestRun(t)
	request.Disposition, request.FinalCommit = "orphaned", ""
	ports := terminalTestPorts()
	ports.OpenPrivateChild = func(*os.File, string, bool) (*os.File, error) {
		t.Fatal("invalid orphaned request reached storage")
		return nil, nil
	}
	if _, err := ports.SealTerminal(home, run, request); err == nil {
		t.Fatal("orphaned seal accepted nil negative proof")
	}
	request.Evidence.Orphaned = &worktreeproof.OrphanedEvidence{Version: 1, Actor: "operator", Reason: "missing",
		WorktreeAbsent: true, RegistrationAbsent: true, LocalBranchAbsent: true, RemoteBranchAbsent: true, TerminalAbsent: true}
	ports = terminalTestPorts()
	if _, err := ports.SealTerminal(home, run, request); err != nil {
		t.Fatal(err)
	}
	publicPath := filepath.Join(home, "worklogs", "task", "outbox", "run-"+request.Claim.ClaimID+"-sealed.json")
	data, err := os.ReadFile(publicPath)
	if err != nil {
		t.Fatal(err)
	}
	var event PublicEvent
	if err := json.Unmarshal(data, &event); err != nil || event.Disposition != "orphaned" || bytes.Contains(data, []byte(`"orphaned_evidence"`)) {
		t.Fatalf("public orphaned receipt leaked private evidence: %s, %v", data, err)
	}
}

func TestTerminalSealStorageFailuresAndRetryConflicts(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"open terminal", "inspect terminal", "write terminal", "open outbox", "write outbox"} {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			home, run, request := terminalTestRun(t)
			ports := terminalTestPorts()
			failure := errors.New(stage)
			switch stage {
			case "open terminal":
				ports.OpenPrivateChild = func(*os.File, string, bool) (*os.File, error) { return nil, failure }
			case "inspect terminal":
				ports.ReadJSONAt = func(*os.File, string, any) error { return failure }
			case "write terminal":
				ports.WriteJSONImmutable = func(*os.File, string, any, bool) error { return failure }
			case "open outbox":
				ports.OpenOutbox = func(string, string, bool) (*os.File, error) { return nil, failure }
			case "write outbox":
				ports.WriteJSONImmutable = func(_ *os.File, _ string, _ any, idempotent bool) error {
					if idempotent {
						return failure
					}
					return nil
				}
			}
			if _, err := ports.SealTerminal(home, run, request); !errors.Is(err, failure) {
				t.Fatalf("%s error = %v", stage, err)
			}
		})
	}
}

func TestTerminalEvidenceEqualityPreservesOptionalFields(t *testing.T) {
	t.Parallel()
	report := &FinalizeReport{Result: "success"}
	if !SameFinalizeReport(nil, nil) || SameFinalizeReport(report, nil) || SameFinalizeReport(nil, report) ||
		!SameFinalizeReport(report, &FinalizeReport{Result: "success"}) || SameFinalizeReport(report, &FinalizeReport{Result: "failed"}) {
		t.Fatal("finalize report equality")
	}
	orphaned := &worktreeproof.OrphanedEvidence{Version: 1}
	if !SameOrphanedEvidence(nil, nil) || SameOrphanedEvidence(orphaned, nil) || SameOrphanedEvidence(nil, orphaned) ||
		!SameOrphanedEvidence(orphaned, &worktreeproof.OrphanedEvidence{Version: 1}) || SameOrphanedEvidence(orphaned, &worktreeproof.OrphanedEvidence{Version: 2}) {
		t.Fatal("orphaned evidence equality")
	}
	dirty := &worktreeproof.DirtyWorktreeEvidence{SHA256: "digest"}
	if !SameDirtyWorktreeEvidence(nil, nil) || SameDirtyWorktreeEvidence(dirty, nil) || SameDirtyWorktreeEvidence(nil, dirty) ||
		!SameDirtyWorktreeEvidence(dirty, &worktreeproof.DirtyWorktreeEvidence{SHA256: "digest"}) || SameDirtyWorktreeEvidence(dirty, &worktreeproof.DirtyWorktreeEvidence{SHA256: "other"}) {
		t.Fatal("dirty capture evidence equality")
	}
	handoff := &ExternalHandoffEvidence{Version: 1}
	if !sameExternalHandoff(nil, nil) || sameExternalHandoff(handoff, nil) || sameExternalHandoff(nil, handoff) ||
		!sameExternalHandoff(handoff, &ExternalHandoffEvidence{Version: 1}) || sameExternalHandoff(handoff, &ExternalHandoffEvidence{Version: 2}) {
		t.Fatal("external handoff evidence equality")
	}
}

func landedTestEvidence() *LandedEvidence {
	return &LandedEvidence{Target: "main", LandedSHA: strings.Repeat("d", 40), Proof: LandedProofMergedPullRequest, PullRequest: 17}
}

func TestLandedSealRecordsItsTargetAndCommitInTheTerminalAndTheOutbox(t *testing.T) {
	t.Parallel()
	home, run, request := terminalTestRun(t)
	ports := terminalTestPorts()
	request.Evidence.Landed = landedTestEvidence()
	first, err := ports.SealTerminal(home, run, request)
	if err != nil {
		t.Fatal(err)
	}
	var terminal TerminalRecord
	readTerminalTestJSON(t, filepath.Join(home, "worklogs", "task", "runs", "run", "terminals", request.Claim.ClaimID+".json"), &terminal)
	if terminal.Disposition != "landed" || !SameLandedEvidence(terminal.Landed, landedTestEvidence()) {
		t.Fatalf("terminal = %q with %#v, want landed with its proof", terminal.Disposition, terminal.Landed)
	}
	var event PublicEvent
	readTerminalTestJSON(t, filepath.Join(home, "worklogs", "task", "outbox", "run-"+request.Claim.ClaimID+"-sealed.json"), &event)
	if event.Disposition != "landed" || !SameLandedEvidence(event.Landed, landedTestEvidence()) {
		t.Fatalf("outbox = %q with %#v, want landed with its proof", event.Disposition, event.Landed)
	}
	again, err := ports.SealTerminal(home, run, request)
	if err != nil || !again.Equal(first) {
		t.Fatalf("retry seal = %s, %v; want %s", again, err, first)
	}
	moved := request
	moved.Evidence.Landed = landedTestEvidence()
	moved.Evidence.Landed.Target = "release"
	if _, err := ports.SealTerminal(home, run, moved); !errors.Is(err, ErrImmutableTerminalConflict) {
		t.Fatalf("a different landing over a sealed one: %v", err)
	}
}

func TestLandedEvidenceIsRefusedOnAnotherDispositionOrWhenIncomplete(t *testing.T) {
	t.Parallel()
	edits := map[string]func(*TerminalSealRequest){
		"removed disposition": func(request *TerminalSealRequest) { request.Disposition = "removed" },
		"no target":           func(request *TerminalSealRequest) { request.Evidence.Landed.Target = "" },
		"short commit":        func(request *TerminalSealRequest) { request.Evidence.Landed.LandedSHA = "abc" },
		"unknown proof":       func(request *TerminalSealRequest) { request.Evidence.Landed.Proof = "guessed" },
		"negative number":     func(request *TerminalSealRequest) { request.Evidence.Landed.PullRequest = -1 },
	}
	for name, edit := range edits {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			home, run, request := terminalTestRun(t)
			request.Evidence.Landed = landedTestEvidence()
			edit(&request)
			ports := terminalTestPorts()
			ports.OpenPrivateChild = func(*os.File, string, bool) (*os.File, error) {
				t.Fatal("an invalid landing reached storage")
				return nil, nil
			}
			if _, err := ports.SealTerminal(home, run, request); err == nil {
				t.Fatal("seal accepted an invalid landing")
			}
		})
	}
}

func TestLandedEvidenceValidityAndEquality(t *testing.T) {
	t.Parallel()
	for _, proof := range []string{LandedProofContained, LandedProofMergedPullRequest, LandedProofRebaseMerged, LandedProofAbsorbed} {
		evidence := landedTestEvidence()
		evidence.Proof = proof
		if !ValidLandedEvidence(evidence) {
			t.Fatalf("proof %q is not valid", proof)
		}
	}
	if ValidLandedEvidence(nil) {
		t.Fatal("no evidence is valid")
	}
	other := landedTestEvidence()
	other.PullRequest = 18
	if !SameLandedEvidence(nil, nil) || SameLandedEvidence(landedTestEvidence(), nil) || SameLandedEvidence(nil, landedTestEvidence()) ||
		!SameLandedEvidence(landedTestEvidence(), landedTestEvidence()) || SameLandedEvidence(landedTestEvidence(), other) {
		t.Fatal("landed evidence equality")
	}
}

func readTerminalTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, value); err != nil {
		t.Fatal(err)
	}
}
