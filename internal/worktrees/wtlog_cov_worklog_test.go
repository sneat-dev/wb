package worktrees

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWtLogCovSameEvidencePointers(t *testing.T) {
	t.Parallel()
	if !sameFinalizeReport(nil, nil) || sameFinalizeReport(&workLogFinalizeReport{Result: "x"}, nil) || sameFinalizeReport(nil, &workLogFinalizeReport{Result: "x"}) {
		t.Fatal("sameFinalizeReport nil handling is wrong")
	}
	if !sameFinalizeReport(&workLogFinalizeReport{Result: "x"}, &workLogFinalizeReport{Result: "x"}) {
		t.Fatal("equal finalize reports must match")
	}
	if sameFinalizeReport(&workLogFinalizeReport{Result: "x"}, &workLogFinalizeReport{Result: "y"}) {
		t.Fatal("different finalize reports must not match")
	}

	if !sameOrphanedEvidence(nil, nil) || sameOrphanedEvidence(&workLogOrphanedEvidence{Version: 1}, nil) || sameOrphanedEvidence(nil, &workLogOrphanedEvidence{Version: 1}) {
		t.Fatal("sameOrphanedEvidence nil handling is wrong")
	}
	if !sameOrphanedEvidence(&workLogOrphanedEvidence{Version: 1, Actor: "a"}, &workLogOrphanedEvidence{Version: 1, Actor: "a"}) {
		t.Fatal("equal orphaned evidence must match")
	}
	if sameOrphanedEvidence(&workLogOrphanedEvidence{Version: 1}, &workLogOrphanedEvidence{Version: 2}) {
		t.Fatal("different orphaned evidence must not match")
	}

	if !sameDirtyWorktreeEvidence(nil, nil) || sameDirtyWorktreeEvidence(&DirtyWorktreeEvidence{SHA256: "a"}, nil) {
		t.Fatal("sameDirtyWorktreeEvidence nil handling is wrong")
	}
	if !sameDirtyWorktreeEvidence(&DirtyWorktreeEvidence{SHA256: "a", Files: 2}, &DirtyWorktreeEvidence{SHA256: "a", Files: 2}) {
		t.Fatal("equal dirty evidence must match")
	}
	if sameDirtyWorktreeEvidence(&DirtyWorktreeEvidence{SHA256: "a"}, &DirtyWorktreeEvidence{SHA256: "b"}) {
		t.Fatal("different dirty evidence must not match")
	}
}

func TestWtLogCovValidateProjection(t *testing.T) {
	t.Parallel()
	claimID := strings.Repeat("a", 64)
	valid := workLogProjection{Version: 1, EffortID: "effort", RunID: "run", ClaimID: claimID, Lifecycle: "active"}
	if err := validateProjection(valid); err != nil {
		t.Fatalf("valid projection rejected: %v", err)
	}
	terminal := valid
	terminal.Lifecycle = "terminal"
	if err := validateProjection(terminal); err != nil {
		t.Fatalf("terminal projection rejected: %v", err)
	}
	cases := map[string]workLogProjection{
		"version":   {Version: 2, EffortID: "effort", RunID: "run", ClaimID: claimID, Lifecycle: "active"},
		"effort":    {Version: 1, EffortID: "../escape", RunID: "run", ClaimID: claimID, Lifecycle: "active"},
		"run":       {Version: 1, EffortID: "effort", RunID: "", ClaimID: claimID, Lifecycle: "active"},
		"claim id":  {Version: 1, EffortID: "effort", RunID: "run", ClaimID: "short", Lifecycle: "active"},
		"lifecycle": {Version: 1, EffortID: "effort", RunID: "run", ClaimID: claimID, Lifecycle: "recycled"},
	}
	for name, projection := range cases {
		if err := validateProjection(projection); err == nil {
			t.Errorf("invalid projection %q was accepted", name)
		}
	}
}

func TestWtLogCovNormalizeTaskSummary(t *testing.T) {
	t.Parallel()
	if got, err := NormalizeTaskSummary("  "); err != nil || got != "" {
		t.Fatalf("blank summary = %q/%v", got, err)
	}
	if got, err := NormalizeTaskSummary("  ship the fix  "); err != nil || got != "ship the fix" {
		t.Fatalf("trimmed summary = %q/%v", got, err)
	}
	tooLong := strings.Repeat("x", MaxTaskSummaryRunes+1)
	atLimit := strings.Repeat("x", MaxTaskSummaryRunes)
	if _, err := NormalizeTaskSummary(atLimit); err != nil {
		t.Fatalf("summary at the limit was rejected: %v", err)
	}
	for name, value := range map[string]string{
		"newline":         "one\ntwo",
		"carriage return": "one\rtwo",
		"control char":    "one\x00two",
		"too long":        tooLong,
		"bearer":          "Authorization: Bearer abcdefghijklmnop",
		"assignment":      "password = hunter2",
		"token marker":    "leaked sk-abcdefghij",
	} {
		if _, err := NormalizeTaskSummary(value); err == nil {
			t.Errorf("summary %q was accepted", name)
		}
	}
	if _, err := NormalizeTaskSummary(string([]byte{0xff, 0xfe})); err == nil {
		t.Fatal("invalid UTF-8 summary was accepted")
	}
}

