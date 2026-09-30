package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wbprovenance "github.com/sneat-dev/wb/internal/provenance"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/worktreeclaims"
)

//nolint:paralleltest // the Git fixture and WB home state are process-wide.
func TestClaimPublicationAdapterResumeBranches(t *testing.T) {
	fixture := newWorkLogCoverageBatchFixture(t, "claim-publication-adapter")
	invalid := fixture.options
	invalid.RunID = "bad/run"
	if _, err := EnsureWorkLogClaim(fixture.home, fixture.task, fixture.result, invalid); err == nil {
		t.Fatal("invalid run accepted")
	}
	malformed := fixture.options
	malformed.TaskSummary = "two\nlines"
	if _, err := EnsureWorkLogClaim(fixture.home, fixture.task, fixture.result, malformed); err == nil {
		t.Fatal("malformed summary accepted")
	}
	different := fixture.options
	different.TaskSummary = "different summary"
	if _, err := EnsureWorkLogClaim(fixture.home, fixture.task, fixture.result, different); err == nil || !strings.Contains(err.Error(), "task summary") {
		t.Fatalf("different summary err=%v", err)
	}
	if _, err := recordWorkLogWithHooks(fixture.home, fixture.task, fixture.result, malformed, workLogPublicationHooks{}); err == nil {
		t.Fatal("malformed publication summary accepted")
	}
	projectionPath := filepath.Join(fixture.worktree, workLogProjectionDirectory, workLogProjectionName)
	if err := os.WriteFile(projectionPath, []byte("bad json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureWorkLogClaim(fixture.home, fixture.task, fixture.result, fixture.options); err == nil {
		t.Fatal("corrupt projection accepted")
	}
}

//nolint:paralleltest // the Git fixture and WB home state are process-wide.
func TestClaimPublicationAdapterPreparationFailures(t *testing.T) {
	fixture := newWorkLogCoverageBatchFixture(t, "claim-publication-preparation")
	options := fixture.options
	options.EffortID = "another-effort"
	options.RunID = "new-run"
	blockedHome := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(blockedHome, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := recordWorkLogWithHooks(blockedHome, fixture.task, fixture.result, options, workLogPublicationHooks{}); err == nil {
		t.Fatal("bad run root accepted")
	}
	// A previously recorded exact prompt cannot be replaced by a different
	// private payload under the same coordinated run identity.
	changed, err := options.WithOriginalPromptFromStdin([]byte("different exact prompt\n"))
	if err != nil {
		t.Fatal(err)
	}
	changed.EffortID = fixture.options.EffortID
	changed.RunID = fixture.options.RunID
	if _, err := recordWorkLogWithHooks(fixture.home, fixture.task, fixture.result, changed, workLogPublicationHooks{}); err == nil {
		t.Fatal("conflicting prompt accepted")
	}
}

func TestAutoRegisterSessionOperationPorts(t *testing.T) {
	fields := wbprovenance.Fields{Harness: "codex_1", HarnessSessionID: "harness-id"}
	ports := sessionRegistrationPorts{pid: func() int { return 10 }, findAncestor: func(int) (int, string) { return 20, "" }, lookup: func(string, int) (session.Record, bool) { return session.Record{}, false }, register: func(_ string, record session.Record) (session.Record, error) {
		record.WBSessionID = "registered"
		return record, nil
	}}
	registered, ok := autoRegisterSessionFromEnvWithPorts(t.TempDir(), fields, ports)
	if !ok || registered.Runtime != "codex" || registered.PID != 20 {
		t.Fatalf("registration=%+v ok=%v", registered, ok)
	}
	ports.lookup = func(string, int) (session.Record, bool) { return session.Record{WBSessionID: "existing"}, true }
	registered, ok = autoRegisterSessionFromEnvWithPorts(t.TempDir(), fields, ports)
	if !ok || registered.WBSessionID != "existing" {
		t.Fatalf("existing=%+v ok=%v", registered, ok)
	}
	ports.lookup = func(string, int) (session.Record, bool) { return session.Record{Lifecycle: "parked"}, false }
	if _, ok = autoRegisterSessionFromEnvWithPorts(t.TempDir(), fields, ports); ok {
		t.Fatal("parked record merged")
	}
	ports.lookup = func(string, int) (session.Record, bool) { return session.Record{}, false }
	ports.findAncestor = func(int) (int, string) { return 20, "claude-code" }
	fields.Harness = "unrecognized"
	registered, ok = autoRegisterSessionFromEnvWithPorts(t.TempDir(), fields, ports)
	if !ok || registered.Runtime != "claude-code" {
		t.Fatalf("ancestor runtime=%+v ok=%v", registered, ok)
	}
	ports.findAncestor = func(int) (int, string) { return 20, "" }
	registered, ok = autoRegisterSessionFromEnvWithPorts(t.TempDir(), fields, ports)
	if !ok || registered.Runtime != session.Unknown {
		t.Fatalf("unknown runtime=%+v ok=%v", registered, ok)
	}
	ports.register = func(string, session.Record) (session.Record, error) { return session.Record{}, errors.New("register") }
	if _, ok = autoRegisterSessionFromEnvWithPorts(t.TempDir(), fields, ports); ok {
		t.Fatal("registration error accepted")
	}
	ports.findAncestor = func(int) (int, string) { return 0, "" }
	if _, ok = autoRegisterSessionFromEnvWithPorts(t.TempDir(), fields, ports); ok {
		t.Fatal("missing ancestor accepted")
	}
	fields.Harness = ""
	fields.HarnessSessionID = ""
	if _, ok = autoRegisterSessionFromEnvWithPorts(t.TempDir(), fields, ports); ok {
		t.Fatal("empty declaration accepted")
	}
}

//nolint:paralleltest // the Git fixture and WB home state are process-wide.
func TestClaimPublicationAdapterFirstPublication(t *testing.T) {
	gitFixture := newGitFixture(t)
	evidence := observeLocalGit(context.Background(), gitFixture.canonical)
	result := CreateResult{Repository: "acme/app", WorktreeDir: gitFixture.canonical, Branch: evidence.Branch, Base: "main", BaseSHA: evidence.Head}
	options, err := (WorkLogOptions{EffortID: "first-publication", RunID: "run", Model: "unknown"}).WithOriginalPromptFromStdin([]byte("first publication\n"))
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := EnsureWorkLogClaim(gitFixture.home, "first-publication", result, options)
	if err != nil || !outcome.ClaimWritten || !outcome.ProjectionWritten || !outcome.OutboxWritten {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
}

//nolint:paralleltest // the Git fixture and WB home state are process-wide.
func TestClaimPublicationPreparationPorts(t *testing.T) {
	fixture := newWorkLogCoverageBatchFixture(t, "claim-publication-faults")
	for _, stage := range []string{"normalize", "summary", "open", "migrate", "archive"} {
		t.Run(stage, func(t *testing.T) {
			ports := defaultPublicationPreparationPorts()
			failure := errors.New(stage)
			switch stage {
			case "normalize":
				ports.normalizeOptions = func(string, WorkLogOptions, time.Time) (string, string, error) { return "", "", failure }
			case "summary":
				ports.normalizeSummary = func(string) (string, error) { return "", failure }
			case "open":
				ports.openRun = func(string, string, string, bool) (*os.File, string, error) { return nil, "", failure }
			case "migrate":
				ports.migrateLegacy = func(*os.File, string, string, string, string) error { return failure }
			case "archive":
				ports.ensurePromptArchive = func(*os.File, WorkLogOptions, time.Time) (string, string, error) { return "", "", failure }
			}
			_, err := recordWorkLogWithPreparation(fixture.home, fixture.task, fixture.result, fixture.options, workLogPublicationHooks{}, ports)
			if !errors.Is(err, failure) {
				t.Fatalf("stage %s err=%v", stage, err)
			}
		})
	}
	ports := defaultPublicationPreparationPorts()
	ports.autoRegister = func(string, wbprovenance.Fields) (session.Record, bool) {
		return session.Record{WBSessionID: "registered-by-port"}, true
	}
	newGit := newGitFixture(t)
	gitEvidence := observeLocalGit(context.Background(), newGit.canonical)
	newResult := CreateResult{Repository: "acme/app", WorktreeDir: newGit.canonical, Branch: gitEvidence.Branch, Base: "main", BaseSHA: gitEvidence.Head}
	newOptions, err := (WorkLogOptions{EffortID: "claim-publication-auto-register", RunID: "new-run", Model: "unknown"}).WithOriginalPromptFromStdin([]byte("register the claim\n"))
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := recordWorkLogWithPreparation(newGit.home, "claim-publication-auto-register", newResult, newOptions, workLogPublicationHooks{}, ports)
	if err != nil || !outcome.ClaimWritten || outcome.claim.WBSessionID != "registered-by-port" {
		t.Fatalf("claim=%+v err=%v", outcome, err)
	}
}

func TestCorrectionFacadeRejectsUnresolvableHome(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(blocked, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	model := "unknown"
	_, err := CorrectExecutionIdentity(CorrectExecutionIdentityOptions{ProjectsRoot: filepath.Join(blocked, "child"), EffortID: "effort", RunID: "run", ClaimID: strings.Repeat("a", 64), EventID: "event", Actor: "actor", Reason: "reason", Model: &model})
	if err == nil {
		t.Fatal("unresolvable home accepted")
	}
}

func TestClaimPublicationOutboxJSONPreservesFacadeContract(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	oldClaim := workLogPublicEvent{Version: 1, Type: "worktree.claimed", At: now, EffortID: "effort", RunID: "run", ClaimID: "claim", Repository: "acme/app", Branch: "branch", Base: "main", BaseSHA: "base", Lifecycle: "active"}
	newClaim := worktreeclaims.ClaimPublicEvent{Version: oldClaim.Version, Type: oldClaim.Type, At: oldClaim.At, EffortID: oldClaim.EffortID, RunID: oldClaim.RunID, ClaimID: oldClaim.ClaimID, Repository: oldClaim.Repository, Branch: oldClaim.Branch, Base: oldClaim.Base, BaseSHA: oldClaim.BaseSHA, Lifecycle: oldClaim.Lifecycle}
	before, err := json.Marshal(oldClaim)
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(newClaim)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("claimed outbox changed: %s vs %s", before, after)
	}
	oldClaim.Type = "worktree.execution_identity_corrected"
	oldClaim.CorrectionID = "correction"
	newCorrection := worktreeclaims.CorrectionOutboxEvent{Version: oldClaim.Version, Type: oldClaim.Type, At: oldClaim.At, EffortID: oldClaim.EffortID, RunID: oldClaim.RunID, ClaimID: oldClaim.ClaimID, Repository: oldClaim.Repository, Branch: oldClaim.Branch, Base: oldClaim.Base, BaseSHA: oldClaim.BaseSHA, Lifecycle: oldClaim.Lifecycle, CorrectionID: oldClaim.CorrectionID}
	before, err = json.Marshal(oldClaim)
	if err != nil {
		t.Fatal(err)
	}
	after, err = json.Marshal(newCorrection)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("correction outbox changed: %s vs %s", before, after)
	}
}
