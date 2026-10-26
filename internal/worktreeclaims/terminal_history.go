package worktreeclaims

import (
	"errors"
	"fmt"
	"github.com/sneat-dev/wb/internal/worktreeproof"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

type TerminalWorkLogExpectation struct {
	Task        string
	Repository  string
	Worktree    string
	Branch      string
	Base        string
	FinalCommit string
}

// HistoryPorts binds descriptor reads and special claim-ID derivation to one
// inspection. The service never writes or opens a projection for repair.
type HistoryPorts struct {
	OpenHome          func(string, bool) (*os.File, error)
	OpenChild         func(*os.File, string, bool) (*os.File, error)
	OpenRun           func(string, string, string, bool) (*os.File, string, error)
	OpenOutbox        func(string, string, bool) (*os.File, error)
	ReadJSON          func(*os.File, string, any) error
	ValidSegment      func(string) bool
	ExpectedClaimID   func(Claim) (string, error)
	IdentityFromClaim func(Claim) ExecutionIdentity
}

func (p HistoryPorts) ValidateRemovedTerminalWorkLogs(home string, expectations []TerminalWorkLogExpectation) error {
	if len(expectations) == 0 {
		return errors.New("no terminal Work Log expectations supplied")
	}
	seen := make(map[string]bool, len(expectations))
	for _, expectation := range expectations {
		if err := p.validateRemovedTerminalExpectation(expectation); err != nil {
			return err
		}
		key := strings.Join([]string{expectation.Task, expectation.Repository, filepath.Clean(expectation.Worktree), expectation.Branch, expectation.Base}, "\x00")
		if seen[key] {
			return fmt.Errorf("duplicate terminal Work Log expectation for task %s", expectation.Task)
		}
		seen[key] = true
		if _, err := p.validateRemovedTerminalWorkLog(home, expectation); err != nil {
			return err
		}
	}
	return nil
}

func (p HistoryPorts) ReadRemovedTerminalWorkLogClaimBase(home string, expectation TerminalWorkLogExpectation) (string, error) {
	if err := p.validateRemovedTerminalExpectation(expectation); err != nil {
		return "", err
	}
	return p.validateRemovedTerminalWorkLog(home, expectation)
}

// ValidateRemovedTerminalExpectation runs before resolving WB_HOME, retaining
// the public reader's fail-closed invalid-input precedence.
func (p HistoryPorts) ValidateRemovedTerminalExpectation(expectation TerminalWorkLogExpectation) error {
	return p.validateRemovedTerminalExpectation(expectation)
}

func (p HistoryPorts) ValidateStaticWorkLogClaim(claim Claim, effort, run string) error {
	return p.validateStaticWorkLogClaim(claim, effort, run)
}

func (p HistoryPorts) validateRemovedTerminalExpectation(expectation TerminalWorkLogExpectation) error {
	if !p.ValidSegment(expectation.Task) || strings.TrimSpace(expectation.Repository) == "" ||
		strings.TrimSpace(expectation.Worktree) == "" || strings.TrimSpace(expectation.Branch) == "" ||
		strings.TrimSpace(expectation.FinalCommit) == "" {
		return fmt.Errorf("invalid terminal Work Log expectation for task %q", expectation.Task)
	}
	return nil
}

func (p HistoryPorts) validateRemovedTerminalWorkLog(home string, expectation TerminalWorkLogExpectation) (string, error) {
	homeDir, err := p.OpenHome(home, false)
	if err != nil {
		return "", fmt.Errorf("open terminal Work Log home: %w", err)
	}
	defer func() { _ = homeDir.Close() }()
	worklogs, err := p.OpenChild(homeDir, "worklogs", false)
	if err != nil {
		return "", fmt.Errorf("open terminal Work Logs: %w", err)
	}
	defer func() { _ = worklogs.Close() }()
	// Task is the physical helper identity, not necessarily its Work Log effort.
	// Old merge receipts predate an effort locator: discover it through secure
	// descriptors, then require the unchanged exact immutable claim/terminal.
	efforts, err := worklogs.Readdirnames(-1)
	if err != nil {
		return "", fmt.Errorf("read terminal Work Log efforts: %w", err)
	}
	sort.Strings(efforts)
	matches := 0
	baseSHA := ""
	for _, effortName := range efforts {
		if !p.ValidSegment(effortName) {
			return "", fmt.Errorf("unsafe terminal Work Log effort %q", effortName)
		}
		claimBase, err := p.validateRemovedTerminalWorkLogEffort(home, worklogs, expectation, effortName, &matches)
		if err != nil {
			return "", err
		}
		if claimBase != "" {
			baseSHA = claimBase
		}
	}
	if matches == 0 {
		return "", fmt.Errorf("missing exact removed terminal Work Log for task %s", expectation.Task)
	}
	if matches != 1 {
		return "", fmt.Errorf("ambiguous removed terminal Work Log evidence for task %s", expectation.Task)
	}
	return baseSHA, nil
}

func (p HistoryPorts) validateRemovedTerminalWorkLogEffort(home string, worklogs *os.File, expectation TerminalWorkLogExpectation, effortName string, matches *int) (string, error) {
	effort, err := p.OpenChild(worklogs, effortName, false)
	if err != nil {
		return "", fmt.Errorf("open terminal Work Log effort %s: %w", effortName, err)
	}
	defer func() { _ = effort.Close() }()
	runs, err := p.OpenChild(effort, "runs", false)
	if err != nil {
		return "", fmt.Errorf("open terminal Work Log runs for task %s: %w", expectation.Task, err)
	}
	defer func() { _ = runs.Close() }()
	runNames, err := runs.Readdirnames(-1)
	if err != nil {
		return "", fmt.Errorf("read terminal Work Log runs for task %s: %w", expectation.Task, err)
	}
	sort.Strings(runNames)
	baseSHA := ""
	for _, run := range runNames {
		if !p.ValidSegment(run) {
			return "", fmt.Errorf("unsafe terminal Work Log run %q for task %s", run, expectation.Task)
		}
		if claimBase, err := p.validateRemovedTerminalWorkLogRun(home, expectation, effortName, run, matches); err != nil {
			return "", err
		} else if claimBase != "" {
			baseSHA = claimBase
		}
	}
	return baseSHA, nil
}

func (p HistoryPorts) validateRemovedTerminalWorkLogRun(home string, expectation TerminalWorkLogExpectation, effort, run string, matches *int) (string, error) {
	runDir, _, err := p.OpenRun(home, effort, run, false)
	if err != nil {
		return "", fmt.Errorf("open terminal Work Log run %s for task %s: %w", run, expectation.Task, err)
	}
	defer func() { _ = runDir.Close() }()
	claims, err := p.OpenChild(runDir, "claims", false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Older Work Log runs can predate immutable claims. They cannot
			// match a terminal expectation, so keep searching later runs.
			return "", nil
		}
		return "", fmt.Errorf("open terminal Work Log claims for task %s: %w", expectation.Task, err)
	}
	claimNames, readErr := claims.Readdirnames(-1)
	_ = claims.Close()
	if readErr != nil {
		return "", fmt.Errorf("read terminal Work Log claims for task %s: %w", expectation.Task, readErr)
	}
	sort.Strings(claimNames)
	baseSHA := ""
	claimFiles := make(map[string]bool, len(claimNames))
	for _, name := range claimNames {
		claimFiles[name] = true
	}
	for _, name := range claimNames {
		if strings.HasSuffix(name, PullRequestBindingSuffix) {
			claimID := strings.TrimSuffix(name, PullRequestBindingSuffix)
			if !ValidClaimID(claimID) || !claimFiles[claimID+".json"] {
				return "", fmt.Errorf("unsafe terminal Work Log claim entry %q for task %s", name, expectation.Task)
			}
			claims, err = p.OpenChild(runDir, "claims", false)
			if err != nil {
				return "", fmt.Errorf("reopen terminal Work Log claims for task %s: %w", expectation.Task, err)
			}
			readErr = readValidatedClaimPullRequestBinding(claims, claimID, p.ReadJSON)
			_ = claims.Close()
			if readErr != nil {
				return "", fmt.Errorf("read terminal Work Log pull request binding %q for task %s: %w", name, expectation.Task, readErr)
			}
			continue
		}
		claimID := strings.TrimSuffix(name, ".json")
		if name != claimID+".json" || !ValidClaimID(claimID) {
			return "", fmt.Errorf("unsafe terminal Work Log claim entry %q for task %s", name, expectation.Task)
		}
		claims, err = p.OpenChild(runDir, "claims", false)
		if err != nil {
			return "", fmt.Errorf("reopen terminal Work Log claims for task %s: %w", expectation.Task, err)
		}
		var claim Claim
		readErr = p.ReadJSON(claims, name, &claim)
		_ = claims.Close()
		if readErr != nil {
			return "", fmt.Errorf("read immutable terminal Work Log claim %s: %w", claimID, readErr)
		}
		if !p.matchesRemovedTerminalExpectation(claim, expectation) {
			continue
		}
		if err := p.validateStaticWorkLogClaim(claim, effort, run); err != nil {
			return "", fmt.Errorf("validate immutable terminal Work Log claim %s: %w", claimID, err)
		}
		*matches++
		if *matches > 1 {
			continue
		}
		terminals, err := p.OpenChild(runDir, "terminals", false)
		if err != nil {
			return "", fmt.Errorf("open terminal Work Log terminals for task %s: %w", expectation.Task, err)
		}
		var terminal TerminalRecord
		readErr = p.ReadJSON(terminals, claimID+".json", &terminal)
		_ = terminals.Close()
		if readErr != nil {
			return "", fmt.Errorf("read removed terminal Work Log for task %s: %w", expectation.Task, readErr)
		}
		expectedClaim := claim
		expectedClaim.Lifecycle = "terminal"
		if !reflect.DeepEqual(terminal.Claim, expectedClaim) || terminal.FinalCommit != expectation.FinalCommit ||
			!cleanupSealedDisposition(terminal) || terminal.SealedAt.IsZero() || terminal.SuccessorClaimID != "" ||
			terminal.SuccessorAgentID != "" || terminal.ExternalHandoff != nil || terminal.Orphaned != nil ||
			terminal.DirtyCapture != nil || terminal.Supersession != nil {
			return "", fmt.Errorf("removed terminal Work Log does not exactly corroborate task %s", expectation.Task)
		}
		if err := p.validateRemovedTerminalOutbox(home, claim, terminal); err != nil {
			return "", fmt.Errorf("validate removed terminal Work Log outbox for task %s: %w", expectation.Task, err)
		}
		baseSHA = claim.BaseSHA
	}
	return baseSHA, nil
}