func TestWtLogCovValidExecutionIdentifier(t *testing.T) {
	t.Parallel()
	if !ValidExecutionIdentifier("unknown", true) {
		t.Fatal("unknown must be allowed when allowUnknown is set")
	}
	if ValidExecutionIdentifier("unknown", false) {
		t.Fatal("unknown must be refused when allowUnknown is clear")
	}
	if !ValidExecutionIdentifier("claude-sonnet-4", true) {
		t.Fatal("a plain execution identifier was refused")
	}
	for _, value := range []string{"", "sk-abc123", "sk_abc", "ghp_abc", "bearer abc", "my-token", "app-secret", "user:password", "a@b", "a=b", "a?b", "a#b", "eyjhbGci"} {
		if ValidExecutionIdentifier(value, true) {
			t.Errorf("credential-shaped identifier %q was accepted", value)
		}
	}
}

func TestWtLogCovValidateNewExecutionIdentity(t *testing.T) {
	t.Parallel()
	if err := validateNewExecutionIdentity(ClaimExecutionIdentity{Model: "claude-sonnet"}); err != nil {
		t.Fatalf("plain identity rejected: %v", err)
	}
	if err := validateNewExecutionIdentity(ClaimExecutionIdentity{Model: "unknown", CLI: "claude", Provider: "anthropic"}); err != nil {
		t.Fatalf("full identity rejected: %v", err)
	}
	for name, identity := range map[string]ClaimExecutionIdentity{
		"missing model": {Model: "  "},
		"bad model":     {Model: "sk-abc"},
		"bad cli":       {Model: "claude-sonnet", CLI: "ghp_token"},
		"bad provider":  {Model: "claude-sonnet", Provider: "a@b"},
	} {
		if err := validateNewExecutionIdentity(identity); err == nil {
			t.Errorf("invalid identity %q was accepted", name)
		}
	}
}

func TestWtLogCovValidateCorrectionIdentity(t *testing.T) {
	t.Parallel()
	model := "claude-sonnet"
	empty := ""
	bad := "sk-secret"
	cli := "claude"
	base := CorrectExecutionIdentityOptions{
		EffortID: "effort", RunID: "run", ClaimID: strings.Repeat("b", 64), EventID: "event-1",
		Actor: "operator", Reason: "wrong route", Model: &model,
	}
	if err := validateCorrectionIdentity(base); err != nil {
		t.Fatalf("valid correction rejected: %v", err)
	}
	withCLI := base
	withCLI.Model, withCLI.CLI, withCLI.Provider = nil, &cli, nil
	if err := validateCorrectionIdentity(withCLI); err != nil {
		t.Fatalf("cli-only correction rejected: %v", err)
	}
	clearing := base
	clearing.Model, clearing.CLI = nil, &empty
	if err := validateCorrectionIdentity(clearing); err != nil {
		t.Fatalf("explicit clear correction rejected: %v", err)
	}
	mutations := map[string]func(*CorrectExecutionIdentityOptions){
		"bad effort":     func(o *CorrectExecutionIdentityOptions) { o.EffortID = "../x" },
		"bad run":        func(o *CorrectExecutionIdentityOptions) { o.RunID = "" },
		"bad claim":      func(o *CorrectExecutionIdentityOptions) { o.ClaimID = "nope" },
		"bad event":      func(o *CorrectExecutionIdentityOptions) { o.EventID = ".." },
		"missing actor":  func(o *CorrectExecutionIdentityOptions) { o.Actor = " " },
		"missing reason": func(o *CorrectExecutionIdentityOptions) { o.Reason = "" },
		"no selection":   func(o *CorrectExecutionIdentityOptions) { o.Model = nil },
		"bad model":      func(o *CorrectExecutionIdentityOptions) { o.Model = &bad },
		"bad cli":        func(o *CorrectExecutionIdentityOptions) { o.Model, o.CLI = nil, &bad },
	}
	for name, mutate := range mutations {
		options := base
		mutate(&options)
		if err := validateCorrectionIdentity(options); err == nil {
			t.Errorf("invalid correction %q was accepted", name)
		}
	}
}

func TestWtLogCovIdentityFromClaim(t *testing.T) {
	t.Parallel()
	legacy := identityFromClaim(workLogClaim{})
	if legacy.Model != "unknown" || legacy.ModelProvenance != modelProvenanceUnknown {
		t.Fatalf("legacy identity = %#v", legacy)
	}
	observed := identityFromClaim(workLogClaim{Model: "claude-sonnet"})
	if observed.Model != "claude-sonnet" || observed.ModelProvenance != modelProvenanceRuntimeObserved {
		t.Fatalf("observed identity = %#v", observed)
	}
	unknown := identityFromClaim(workLogClaim{Model: "unknown"})
	if unknown.ModelProvenance != modelProvenanceUnknown {
		t.Fatalf("explicit unknown identity = %#v", unknown)
	}
	declared := identityFromClaim(workLogClaim{Model: "claude-sonnet", ModelProvenance: modelProvenanceCallerDeclared, ModelDeclaredBy: "operator", CLI: "claude", Provider: "anthropic"})
	if declared.ModelProvenance != modelProvenanceCallerDeclared || declared.ModelDeclaredBy != "operator" || declared.CLI != "claude" || declared.Provider != "anthropic" {
		t.Fatalf("declared identity = %#v", declared)
	}
}

