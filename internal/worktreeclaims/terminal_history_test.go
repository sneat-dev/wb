package worktreeclaims

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/worktreelayout"
	"github.com/sneat-dev/wb/internal/worktreesecure"
)

func terminalHistoryTestPorts() HistoryPorts {
	return HistoryPorts{
		OpenHome: worktreesecure.OpenAbsoluteDirectoryNoFollow,
		OpenChild: func(parent *os.File, name string, create bool) (*os.File, error) {
			return worktreesecure.OpenPrivateChild(parent, name, create, worktreelayout.ValidSafeSegment)
		},
		OpenRun: func(home, effort, run string, create bool) (*os.File, string, error) {
			return OpenWorkLogRun(home, effort, run, create, worktreelayout.ValidSafeSegment)
		},
		OpenOutbox: func(home, effort string, create bool) (*os.File, error) {
			return OpenWorkLogOutbox(home, effort, create, worktreelayout.ValidSafeSegment)
		},
		ReadJSON:     filewrite.ReadJSONAt,
		ValidSegment: worktreelayout.ValidSafeSegment,
		ExpectedClaimID: func(claim Claim) (string, error) {
			return WorkLogClaimID(claim.EffortID, CreationResult{
				Repository: claim.Repository, WorktreeDir: claim.Worktree,
				Branch: claim.Branch, Base: claim.Base, BaseSHA: claim.BaseSHA,
			}), nil
		},
		IdentityFromClaim: func(claim Claim) ExecutionIdentity {
			return IdentityFromClaim(ClaimIdentity{Model: claim.Model, CLI: claim.CLI, Provider: claim.Provider,
				ModelProvenance: claim.ModelProvenance, ModelDeclaredBy: claim.ModelDeclaredBy})
		},
	}
}

func writeHistoryJSON(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func terminalHistoryFixture(t *testing.T) (string, TerminalWorkLogExpectation, Claim) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	expectation := TerminalWorkLogExpectation{Task: "task", Repository: "acme/app", Worktree: filepath.Join(home, "removed"),
		Branch: "wb/task", FinalCommit: strings.Repeat("c", 40)}
	claim := Claim{Version: 1, EffortID: "task", RunID: "run", Task: "task", Repository: expectation.Repository,
		Worktree: expectation.Worktree, Branch: expectation.Branch, Base: "main", BaseSHA: strings.Repeat("b", 40), Lifecycle: "active"}
	claim.ClaimID = WorkLogClaimID(claim.EffortID, CreationResult{Repository: claim.Repository,
		WorktreeDir: claim.Worktree, Branch: claim.Branch, Base: claim.Base, BaseSHA: claim.BaseSHA})
	return home, expectation, claim
}

func addRemovedHistoryRun(t *testing.T, home string, expectation TerminalWorkLogExpectation, claim Claim) {
	t.Helper()
	addSealedHistoryRun(t, home, expectation, claim, "removed", nil)
}

// addSealedHistoryRun writes a claim, its terminal and the outbox receipt of
// the seal, as cleanup leaves them.
func addSealedHistoryRun(t *testing.T, home string, expectation TerminalWorkLogExpectation, claim Claim, disposition string, landed *LandedEvidence) {
	t.Helper()
	run := filepath.Join(home, "worklogs", claim.EffortID, "runs", claim.RunID)
	writeHistoryJSON(t, filepath.Join(run, "claims", claim.ClaimID+".json"), claim)
	terminalClaim := claim
	terminalClaim.Lifecycle = "terminal"
	sealedAt := time.Unix(123, 0).UTC()
	terminal := TerminalRecord{Claim: terminalClaim, FinalCommit: expectation.FinalCommit, Disposition: disposition, SealedAt: sealedAt, Landed: landed}
	writeHistoryJSON(t, filepath.Join(run, "terminals", claim.ClaimID+".json"), terminal)
	event := PublicEvent{Version: 1, Type: "worktree.sealed", At: sealedAt, EffortID: claim.EffortID,
		RunID: claim.RunID, ClaimID: claim.ClaimID, Repository: claim.Repository, Branch: claim.Branch,
		Base: claim.Base, BaseSHA: claim.BaseSHA, FinalCommit: expectation.FinalCommit,
		Lifecycle: "terminal", Disposition: disposition, Landed: landed}
	writeHistoryJSON(t, filepath.Join(home, "worklogs", claim.EffortID, "outbox", claim.RunID+"-"+claim.ClaimID+"-sealed.json"), event)
}