func (p HistoryPorts) matchesRemovedTerminalExpectation(claim Claim, expectation TerminalWorkLogExpectation) bool {
	return claim.Version >= 1 && claim.Task == expectation.Task &&
		claim.Repository == expectation.Repository && filepath.Clean(claim.Worktree) == filepath.Clean(expectation.Worktree) &&
		claim.Branch == expectation.Branch && (expectation.Base == "" || claim.Base == expectation.Base) && claim.Lifecycle == "active"
}

// validateStaticWorkLogClaim is the non-live half of corroborateClaim. It is
// intentionally shared by deleted-worktree recovery: a claim+terminal pair is
// not authority unless the immutable claim itself has a deterministic identity
// and valid execution/handoff metadata.
func (p HistoryPorts) validateStaticWorkLogClaim(claim Claim, effort, run string) error {
	if (claim.Version != 1 && claim.Version != 2) || claim.EffortID != effort || claim.RunID != run || claim.Lifecycle != "active" ||
		!p.ValidSegment(claim.EffortID) || !p.ValidSegment(claim.RunID) || !ValidClaimID(claim.ClaimID) || !worktreeproof.IsGitObjectID(claim.BaseSHA) {
		return errors.New("immutable Work Log claim identity metadata is invalid")
	}
	if claim.ParentClaimID != "" {
		if !ValidClaimID(claim.ParentClaimID) || claim.AgentID == "" ||
			(claim.AcquiredVia != "handoff" && claim.AcquiredVia != "not_landed" && claim.AcquiredVia != "recycle_failed" &&
				claim.AcquiredVia != "external_handoff" && claim.AcquiredVia != "parked_session_resume") {
			return errors.New("immutable successor Work Log claim metadata is invalid")
		}
	}
	if err := ValidateClaimModeLease(claim.Mode, leaseInstant(claim.LeaseExpiresAt), claim.RecordedAt); err != nil {
		return err
	}
	wantID, err := p.ExpectedClaimID(claim)
	if err != nil {
		return err
	}
	if claim.AcquiredVia != "external_handoff" && claim.AcquiredVia != "parked_session_resume" && claim.ExternalHandoff != nil {
		return errors.New("ordinary immutable Work Log claim carries external handoff evidence")
	}
	if wantID != claim.ClaimID {
		return errors.New("immutable Work Log claim digest mismatch")
	}
	if claim.Version == 2 {
		identity := p.IdentityFromClaim(claim)
		if !ValidExecutionIdentifier(identity.Model, true) ||
			(identity.CLI != "" && !ValidExecutionIdentifier(identity.CLI, false)) ||
			(identity.Provider != "" && !ValidExecutionIdentifier(identity.Provider, false)) ||
			(identity.ModelProvenance != ModelProvenanceCallerDeclared && identity.ModelProvenance != ModelProvenanceRuntimeObserved && identity.ModelProvenance != ModelProvenanceUnknown) {
			return errors.New("immutable Work Log claim execution identity metadata is invalid")
		}
	}
	if _, err := NormalizeTaskSummary(claim.TaskSummary); err != nil {
		return fmt.Errorf("immutable Work Log claim task summary is invalid: %w", err)
	}
	return nil
}