func TestWtLogCovClaimIDDerivations(t *testing.T) {
	t.Parallel()
	result := CreateResult{Repository: "acme/app", WorktreeDir: "/tmp/wt", Branch: "wb/x", Base: "main", BaseSHA: "abc"}
	direct := workLogClaimID("effort", result)
	if direct != WorkLogClaimID("effort", result) {
		t.Fatal("WorkLogClaimID must match workLogClaimID")
	}
	if !validClaimID(direct) {
		t.Fatalf("derived claim id %q is not a valid claim id", direct)
	}
	if workLogClaimID("other", result) == direct {
		t.Fatal("claim id must bind the effort")
	}
	if successorWorkLogClaimID("p", "s", "handoff") == successorWorkLogClaimID("p", "s", "not_landed") {
		t.Fatal("successor id must bind the disposition")
	}
	declared := declaredSuccessorWorkLogClaimID("p", "s", "handoff", ClaimExecutionIdentity{Model: "m", CLI: "c", Provider: "pr"})
	if declared == successorWorkLogClaimID("p", "s", "handoff") {
		t.Fatal("declared successor id must bind execution identity")
	}
	if declared == declaredSuccessorWorkLogClaimID("p", "s", "handoff", ClaimExecutionIdentity{Model: "other"}) {
		t.Fatal("declared successor id must change with the model")
	}
	for _, bad := range []string{"", "xyz", strings.Repeat("a", 63), strings.Repeat("z", 64)} {
		if validClaimID(bad) {
			t.Errorf("invalid claim id %q was accepted", bad)
		}
	}
}

func TestWtLogCovDeclaredBy(t *testing.T) {
	t.Parallel()
	if got := declaredBy(WorkLogOptions{Initiator: "operator", AgentID: "agent"}); got != "operator" {
		t.Fatalf("declaredBy initiator = %q", got)
	}
	if got := declaredBy(WorkLogOptions{AgentID: "agent"}); got != "agent" {
		t.Fatalf("declaredBy agent = %q", got)
	}
	if got := declaredBy(WorkLogOptions{}); got != "unknown" {
		t.Fatalf("declaredBy fallback = %q", got)
	}
}

func TestWtLogCovRemovedTerminalExpectations(t *testing.T) {
	t.Parallel()
	valid := TerminalWorkLogExpectation{Task: "task", Repository: "acme/app", Worktree: "/tmp/wt", Branch: "wb/x", Base: "main", FinalCommit: "sha"}
	if err := validateRemovedTerminalExpectation(valid); err != nil {
		t.Fatalf("valid expectation rejected: %v", err)
	}
	for name, mutate := range map[string]func(*TerminalWorkLogExpectation){
		"task":     func(e *TerminalWorkLogExpectation) { e.Task = "../x" },
		"repo":     func(e *TerminalWorkLogExpectation) { e.Repository = " " },
		"worktree": func(e *TerminalWorkLogExpectation) { e.Worktree = "" },
		"branch":   func(e *TerminalWorkLogExpectation) { e.Branch = "" },
		"commit":   func(e *TerminalWorkLogExpectation) { e.FinalCommit = "" },
	} {
		expectation := valid
		mutate(&expectation)
		if err := validateRemovedTerminalExpectation(expectation); err == nil {
			t.Errorf("invalid expectation %q was accepted", name)
		}
	}

	claim := workLogClaim{Version: 1, EffortID: "task", RunID: "run", ClaimID: strings.Repeat("a", 64),
		Task: "task", Repository: "acme/app", Worktree: "/tmp/wt", Branch: "wb/x", Base: "main", Lifecycle: "active"}
	if !matchesRemovedTerminalExpectation(claim, valid) {
		t.Fatal("matching claim was not recognized")
	}
	wrongBranch := valid
	wrongBranch.Branch = "wb/other"
	if matchesRemovedTerminalExpectation(claim, wrongBranch) {
		t.Fatal("claim with a different branch matched")
	}
	terminalClaim := claim
	terminalClaim.Lifecycle = "terminal"
	if matchesRemovedTerminalExpectation(terminalClaim, valid) {
		t.Fatal("terminal lifecycle must not match an active expectation")
	}
}