func TestRemovedTerminalHistoryScansLegacyRunsAndRequiresUniqueProof(t *testing.T) {
	t.Parallel()
	home, expectation, claim := terminalHistoryFixture(t)
	if err := os.MkdirAll(filepath.Join(home, "worklogs", "task", "runs", "older"), 0o700); err != nil {
		t.Fatal(err)
	}
	addRemovedHistoryRun(t, home, expectation, claim)
	ports := terminalHistoryTestPorts()
	base, err := ports.ReadRemovedTerminalWorkLogClaimBase(home, expectation)
	if err != nil || base != claim.BaseSHA {
		t.Fatalf("removed terminal base = %q, %v", base, err)
	}
	if err := ports.ValidateRemovedTerminalWorkLogs(home, []TerminalWorkLogExpectation{expectation}); err != nil {
		t.Fatal(err)
	}
	if err := ports.ValidateRemovedTerminalWorkLogs(home, []TerminalWorkLogExpectation{expectation, expectation}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate expectation: %v", err)
	}
	claim.RunID = "run-two"
	addRemovedHistoryRun(t, home, expectation, claim)
	if _, err := ports.ReadRemovedTerminalWorkLogClaimBase(home, expectation); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("duplicate immutable match: %v", err)
	}
}

func TestRemovedTerminalExpectationMatchesExactIdentityWithOptionalBase(t *testing.T) {
	t.Parallel()
	ports := terminalHistoryTestPorts()
	home, expectation, claim := terminalHistoryFixture(t)
	_ = home
	if err := ports.ValidateRemovedTerminalExpectation(expectation); err != nil {
		t.Fatal(err)
	}
	if !ports.matchesRemovedTerminalExpectation(claim, expectation) {
		t.Fatal("valid removed claim failed to match")
	}
	expectation.Base = "main"
	if !ports.matchesRemovedTerminalExpectation(claim, expectation) {
		t.Fatal("explicit matching base failed")
	}
	for _, field := range []string{"task", "repository", "worktree", "branch", "base", "lifecycle", "version"} {
		field := field
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			other := claim
			switch field {
			case "task":
				other.Task = "other"
			case "repository":
				other.Repository = "acme/other"
			case "worktree":
				other.Worktree = "/different"
			case "branch":
				other.Branch = "wb/other"
			case "base":
				other.Base = "trunk"
			case "lifecycle":
				other.Lifecycle = "terminal"
			case "version":
				other.Version = 0
			}
			if ports.matchesRemovedTerminalExpectation(other, expectation) {
				t.Fatal("mismatched immutable claim matched removed expectation")
			}
		})
	}
	for _, invalid := range []TerminalWorkLogExpectation{
		{Task: "../task", Repository: expectation.Repository, Worktree: expectation.Worktree, Branch: expectation.Branch, FinalCommit: expectation.FinalCommit},
		{Task: expectation.Task, Worktree: expectation.Worktree, Branch: expectation.Branch, FinalCommit: expectation.FinalCommit},
		{Task: expectation.Task, Repository: expectation.Repository, Branch: expectation.Branch, FinalCommit: expectation.FinalCommit},
		{Task: expectation.Task, Repository: expectation.Repository, Worktree: expectation.Worktree, FinalCommit: expectation.FinalCommit},
		{Task: expectation.Task, Repository: expectation.Repository, Worktree: expectation.Worktree, Branch: expectation.Branch},
	} {
		if err := ports.ValidateRemovedTerminalExpectation(invalid); err == nil {
			t.Fatalf("invalid expectation accepted: %#v", invalid)
		}
	}
}

func TestRemovedTerminalHistoryReportsScopedDescriptorFailures(t *testing.T) {
	t.Parallel()
	home, expectation, claim := terminalHistoryFixture(t)
	addRemovedHistoryRun(t, home, expectation, claim)
	failure := errors.New("injected descriptor failure")
	for _, stage := range []string{"home", "worklogs", "task", "runs", "run", "claims", "claim read", "terminals", "terminal read", "outbox", "outbox read"} {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			ports := terminalHistoryTestPorts()
			originalHome, originalChild, originalRun, originalOutbox, originalRead := ports.OpenHome, ports.OpenChild, ports.OpenRun, ports.OpenOutbox, ports.ReadJSON
			ports.OpenHome = func(path string, create bool) (*os.File, error) {
				if stage == "home" {
					return nil, failure
				}
				return originalHome(path, create)
			}
			ports.OpenChild = func(parent *os.File, name string, create bool) (*os.File, error) {
				if stage == name {
					return nil, failure
				}
				return originalChild(parent, name, create)
			}
			ports.OpenRun = func(home, effort, run string, create bool) (*os.File, string, error) {
				if stage == "run" {
					return nil, "", failure
				}
				return originalRun(home, effort, run, create)
			}
			ports.OpenOutbox = func(home, effort string, create bool) (*os.File, error) {
				if stage == "outbox" {
					return nil, failure
				}
				return originalOutbox(home, effort, create)
			}
			ports.ReadJSON = func(directory *os.File, name string, target any) error {
				if stage == "claim read" && name == claim.ClaimID+".json" {
					if _, ok := target.(*Claim); ok {
						return failure
					}
				}
				if stage == "terminal read" && name == claim.ClaimID+".json" {
					if _, ok := target.(*TerminalRecord); ok {
						return failure
					}
				}
				if stage == "outbox read" && strings.HasSuffix(name, "-sealed.json") {
					return failure
				}
				return originalRead(directory, name, target)
			}
			if _, err := ports.ReadRemovedTerminalWorkLogClaimBase(home, expectation); !errors.Is(err, failure) {
				t.Fatalf("%s error = %v", stage, err)
			}
		})
	}
}