func (p HistoryPorts) validateRemovedTerminalOutbox(home string, claim Claim, terminal TerminalRecord) error {
	outbox, err := p.OpenOutbox(home, claim.EffortID, false)
	if err != nil {
		return fmt.Errorf("open immutable terminal outbox: %w", err)
	}
	defer func() { _ = outbox.Close() }()
	var event PublicEvent
	if err := p.ReadJSON(outbox, claim.RunID+"-"+claim.ClaimID+"-sealed.json", &event); err != nil {
		return fmt.Errorf("read immutable terminal outbox: %w", err)
	}
	expected := PublicEvent{Version: 1, Type: "worktree.sealed", At: terminal.SealedAt,
		EffortID: claim.EffortID, RunID: claim.RunID, ClaimID: claim.ClaimID, Repository: claim.Repository,
		Branch: claim.Branch, Base: claim.Base, BaseSHA: claim.BaseSHA, FinalCommit: terminal.FinalCommit,
		Lifecycle: "terminal", Disposition: terminal.Disposition, Landed: terminal.Landed,
		FinalizeReport: terminal.FinalizeReport}
	if !reflect.DeepEqual(event, expected) {
		return errors.New("immutable terminal outbox does not corroborate cleanup authority")
	}
	return nil
}

// cleanupSealedDisposition reports whether cleanup itself sealed terminal:
// `removed`, or `landed` with the landing proof only cleanup records. A
// `landed` terminal without that proof was sealed by finalize, before the
// worktree was removed, so it does not prove the removal.
func cleanupSealedDisposition(terminal TerminalRecord) bool {
	if terminal.Disposition == "landed" {
		return ValidLandedEvidence(terminal.Landed)
	}
	return terminal.Disposition == "removed" && terminal.Landed == nil
}