func TestWtLogCovValidateStaticWorkLogClaim(t *testing.T) {
	t.Parallel()
	base := workLogClaim{Version: 1, EffortID: "effort", RunID: "run", Task: "effort",
		Repository: "acme/app", Worktree: "/tmp/wt", Branch: "wb/x", Base: "main",
		BaseSHA: strings.Repeat("a", 40), Lifecycle: "active"}
	base.ClaimID = workLogClaimID(base.EffortID, CreateResult{Repository: base.Repository, WorktreeDir: base.Worktree, Branch: base.Branch, Base: base.Base, BaseSHA: base.BaseSHA})
	if err := validateStaticWorkLogClaim(base, "effort", "run"); err != nil {
		t.Fatalf("valid v1 claim rejected: %v", err)
	}

	parentID := strings.Repeat("b", 64)
	recycle := base
	recycle.ParentClaimID, recycle.AgentID, recycle.AcquiredVia = parentID, "agent", "recycle_failed"
	recycle.ClaimID = successorWorkLogClaimID(parentID, "agent", "recycle_failed")
	if err := validateStaticWorkLogClaim(recycle, "effort", "run"); err != nil {
		t.Fatalf("valid recycle_failed successor rejected: %v", err)
	}
	handoff := base
	handoff.ParentClaimID, handoff.AgentID, handoff.AcquiredVia = parentID, "agent", "handoff"
	handoff.ClaimID = successorWorkLogClaimID(parentID, "agent", "handoff")
	if err := validateStaticWorkLogClaim(handoff, "effort", "run"); err != nil {
		t.Fatalf("valid v1 handoff successor rejected: %v", err)
	}
	notLanded := base
	notLanded.ParentClaimID, notLanded.AgentID, notLanded.AcquiredVia = parentID, "agent", "not_landed"
	notLanded.ClaimID = successorWorkLogClaimID(parentID, "agent", "not_landed")
	if err := validateStaticWorkLogClaim(notLanded, "effort", "run"); err != nil {
		t.Fatalf("valid not_landed successor rejected: %v", err)
	}
	v2 := base
	v2.Version, v2.Model, v2.ModelProvenance = 2, "claude-sonnet", modelProvenanceCallerDeclared
	if err := validateStaticWorkLogClaim(v2, "effort", "run"); err != nil {
		t.Fatalf("valid v2 claim rejected: %v", err)
	}
	v2Successor := v2
	v2Successor.ParentClaimID, v2Successor.AgentID, v2Successor.AcquiredVia = parentID, "agent", "handoff"
	v2Successor.ClaimID = declaredSuccessorWorkLogClaimID(parentID, "agent", "handoff", ClaimExecutionIdentity{Model: "claude-sonnet"})
	if err := validateStaticWorkLogClaim(v2Successor, "effort", "run"); err != nil {
		t.Fatalf("valid declared v2 successor rejected: %v", err)
	}

	for name, mutate := range map[string]func(*workLogClaim){
		"version":     func(c *workLogClaim) { c.Version = 3 },
		"run":         func(c *workLogClaim) { c.RunID = "other" },
		"lifecycle":   func(c *workLogClaim) { c.Lifecycle = "terminal" },
		"claim id":    func(c *workLogClaim) { c.ClaimID = "short" },
		"base sha":    func(c *workLogClaim) { c.BaseSHA = "zzz" },
		"digest":      func(c *workLogClaim) { c.Branch = "wb/rewritten" },
		"bad summary": func(c *workLogClaim) { c.TaskSummary = "two\nlines" },
	} {
		claim := base
		mutate(&claim)
		if err := validateStaticWorkLogClaim(claim, "effort", "run"); err == nil {
			t.Errorf("invalid claim %q was accepted", name)
		}
	}

	badVia := base
	badVia.ParentClaimID, badVia.AgentID, badVia.AcquiredVia = parentID, "agent", "teleported"
	badVia.ClaimID = successorWorkLogClaimID(parentID, "agent", "teleported")
	if err := validateStaticWorkLogClaim(badVia, "effort", "run"); err == nil {
		t.Fatal("unknown acquired_via was accepted")
	}
	noAgent := base
	noAgent.ParentClaimID, noAgent.AcquiredVia = parentID, "handoff"
	noAgent.ClaimID = successorWorkLogClaimID(parentID, "", "handoff")
	if err := validateStaticWorkLogClaim(noAgent, "effort", "run"); err == nil {
		t.Fatal("parent claim without an agent was accepted")
	}
	external := base
	external.ParentClaimID, external.AgentID, external.AcquiredVia = parentID, "agent", "external_handoff"
	external.ClaimID = successorWorkLogClaimID(parentID, "agent", "external_handoff")
	if err := validateStaticWorkLogClaim(external, "effort", "run"); err == nil {
		t.Fatal("external_handoff without evidence was accepted")
	}
	parked := base
	parked.ParentClaimID, parked.AgentID, parked.AcquiredVia = parentID, "agent", "parked_session_resume"
	parked.ClaimID = successorWorkLogClaimID(parentID, "agent", "parked_session_resume")
	if err := validateStaticWorkLogClaim(parked, "effort", "run"); err == nil {
		t.Fatal("parked_session_resume without evidence was accepted")
	}
	ordinaryWithEvidence := base
	ordinaryWithEvidence.ExternalHandoff = &workLogExternalHandoffEvidence{Version: externalHandoffEvidenceVersion}
	if err := validateStaticWorkLogClaim(ordinaryWithEvidence, "effort", "run"); err == nil {
		t.Fatal("ordinary claim carrying external handoff evidence was accepted")
	}
	badProvenance := v2
	badProvenance.ModelProvenance = "guessed"
	if err := validateStaticWorkLogClaim(badProvenance, "effort", "run"); err == nil {
		t.Fatal("invalid model provenance was accepted")
	}
	badModel := v2
	badModel.Model = "sk-secret"
	if err := validateStaticWorkLogClaim(badModel, "effort", "run"); err == nil {
		t.Fatal("credential-shaped model was accepted")
	}
	badCLI := v2
	badCLI.CLI = "a@b"
	if err := validateStaticWorkLogClaim(badCLI, "effort", "run"); err == nil {
		t.Fatal("invalid CLI was accepted")
	}
	badProvider := v2
	badProvider.Provider = "ghp_token"
	if err := validateStaticWorkLogClaim(badProvider, "effort", "run"); err == nil {
		t.Fatal("credential-shaped provider was accepted")
	}
}