func TestRemovedTerminalHistoryRefusesIncompleteScans(t *testing.T) {
	t.Parallel()
	home, expectation, claim := terminalHistoryFixture(t)
	ports := terminalHistoryTestPorts()
	if err := ports.ValidateRemovedTerminalWorkLogs(home, nil); err == nil {
		t.Fatal("empty expectation set accepted")
	}
	if _, err := ports.ReadRemovedTerminalWorkLogClaimBase(home, TerminalWorkLogExpectation{}); err == nil {
		t.Fatal("invalid expectation accepted")
	}
	if err := ports.ValidateRemovedTerminalWorkLogs(home, []TerminalWorkLogExpectation{{}}); err == nil {
		t.Fatal("invalid batch expectation accepted")
	}
	if err := ports.ValidateRemovedTerminalWorkLogs(home, []TerminalWorkLogExpectation{expectation}); err == nil {
		t.Fatal("missing private evidence accepted")
	}
	if err := os.MkdirAll(filepath.Join(home, "worklogs", "task", "runs", "older"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ports.ReadRemovedTerminalWorkLogClaimBase(home, expectation); err == nil || !strings.Contains(err.Error(), "missing exact") {
		t.Fatalf("legacy-only history = %v", err)
	}
	addRemovedHistoryRun(t, home, expectation, claim)
	for _, stage := range []string{"read runs", "read claims", "reopen claims"} {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			broken := terminalHistoryTestPorts()
			original := broken.OpenChild
			claimOpens := 0
			broken.OpenChild = func(parent *os.File, name string, create bool) (*os.File, error) {
				file, err := original(parent, name, create)
				if name == "claims" {
					if err == nil {
						claimOpens++
					}
					if stage == "reopen claims" && claimOpens == 2 {
						_ = file.Close()
						return nil, errors.New(stage)
					}
				}
				if err == nil && ((stage == "read runs" && name == "runs") || (stage == "read claims" && name == "claims")) {
					_ = file.Close()
				}
				return file, err
			}
			_, err := broken.ReadRemovedTerminalWorkLogClaimBase(home, expectation)
			want := map[string]string{"read runs": "read terminal Work Log runs", "read claims": "read terminal Work Log claims", "reopen claims": "reopen terminal Work Log claims"}[stage]
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("%s fault = %v", stage, err)
			}
		})
	}
	other := claim
	other.Repository = "acme/other"
	writeHistoryJSON(t, filepath.Join(home, "worklogs", "task", "runs", "run", "claims", claim.ClaimID+".json"), other)
	if _, err := ports.ReadRemovedTerminalWorkLogClaimBase(home, expectation); err == nil || !strings.Contains(err.Error(), "missing exact") {
		t.Fatalf("nonmatching immutable claim = %v", err)
	}
}

func TestRemovedTerminalHistoryAcceptsALandingOnlyWithCleanupsProof(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		disposition string
		landed      *LandedEvidence
		accepted    bool
	}{
		"landed with its proof":    {"landed", landedTestEvidence(), true},
		"landed by finalize alone": {"landed", nil, false},
		"removed":                  {"removed", nil, true},
		"removed carrying a proof": {"removed", landedTestEvidence(), false},
		"discarded":                {"discarded", nil, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			home, expectation, claim := terminalHistoryFixture(t)
			addSealedHistoryRun(t, home, expectation, claim, tc.disposition, tc.landed)
			base, err := terminalHistoryTestPorts().ReadRemovedTerminalWorkLogClaimBase(home, expectation)
			if tc.accepted && (err != nil || base != claim.BaseSHA) {
				t.Fatalf("a worktree cleanup removed was not proved: %q, %v", base, err)
			}
			if !tc.accepted && (err == nil || !strings.Contains(err.Error(), "does not exactly corroborate")) {
				t.Fatalf("a terminal cleanup did not seal proved a removal: %v", err)
			}
		})
	}
}

func TestRemovedTerminalHistoryRefusesAnOutboxThatOmitsTheLanding(t *testing.T) {
	t.Parallel()
	home, expectation, claim := terminalHistoryFixture(t)
	addSealedHistoryRun(t, home, expectation, claim, "landed", landedTestEvidence())
	outbox := filepath.Join(home, "worklogs", claim.EffortID, "outbox", claim.RunID+"-"+claim.ClaimID+"-sealed.json")
	writeHistoryJSON(t, outbox, PublicEvent{Version: 1, Type: "worktree.sealed", At: time.Unix(123, 0).UTC(), EffortID: claim.EffortID,
		RunID: claim.RunID, ClaimID: claim.ClaimID, Repository: claim.Repository, Branch: claim.Branch,
		Base: claim.Base, BaseSHA: claim.BaseSHA, FinalCommit: expectation.FinalCommit,
		Lifecycle: "terminal", Disposition: "landed"})
	if _, err := terminalHistoryTestPorts().ReadRemovedTerminalWorkLogClaimBase(home, expectation); err == nil || !strings.Contains(err.Error(), "outbox") {
		t.Fatalf("an outbox receipt without the landing corroborated it: %v", err)
	}
}