func TestWtLogCovPrivateDirectoryHelpers(t *testing.T) {
	t.Parallel()
	root, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	if _, err := openPrivateChild(root, "../escape", true); err == nil {
		t.Fatal("unsafe private directory segment was accepted")
	}
	if _, err := openPrivateChild(root, "missing", false); !os.IsNotExist(err) {
		t.Fatalf("missing private child error = %v", err)
	}
	created, err := openPrivateChild(root, "worklogs", true)
	if err != nil {
		t.Fatal(err)
	}
	info, err := created.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("private directory mode = %o", info.Mode().Perm())
	}
	reopened, err := openPrivateChild(root, "worklogs", false)
	if err != nil {
		t.Fatal(err)
	}
	_ = reopened.Close()
	_ = created.Close()
}

func TestWtLogCovAtomicReadWriteHelpers(t *testing.T) {
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()

	payload := map[string]string{"hello": "world"}
	if err := writeJSONAtomicAt(directory, "value.json", payload, 0o600); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]string
	if err := readJSONAt(directory, "value.json", &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["hello"] != "world" {
		t.Fatalf("decoded = %#v", decoded)
	}
	if err := readJSONAt(directory, "missing.json", &decoded); !os.IsNotExist(err) {
		t.Fatalf("missing read error = %v", err)
	}

	if err := writeBytesImmutableAt(directory, "immutable.txt", []byte("one"), 0o600, false); err != nil {
		t.Fatal(err)
	}
	if err := writeBytesImmutableAt(directory, "immutable.txt", []byte("one"), 0o600, true); err != nil {
		t.Fatalf("idempotent rewrite rejected: %v", err)
	}
	if err := writeBytesImmutableAt(directory, "immutable.txt", []byte("two"), 0o600, true); err == nil {
		t.Fatal("conflicting immutable rewrite was accepted")
	}
	if err := writeBytesImmutableAt(directory, "../escape.txt", []byte("x"), 0o600, false); err == nil {
		t.Fatal("unsafe immutable filename was accepted")
	}
	if err := writeJSONImmutableAt(directory, "immutable.json", payload, true); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONImmutableAt(directory, "immutable.json", payload, true); err != nil {
		t.Fatalf("idempotent JSON rewrite rejected: %v", err)
	}
	if err := writeJSONImmutableAt(directory, "immutable.json", map[string]string{"hello": "other"}, true); err == nil {
		t.Fatal("conflicting immutable JSON rewrite was accepted")
	}

	nested := filepath.Join(t.TempDir(), "nested")
	// writeBytesAtomic creates the parent directory itself.
	if err := writeJSONAtomic(filepath.Join(nested, "direct.json"), payload, 0o600); err != nil {
		t.Fatalf("writeJSONAtomic into a new directory: %v", err)
	}
	if err := writeJSONAtomic(filepath.Join(nested, "value.json"), payload, 0o600); err != nil {
		t.Fatalf("writeJSONAtomic: %v", err)
	}
	if err := writeBytesAtomic(nested, "bytes.bin", []byte("payload"), 0o600); err != nil {
		t.Fatalf("writeBytesAtomic: %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(nested, "bytes.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "payload" {
		t.Fatalf("bytes file = %q", contents)
	}
	if err := writeBytesAtomicAt(nil, "x", nil, 0o600); err == nil {
		t.Fatal("nil directory atomic write was accepted")
	}
	if err := writeBytesAtomicAt(directory, "../escape", []byte("x"), 0o600); err == nil {
		t.Fatal("unsafe atomic filename was accepted")
	}
}

func TestWtLogCovOpenWorkLogRunAndOutbox(t *testing.T) {
	home := t.TempDir()
	if _, _, err := openWorkLogRun(home, "../bad", "run", true); err == nil {
		t.Fatal("unsafe effort was accepted")
	}
	if _, _, err := openWorkLogRun(home, "effort", "..", true); err == nil {
		t.Fatal("unsafe run was accepted")
	}
	runDir, runPath, err := openWorkLogRun(home, "effort", "run", true)
	if err != nil {
		t.Fatal(err)
	}
	_ = runDir.Close()
	if runPath != filepath.Join(home, "worklogs", "effort", "runs", "run") {
		t.Fatalf("run path = %q", runPath)
	}
	if _, _, err := openWorkLogRun(home, "effort", "run", false); err != nil {
		t.Fatalf("reopening a created run failed: %v", err)
	}
	if _, _, err := openWorkLogRun(home, "effort", "missing", false); err == nil {
		t.Fatal("missing run was opened")
	}
	if _, err := openWorkLogOutbox(home, "../bad", true); err == nil {
		t.Fatal("unsafe outbox effort was accepted")
	}
	outbox, err := openWorkLogOutbox(home, "effort", true)
	if err != nil {
		t.Fatal(err)
	}
	_ = outbox.Close()
	if _, err := openWorkLogOutbox(home, "effort", false); err != nil {
		t.Fatalf("reopening a created outbox failed: %v", err)
	}
}

func TestWtLogCovLockClaimSerializes(t *testing.T) {
	home := t.TempDir()
	runDir, _, err := openWorkLogRun(home, "effort", "run", true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runDir.Close() }()
	unlock, err := lockClaim(runDir, strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	unlockAgain, err := lockClaim(runDir, strings.Repeat("c", 64))
	if err != nil {
		t.Fatalf("relocking a released claim lock failed: %v", err)
	}
	unlockAgain()
}

func TestWtLogCovRemoveWorkLogProjection(t *testing.T) {
	worktree := t.TempDir()
	if err := removeWorkLogProjection(worktree); err != nil {
		t.Fatalf("removing an absent projection: %v", err)
	}
	directory := filepath.Join(worktree, workLogProjectionDirectory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, workLogProjectionName), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeWorkLogProjection(worktree); err != nil {
		t.Fatalf("removing a present projection: %v", err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("projection directory still present: %v", err)
	}
	if err := removeWorkLogProjection(worktree); err != nil {
		t.Fatalf("removing an already removed projection: %v", err)
	}
}

func TestWtLogCovReadWorkLogProjectionRoundTrip(t *testing.T) {
	worktree := t.TempDir()
	if _, err := readWorkLogProjection(worktree); !os.IsNotExist(err) {
		t.Fatalf("missing projection error = %v", err)
	}
	if _, err := readLegacyWorkLogProjection(worktree); !os.IsNotExist(err) {
		t.Fatalf("missing legacy projection error = %v", err)
	}
	projection := workLogProjection{Version: 1, EffortID: "effort", RunID: "run", ClaimID: strings.Repeat("d", 64), Lifecycle: "active"}
	directory := filepath.Join(worktree, workLogProjectionDirectory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.MarshalIndent(projection, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(filepath.Join(directory, workLogProjectionName), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	decoded, err := readWorkLogProjection(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != projection {
		t.Fatalf("decoded projection = %#v", decoded)
	}
	if err := os.WriteFile(filepath.Join(directory, workLogProjectionName), []byte("{ not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readWorkLogProjection(worktree); err == nil {
		t.Fatal("malformed projection was accepted")
	}
	if err := os.WriteFile(filepath.Join(directory, workLogProjectionName), []byte(`{"version":1,"effort_id":"effort","run_id":"run","claim_id":"short","lifecycle":"active"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readWorkLogProjection(worktree); err == nil {
		t.Fatal("invalid projection identity was accepted")
	}

	legacy := workLogProjection{Version: 1, EffortID: "effort", RunID: "run", ClaimID: strings.Repeat("e", 64), Lifecycle: "active"}
	legacyBytes, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	legacyBytes = append(legacyBytes, '\n')
	if err := os.WriteFile(filepath.Join(worktree, legacyWorkLogProjectionName), legacyBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	readLegacy, err := readLegacyWorkLogProjection(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if readLegacy != legacy {
		t.Fatalf("legacy projection = %#v", readLegacy)
	}
}

func TestWtLogCovRelocationRecordValidation(t *testing.T) {
	t.Parallel()
	source := filepath.Join(t.TempDir(), "source")
	destination := filepath.Join(t.TempDir(), "destination")
	claim := workLogClaim{ClaimID: strings.Repeat("f", 64), Task: "task", Repository: "acme/app", Branch: "wb/x"}

	local := workLogRelocationIntent{Version: 1, Type: workLogRelocationIntentType, OperationID: "op-1",
		ClaimID: claim.ClaimID, Task: claim.Task, Repository: claim.Repository, Branch: claim.Branch,
		HeadSHA: "head-1", Source: source, Destination: destination, To: "local", At: time.Now().UTC()}
	if err := validateRelocationRecord(local, claim, true); err != nil {
		t.Fatalf("valid local intent rejected: %v", err)
	}
	receipt := local
	receipt.Type = workLogRelocationType
	if err := validateRelocationRecord(receipt, claim, false); err != nil {
		t.Fatalf("valid local receipt rejected: %v", err)
	}
	if err := validateRelocationRecord(receipt, claim, true); err == nil {
		t.Fatal("receipt type accepted as an intent")
	}

	repository := local
	repository.To = "repository"
	repository.SourceRepository, repository.DestinationRepository = "acme/app", "acme/dest"
	repository.RemoteURL = "https://github.com/acme/dest.git"
	if err := validateRelocationRecord(repository, claim, true); err != nil {
		t.Fatalf("valid repository intent rejected: %v", err)
	}
	legacy := repository
	legacy.To = workLogRelocationLegacyCheckout
	if err := validateRelocationRecord(legacy, claim, true); err != nil {
		t.Fatalf("valid legacy checkout intent rejected: %v", err)
	}
	shared := local
	shared.To = "shared"
	if err := validateRelocationRecord(shared, claim, true); err != nil {
		t.Fatalf("valid shared intent rejected: %v", err)
	}

	for name, mutate := range map[string]func(*workLogRelocationIntent){
		"version":           func(r *workLogRelocationIntent) { r.Version = 2 },
		"operation id":      func(r *workLogRelocationIntent) { r.OperationID = "../escape" },
		"claim":             func(r *workLogRelocationIntent) { r.ClaimID = strings.Repeat("0", 64) },
		"task":              func(r *workLogRelocationIntent) { r.Task = "other" },
		"repository":        func(r *workLogRelocationIntent) { r.Repository = "acme/other" },
		"branch":            func(r *workLogRelocationIntent) { r.Branch = "wb/other" },
		"head":              func(r *workLogRelocationIntent) { r.HeadSHA = "" },
		"destination kind":  func(r *workLogRelocationIntent) { r.To = "moon" },
		"relative source":   func(r *workLogRelocationIntent) { r.Source = "relative/path" },
		"relative dest":     func(r *workLogRelocationIntent) { r.Destination = "relative/path" },
		"zero time":         func(r *workLogRelocationIntent) { r.At = time.Time{} },
		"identical local":   func(r *workLogRelocationIntent) { r.Destination = r.Source },
		"unexpected repos":  func(r *workLogRelocationIntent) { r.SourceRepository = "acme/app" },
		"bad source repo":   func(r *workLogRelocationIntent) { r.SourceRepository = "nope" },
		"same repo":         func(r *workLogRelocationIntent) { r.DestinationRepository = r.SourceRepository },
		"bad remote":        func(r *workLogRelocationIntent) { r.RemoteURL = "not a url" },
		"remote repo drift": func(r *workLogRelocationIntent) { r.DestinationRepository = "acme/other" },
	} {
		record := local
		if name == "bad source repo" || name == "same repo" || name == "bad remote" || name == "remote repo drift" {
			record = repository
		}
		mutate(&record)
		if err := validateRelocationRecord(record, claim, true); err == nil {
			t.Errorf("invalid relocation record %q was accepted", name)
		}
	}
}

func TestWtLogCovSameRelocationBinding(t *testing.T) {
	t.Parallel()
	intent := workLogRelocationIntent{OperationID: "op", ClaimID: "claim", Task: "task", Repository: "acme/app",
		Branch: "wb/x", HeadSHA: "head", Source: "/a/b", Destination: "/a/c", To: "local"}
	receipt := intent
	if !sameRelocationBinding(intent, receipt) {
		t.Fatal("identical records must bind")
	}
	for name, mutate := range map[string]func(*workLogRelocationIntent){
		"operation":   func(r *workLogRelocationIntent) { r.OperationID = "other" },
		"claim":       func(r *workLogRelocationIntent) { r.ClaimID = "other" },
		"task":        func(r *workLogRelocationIntent) { r.Task = "other" },
		"repository":  func(r *workLogRelocationIntent) { r.Repository = "acme/other" },
		"branch":      func(r *workLogRelocationIntent) { r.Branch = "wb/other" },
		"head":        func(r *workLogRelocationIntent) { r.HeadSHA = "other" },
		"to":          func(r *workLogRelocationIntent) { r.To = "shared" },
		"source":      func(r *workLogRelocationIntent) { r.Source = "/a/z" },
		"destination": func(r *workLogRelocationIntent) { r.Destination = "/a/z" },
		"src repo":    func(r *workLogRelocationIntent) { r.SourceRepository = "acme/app" },
		"dst repo":    func(r *workLogRelocationIntent) { r.DestinationRepository = "acme/dest" },
		"remote":      func(r *workLogRelocationIntent) { r.RemoteURL = "https://x" },
	} {
		other := intent
		mutate(&other)
		if sameRelocationBinding(intent, other) {
			t.Errorf("binding %q must differ", name)
		}
	}
}

func TestWtLogCovRelocationNameAndOperationID(t *testing.T) {
	t.Parallel()
	if got := relocationIntentName("claim", "op"); got != "claim-op.intent.json" {
		t.Fatalf("intent name = %q", got)
	}
	if got := relocationReceiptName("claim", "op"); got != "claim-op.completed.json" {
		t.Fatalf("receipt name = %q", got)
	}
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	first, err := relocationOperationID("claim", "/a", "/b", "head", at)
	if err != nil {
		t.Fatal(err)
	}
	if !validSafeSegment(first) {
		t.Fatalf("operation id %q is not a safe segment", first)
	}
	second, err := relocationOperationID("claim", "/a", "/b", "head", at)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("operation ids must be unique per call")
	}
	if !canonicalRelocationPath(filepath.Join(t.TempDir(), "x")) {
		t.Fatal("absolute clean path was not canonical")
	}
	if canonicalRelocationPath("relative") || canonicalRelocationPath("/a/../b") {
		t.Fatal("non-canonical path was accepted")
	}
}

func TestWtLogCovOpenRelocationJournalAndPendingIntent(t *testing.T) {
	home := t.TempDir()
	sourceDir := t.TempDir()
	claim := workLogClaim{Version: 1, EffortID: "effort", RunID: "run", ClaimID: strings.Repeat("a", 64),
		Task: "task", Repository: "acme/app", Branch: "wb/x", Worktree: filepath.Join(sourceDir, "source")}
	run, runPath, err := openWorkLogRun(home, claim.EffortID, claim.RunID, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = run.Close() }()

	journal, err := openRelocationJournal(run, runPath, claim)
	if err != nil {
		t.Fatalf("absent relocation journal should be empty: %v", err)
	}
	if len(journal.intents) != 0 || len(journal.receipts) != 0 {
		t.Fatalf("absent journal = %#v", journal)
	}

	source := claim.Worktree
	destination := filepath.Join(t.TempDir(), "destination")
	intent, intentPath, err := appendRelocationIntent(home, claim, source, destination, "local", "head-1", relocationPlacementRecord{}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if intent == nil || intentPath == "" {
		t.Fatalf("intent = %#v path = %q", intent, intentPath)
	}
	// A second identical append is idempotent and returns the same operation.
	again, againPath, err := appendRelocationIntent(home, claim, source, destination, "local", "head-1", relocationPlacementRecord{}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if again == nil || again.OperationID != intent.OperationID || againPath != intentPath {
		t.Fatalf("idempotent append = %#v path = %q", again, againPath)
	}

	journal, err = openRelocationJournal(run, runPath, claim)
	if err != nil {
		t.Fatal(err)
	}
	pending, path, err := matchingPendingIntent(journal, claim, source, destination, "local", claim.Branch, "head-1")
	if err != nil {
		t.Fatal(err)
	}
	if pending == nil || pending.OperationID != intent.OperationID || path != intentPath {
		t.Fatalf("pending intent = %#v path = %q", pending, path)
	}
	missing, _, err := matchingPendingIntent(journal, claim, source, destination, "shared", claim.Branch, "head-1")
	if err != nil || missing != nil {
		t.Fatalf("non-matching pending intent = %#v err = %v", missing, err)
	}

	if _, _, err := appendRelocationReceipt(home, claim, intent, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	journal, err = openRelocationJournal(run, runPath, claim)
	if err != nil {
		t.Fatal(err)
	}
	completed, _, err := matchingPendingIntent(journal, claim, source, destination, "local", claim.Branch, "head-1")
	if err != nil || completed != nil {
		t.Fatalf("completed relocation must not be pending: %#v err = %v", completed, err)
	}
	resolution, err := latestRelocationResolution(home, claim, destination)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.receipt == nil || resolution.worktree != destination {
		t.Fatalf("latest resolution = %#v", resolution)
	}
	latest, _, err := latestRelocationReceipt(home, claim, destination)
	if err != nil || latest == nil {
		t.Fatalf("latest receipt = %#v err = %v", latest, err)
	}
	pendingIntent, _, err := pendingRelocationIntent(home, claim, destination, claim.Branch, "head-1")
	if err != nil || pendingIntent != nil {
		t.Fatalf("completed intent reported pending: %#v err = %v", pendingIntent, err)
	}
}

func TestWtLogCovFindDiscardedLifecycleBacklogProof(t *testing.T) {
	fixture := newGitFixture(t)
	head := strings.Repeat("a", 40)
	if _, err := FindDiscardedLifecycleBacklogProof(context.Background(), fixture.projectsRoot, "acme/app", "main", "discard-task", "", "wb/x", head); err == nil || !strings.Contains(err.Error(), "identity is incomplete") {
		t.Fatalf("incomplete identity error = %v", err)
	}
	if _, err := FindDiscardedLifecycleBacklogProof(context.Background(), fixture.projectsRoot, "not-a-slug", "main", "discard-task", "/tmp/x", "wb/x", head); err == nil || !strings.Contains(err.Error(), "invalid discarded cleanup repository") {
		t.Fatalf("invalid repository error = %v", err)
	}

	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	worktreeDir := filepath.Join(worktreesRoot, "discard-task", "acme", "app")
	result := ListResult{
		Task: "discard-task", Repository: "acme/app", CanonicalDir: fixture.canonical,
		WorktreesRoot: worktreesRoot, WorktreeDir: worktreeDir, Branch: "wb/x", Base: "main", HeadSHA: head,
	}
	record := newLifecycleBacklogRecord(fixture.projectsRoot, result, string(AbortDiscarded))
	if err := persistLifecycleBacklog(fixture.home, &record, lifecycleStageComplete); err != nil {
		t.Fatalf("persist discarded backlog: %v", err)
	}
	proof, err := FindDiscardedLifecycleBacklogProof(context.Background(), fixture.projectsRoot, "acme/app", "main", "discard-task", worktreeDir, "wb/x", head)
	if err != nil {
		t.Fatalf("completed discarded proof rejected: %v", err)
	}
	if proof == nil || proof.Worktree != worktreeDir || proof.HeadSHA != head || proof.Task != "discard-task" || proof.Branch != "wb/x" {
		t.Fatalf("proof = %#v", proof)
	}
	if !strings.HasPrefix(proof.Path, lifecycleBacklogDirectory(fixture.home)) {
		t.Fatalf("proof path = %q", proof.Path)
	}

	// A still-present checkout must be refused.
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := FindDiscardedLifecycleBacklogProof(context.Background(), fixture.projectsRoot, "acme/app", "main", "discard-task", worktreeDir, "wb/x", head); err == nil || !strings.Contains(err.Error(), "still exists") {
		t.Fatalf("existing worktree error = %v", err)
	}
	if err := os.RemoveAll(worktreeDir); err != nil {
		t.Fatal(err)
	}

	// A different identity finds no record.
	if _, err := FindDiscardedLifecycleBacklogProof(context.Background(), fixture.projectsRoot, "acme/app", "main", "other-task", worktreeDir, "wb/x", head); err == nil || !strings.Contains(err.Error(), "expected one discarded cleanup backlog") {
		t.Fatalf("missing record error = %v", err)
	}
}
