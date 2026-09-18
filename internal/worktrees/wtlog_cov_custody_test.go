package worktrees

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

func TestWtLogCovSameExternalHandoffEvidence(t *testing.T) {
	t.Parallel()
	if !sameExternalHandoffEvidence(nil, nil) || sameExternalHandoffEvidence(&workLogExternalHandoffEvidence{Version: 1}, nil) || sameExternalHandoffEvidence(nil, &workLogExternalHandoffEvidence{Version: 1}) {
		t.Fatal("nil handling is wrong")
	}
	first := &workLogExternalHandoffEvidence{Version: 1, HandoffID: "h", RequestDigest: "d"}
	second := &workLogExternalHandoffEvidence{Version: 1, HandoffID: "h", RequestDigest: "d"}
	if !sameExternalHandoffEvidence(first, second) {
		t.Fatal("equal evidence must match")
	}
	second.RequestDigest = "other"
	if sameExternalHandoffEvidence(first, second) {
		t.Fatal("different evidence must not match")
	}
}

func TestWtLogCovValidExternalAttempt(t *testing.T) {
	t.Parallel()
	valid := "000001-" + strings.Repeat("a", 32)
	if !validExternalAttempt(valid, 1) {
		t.Fatal("valid attempt id was refused")
	}
	if !validExternalAttempt("999999-"+strings.Repeat("f", 32), 999999) {
		t.Fatal("attempt at the index limit was refused")
	}
	cases := []struct {
		id    string
		index uint64
	}{
		{"", 0},
		{valid, 0},
		{valid, 1000000},
		{"000002-" + strings.Repeat("a", 32), 1},
		{"000001-" + strings.Repeat("a", 31), 1},
		{"000001-" + strings.Repeat("z", 32), 1},
		{"000001-" + strings.Repeat("F", 32), 1},
		{"000001-" + strings.Repeat("a", 33), 1},
	}
	for _, tc := range cases {
		if validExternalAttempt(tc.id, tc.index) {
			t.Errorf("invalid attempt %q/%d was accepted", tc.id, tc.index)
		}
	}
}

func TestWtLogCovExternalTargetRuntimeModel(t *testing.T) {
	t.Parallel()
	request := sessionmove.Request{SourceRuntime: "codex", SourceModel: "gpt-5", RequestedHarness: "codex"}
	if runtime, model := externalTargetRuntimeModel(request); runtime != "codex" || model != "gpt-5" {
		t.Fatalf("same harness = %q/%q", runtime, model)
	}
	request.RequestedHarness = ""
	if runtime, model := externalTargetRuntimeModel(request); runtime != "codex" || model != "gpt-5" {
		t.Fatalf("empty requested harness = %q/%q", runtime, model)
	}
	request.RequestedHarness = "claude"
	if runtime, model := externalTargetRuntimeModel(request); runtime != "claude" || model != "" {
		t.Fatalf("switched harness = %q/%q", runtime, model)
	}
}

func TestWtLogCovSessionNativeHarnessID(t *testing.T) {
	t.Parallel()
	if got := sessionNativeHarnessID(session.Record{NativeHarnessID: " native ", AgentID: "agent"}); got != "native" {
		t.Fatalf("native harness id = %q", got)
	}
	if got := sessionNativeHarnessID(session.Record{AgentID: " agent "}); got != "agent" {
		t.Fatalf("agent fallback = %q", got)
	}
	if got := sessionNativeHarnessID(session.Record{}); got != "" {
		t.Fatalf("empty record = %q", got)
	}
}

func TestWtLogCovExternalLocalEventID(t *testing.T) {
	t.Parallel()
	digest := sessionmove.DigestBytes([]byte("payload"))
	first := externalLocalEventID("kind", digest, "attempt")
	if first != externalLocalEventID("kind", digest, "attempt") {
		t.Fatal("event id must be deterministic")
	}
	if first == externalLocalEventID("other", digest, "attempt") ||
		first == externalLocalEventID("kind", sessionmove.DigestBytes([]byte("other")), "attempt") ||
		first == externalLocalEventID("kind", digest, "other") {
		t.Fatal("event id must bind kind, digest, and attempt")
	}
	if !validClaimID(first) {
		t.Fatalf("event id %q is not a claim-shaped hash", first)
	}
}

func TestWtLogCovExternalEvidenceBuilders(t *testing.T) {
	fixture := newExternalTargetFixture(t)
	request, digest := fixture.base.request, fixture.digest

	event := externalSourceOfferEvent(request, digest)
	if event.Type != LocalEventHandoff || event.Result != "offered" || event.Git == nil {
		t.Fatalf("offer event = %#v", event)
	}
	if event.ID != externalLocalEventID("source-offered", digest, "") {
		t.Fatalf("offer event id = %q", event.ID)
	}
	if event.Extra["handoff_id"] != request.HandoffID || event.Extra["request_digest"] != string(digest) {
		t.Fatalf("offer extra = %#v", event.Extra)
	}

	ownerExtra := externalSourceOwnerExtra(request, digest)
	if ownerExtra["handoff_id"] != request.HandoffID || ownerExtra["source_work_log_reference"] != request.WorkLogReference {
		t.Fatalf("owner extra = %#v", ownerExtra)
	}
	localExtra := externalLocalEventExtra(request, "worklog:target", "target")
	if localExtra["endpoint"] != "target" || localExtra["target_work_log_reference"] != "worklog:target" ||
		localExtra["predecessor_wb_session_id"] != request.PredecessorWBSessionID {
		t.Fatalf("local extra = %#v", localExtra)
	}
	evidence := externalHandoffEvidence(request, digest, "worklog:target")
	if evidence.Version != externalHandoffEvidenceVersion || evidence.SuccessorTmuxName != "wb-session-"+request.SuccessorWBSessionID ||
		evidence.TargetWorkLogReference != "worklog:target" || evidence.SourceWorkLogReference != request.WorkLogReference {
		t.Fatalf("handoff evidence = %#v", evidence)
	}
}

func TestWtLogCovFindExternalSourceOffer(t *testing.T) {
	fixture := newExternalSourceFixture(t)
	request, digest := fixture.base.request, fixture.digest
	if _, found, err := findExternalSourceOffer(nil, request, digest); err != nil || found {
		t.Fatalf("empty events = %t/%v", found, err)
	}
	want := externalSourceOfferEvent(request, digest)
	want.Version, want.Seq = 1, 0
	event, found, err := findExternalSourceOffer([]LocalWorkLogEvent{want}, request, digest)
	if err != nil || !found || event.ID != want.ID {
		t.Fatalf("matching offer = %#v/%t/%v", event, found, err)
	}
	conflict := want
	conflict.Message = "tampered"
	if _, found, err := findExternalSourceOffer([]LocalWorkLogEvent{conflict}, request, digest); err == nil || found {
		t.Fatalf("conflicting offer = %t/%v", found, err)
	}
}

func TestWtLogCovExpectedExternalClaimID(t *testing.T) {
	t.Parallel()
	parentID := strings.Repeat("a", 64)
	claimID := strings.Repeat("b", 64)
	claim := workLogClaim{EffortID: "effort", RunID: "run", ClaimID: claimID, ParentClaimID: parentID, AgentID: "agent"}
	claim.ExternalHandoff = &workLogExternalHandoffEvidence{
		Version: externalHandoffEvidenceVersion, HandoffID: "h", RequestDigest: "sha256:" + strings.Repeat("a", 64),
		PredecessorWBSessionID: "src", SuccessorWBSessionID: "agent",
		SourceWorkLogReference: "worklog:effort/run/" + parentID,
		TargetWorkLogReference: "worklog:effort/run/" + claimID,
		SuccessorTmuxName:      "wb-session-agent",
	}
	id, err := expectedExternalClaimID(claim)
	if err != nil {
		t.Fatalf("valid external claim rejected: %v", err)
	}
	wantID, wantErr := sessionmove.ExternalHandoffClaimID(sessionmove.Digest("sha256:"+strings.Repeat("a", 64)), "agent")
	if wantErr != nil {
		t.Fatal(wantErr)
	}
	if id != wantID || !validClaimID(id) {
		t.Fatalf("external claim id = %q, want %q", id, wantID)
	}
	cases := map[string]func(*workLogClaim){
		"no evidence":    func(c *workLogClaim) { c.ExternalHandoff = nil },
		"bad version":    func(c *workLogClaim) { c.ExternalHandoff.Version = 2 },
		"no handoff":     func(c *workLogClaim) { c.ExternalHandoff.HandoffID = "" },
		"no predecessor": func(c *workLogClaim) { c.ExternalHandoff.PredecessorWBSessionID = "" },
		"agent mismatch": func(c *workLogClaim) { c.ExternalHandoff.SuccessorWBSessionID = "other" },
		"no source ref":  func(c *workLogClaim) { c.ExternalHandoff.SourceWorkLogReference = "" },
		"no target ref":  func(c *workLogClaim) { c.ExternalHandoff.TargetWorkLogReference = "" },
		"tmux mismatch":  func(c *workLogClaim) { c.ExternalHandoff.SuccessorTmuxName = "wb-session-other" },
		"bad source ref": func(c *workLogClaim) { c.ExternalHandoff.SourceWorkLogReference = "nope" },
		"source lineage": func(c *workLogClaim) { c.ParentClaimID = strings.Repeat("c", 64) },
		"bad target ref": func(c *workLogClaim) { c.ExternalHandoff.TargetWorkLogReference = "nope" },
		"target lineage": func(c *workLogClaim) { c.ClaimID = strings.Repeat("d", 64) },
	}
	for name, mutate := range cases {
		mutated := claim
		evidence := *claim.ExternalHandoff
		mutated.ExternalHandoff = &evidence
		mutate(&mutated)
		if _, err := expectedExternalClaimID(mutated); err == nil {
			t.Errorf("invalid external claim %q was accepted", name)
		}
	}
}

func TestWtLogCovRequestHandoverBytes(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	worktree := t.TempDir()
	handoverPath := "docs/handover.md"
	if err := os.MkdirAll(filepath.Join(worktree, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("# handover\n")
	if err := os.WriteFile(filepath.Join(worktree, filepath.FromSlash(handoverPath)), body, 0o644); err != nil {
		t.Fatal(err)
	}
	request := sessionmove.Request{HandoverPath: handoverPath, HandoverContent: "inline content"}
	contents, err := requestHandoverBytes(worktree, request)
	if err != nil || string(contents) != "inline content" {
		t.Fatalf("inline handover = %q/%v", contents, err)
	}
	request.HandoverContent = ""
	if contents, err := requestHandoverBytes(worktree, request); err != nil || string(contents) != string(body) {
		t.Fatalf("file handover = %q/%v", contents, err)
	}
	request.HandoverPath = "missing.md"
	if _, err := requestHandoverBytes(worktree, request); err == nil {
		t.Fatal("missing handover file was accepted")
	}
	_ = fixture
}

func TestWtLogCovReadBoundedRelativeRegular(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte("bounded\n")
	if err := os.WriteFile(filepath.Join(root, "docs", "note.md"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if contents, err := readBoundedRelativeRegular(root, "docs/note.md", 1<<20); err != nil || string(contents) != string(body) {
		t.Fatalf("read = %q/%v", contents, err)
	}
	if contents, err := readBoundedRelativeRegular(root, "./docs/note.md", 1<<20); err != nil || string(contents) != string(body) {
		t.Fatalf("cleaned read = %q/%v", contents, err)
	}
	for name, relative := range map[string]string{
		"empty":    "",
		"parent":   "../escape",
		"dotdot":   "..",
		"absolute": filepath.Join(root, "docs", "note.md"),
	} {
		if _, err := readBoundedRelativeRegular(root, relative, 1<<20); err == nil {
			t.Errorf("unsafe relative path %q was accepted", name)
		}
	}
	if _, err := readBoundedRelativeRegular(root, "docs/missing.md", 1<<20); err == nil {
		t.Fatal("missing relative file was accepted")
	}
	if _, err := readBoundedRelativeRegular(root, "docs", 1<<20); err == nil {
		t.Fatal("directory entry was accepted")
	}
	if _, err := readBoundedRelativeRegular(root, "docs/note.md", 2); err == nil {
		t.Fatal("oversize file was accepted")
	}
	link := filepath.Join(root, "docs", "link.md")
	if err := os.Symlink(filepath.Join(root, "docs", "note.md"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundedRelativeRegular(root, "docs/link.md", 1<<20); err == nil {
		t.Fatal("symlinked handover was accepted")
	}
	if _, err := readBoundedRelativeRegular(filepath.Join(root, "docs", "missing"), "note.md", 1<<20); err == nil {
		t.Fatal("missing root was accepted")
	}
}

func TestWtLogCovValidateExternalTargetSessionAndReceipt(t *testing.T) {
	fixture := newExternalTargetFixture(t)
	request, digest := fixture.base.request, fixture.digest
	if err := validateExternalTargetSession(request, fixture.session); err != nil {
		t.Fatalf("valid target session rejected: %v", err)
	}
	sessionMutations := map[string]func(*session.Record){
		"pid":         func(r *session.Record) { r.PID = 0 },
		"started at":  func(r *session.Record) { r.StartedAt = time.Time{} },
		"session id":  func(r *session.Record) { r.WBSessionID = "other" },
		"predecessor": func(r *session.Record) { r.PredecessorWBSessionID = "other" },
		"handoff":     func(r *session.Record) { r.HandoffID = "other" },
		"machine":     func(r *session.Record) { r.Machine = "other" },
		"tmux":        func(r *session.Record) { r.TmuxName = "other" },
		"runtime":     func(r *session.Record) { r.Runtime = "other" },
		"model":       func(r *session.Record) { r.Model = "other" },
	}
	for name, mutate := range sessionMutations {
		record := fixture.session
		mutate(&record)
		if err := validateExternalTargetSession(request, record); err == nil {
			t.Errorf("invalid target session %q was accepted", name)
		}
	}

	target, err := sessionmove.ExpectedTargetWorkLogReference(request, digest)
	if err != nil {
		t.Fatal(err)
	}
	receipt := fixture.receipt(t, ExternalSessionWorkLogPrepareResult{WorkLogReference: target.String()})
	if err := validateExternalReceipt(request, digest, receipt); err != nil {
		t.Fatalf("valid receipt rejected: %v", err)
	}
	receiptMutations := map[string]func(*sessionmove.Receipt){
		"handoff":     func(r *sessionmove.Receipt) { r.HandoffID = "other" },
		"session id":  func(r *sessionmove.Receipt) { r.SuccessorWBSessionID = "other" },
		"predecessor": func(r *sessionmove.Receipt) { r.PredecessorWBSessionID = "other" },
		"machine":     func(r *sessionmove.Receipt) { r.TargetMachine = "other" },
		"tmux":        func(r *sessionmove.Receipt) { r.TmuxName = "other" },
		"runtime":     func(r *sessionmove.Receipt) { r.Runtime = "other" },
		"model":       func(r *sessionmove.Receipt) { r.Model = "other" },
		"pinned":      func(r *sessionmove.Receipt) { r.PinnedCommit = "other" },
		"target ref":  func(r *sessionmove.Receipt) { r.TargetWorkLogReference = "other" },
	}
	for name, mutate := range receiptMutations {
		mutated := receipt
		mutate(&mutated)
		if err := validateExternalReceipt(request, digest, mutated); err == nil {
			t.Errorf("invalid receipt %q was accepted", name)
		}
	}
	otherDigest := sessionmove.DigestBytes([]byte("other"))
	if err := validateExternalReceipt(request, otherDigest, receipt); err == nil {
		t.Error("receipt with a different request digest was accepted")
	}
	invalidSchema := receipt
	invalidSchema.SchemaVersion = 0
	if err := validateExternalReceipt(request, digest, invalidSchema); err == nil {
		t.Error("receipt with an invalid schema was accepted")
	}
}

func TestWtLogCovValidateExternalSourceSession(t *testing.T) {
	fixture := newExternalSourceFixture(t)
	if err := validateExternalSourceSession(fixture.source, fixture.base.request); err != nil {
		t.Fatalf("valid source session rejected: %v", err)
	}
	for name, mutate := range map[string]func(*session.Record){
		"pid":        func(r *session.Record) { r.PID = 0 },
		"started at": func(r *session.Record) { r.StartedAt = time.Time{} },
		"session id": func(r *session.Record) { r.WBSessionID = "other" },
		"machine":    func(r *session.Record) { r.Machine = "other" },
		"runtime":    func(r *session.Record) { r.Runtime = "other" },
		"model":      func(r *session.Record) { r.Model = "other" },
		"native id":  func(r *session.Record) { r.NativeHarnessID = "other" },
	} {
		record := fixture.source
		mutate(&record)
		if err := validateExternalSourceSession(record, fixture.base.request); err == nil {
			t.Errorf("invalid source session %q was accepted", name)
		}
	}
}

func TestWtLogCovEnsureExternalManifestAndHandoverPrompt(t *testing.T) {
	fixture := newExternalTargetFixture(t)
	worktree := fixture.worktree
	manifest := Manifest{Version: 1, EffortID: "effort", EffortKind: "feature", Repository: "acme/app",
		Branch: "wb/session", Base: "main", BaseSHA: strings.Repeat("a", 40), Provenance: "created"}
	if err := ensureExternalManifest(worktree, manifest); err != nil {
		t.Fatalf("manifest publication failed: %v", err)
	}
	if err := ensureExternalManifest(worktree, manifest); err != nil {
		t.Fatalf("identical manifest replay failed: %v", err)
	}
	conflict := manifest
	conflict.EffortID = "other"
	if err := ensureExternalManifest(worktree, conflict); err == nil {
		t.Fatal("conflicting manifest was accepted")
	}

	body := []byte("# handover body\n")
	digest := sessionmove.DigestBytes(body)
	at := fixture.base.request.CreatedAt
	record := fixture.session
	if err := ensureExternalHandoverPrompt(worktree, at, record, digest, body); err != nil {
		t.Fatalf("handover prompt publication failed: %v", err)
	}
	if err := ensureExternalHandoverPrompt(worktree, at, record, digest, body); err != nil {
		t.Fatalf("identical handover prompt replay failed: %v", err)
	}
	if err := ensureExternalHandoverPrompt(worktree, at.Add(time.Minute), record, digest, body); err == nil {
		t.Fatal("handover prompt with a different timestamp was accepted")
	}
	otherDigest := sessionmove.DigestBytes([]byte("other body"))
	if err := ensureExternalHandoverPrompt(worktree, at, record, otherDigest, []byte("other body")); err == nil {
		t.Fatal("handover prompt with different bytes was accepted")
	}
}

func TestWtLogCovValidateExternalSourceOffer(t *testing.T) {
	fixture := newExternalSourceFixture(t)
	lock := fixture.lock(t)
	if _, err := EnsureExternalSourceOfferEvidence(ExternalSourceOfferOptions{
		Store: fixture.store, ExecutionLock: lock, ProjectsRoot: fixture.base.projectsRoot,
		Request: fixture.base.request, RequestDigest: fixture.digest, SourceSession: fixture.source,
	}); err != nil {
		t.Fatal(err)
	}
	if err := validateExternalSourceOffer(fixture.worktree, fixture.base.request, fixture.digest); err != nil {
		t.Fatalf("valid source offer rejected: %v", err)
	}
	if err := validateExternalSourceOffer(fixture.worktree, fixture.base.request, sessionmove.DigestBytes([]byte("other"))); err == nil {
		t.Fatal("source offer with a different digest was accepted")
	}
	request := fixture.base.request
	request.HandoverDigest = sessionmove.DigestBytes([]byte("tampered"))
	if err := validateExternalSourceOffer(fixture.worktree, request, fixture.digest); err == nil || !strings.Contains(err.Error(), "does not match admitted immutable bytes") {
		t.Fatalf("tampered handover error = %v", err)
	}
}

func TestWtLogCovRecordExternalTargetAttemptFailedGuard(t *testing.T) {
	fixture := newExternalTargetFixture(t)
	_, err := RecordExternalTargetAttemptFailed(ExternalTargetAttemptFailureOptions{
		ProjectsRoot: fixture.base.projectsRoot, Request: fixture.base.request, RequestDigest: fixture.digest,
		WorktreeDir: fixture.worktree, Failure: sessionlaunch.FailureEvidence{},
	})
	if err == nil || !strings.Contains(err.Error(), "exact failed launcher attempt evidence is incomplete") {
		t.Fatalf("incomplete failure evidence error = %v", err)
	}
}

func TestWtLogCovFindExternalSourceOwner(t *testing.T) {
	fixture := newExternalSourceFixture(t)
	request, digest := fixture.base.request, fixture.digest
	if _, found, err := findExternalSourceOwner(nil, request, digest, fixture.source, fixture.claim, false); err != nil || found {
		t.Fatalf("empty owner events = %t/%v", found, err)
	}
	owner := OwnerRegistration{
		Agent: fixture.source.Runtime + "/" + fixture.source.WBSessionID, Model: fixture.source.Model,
		Effort: fixture.claim.EffortID, PID: fixture.source.PID, WBVersion: "test",
		Command: "session move offer", At: request.CreatedAt.UTC(),
	}
	event := LocalWorkLogEvent{ID: externalLocalEventID("source-owner", digest, ""), Type: LocalEventOwner,
		Message: "predecessor session owns offered external handoff", At: request.CreatedAt.UTC(),
		Owner: &owner, Extra: externalSourceOwnerExtra(request, digest)}
	found, ok, err := findExternalSourceOwner([]LocalWorkLogEvent{event}, request, digest, fixture.source, fixture.claim, false)
	if err != nil || !ok || found.ID != event.ID {
		t.Fatalf("matching owner = %#v/%t/%v", found, ok, err)
	}
	if _, _, err := findExternalSourceOwner([]LocalWorkLogEvent{event, event}, request, digest, fixture.source, fixture.claim, false); err == nil {
		t.Fatal("duplicate source owner was accepted")
	}
	conflict := event
	conflict.Message = "tampered"
	if _, _, err := findExternalSourceOwner([]LocalWorkLogEvent{conflict}, request, digest, fixture.source, fixture.claim, false); err == nil {
		t.Fatal("conflicting source owner was accepted")
	}
	// requireLatest must reject an owner that is no longer the current one.
	later := event
	later.ID = "not-the-owner"
	later.Owner = &owner
	if _, _, err := findExternalSourceOwner([]LocalWorkLogEvent{event, later}, request, digest, fixture.source, fixture.claim, true); err == nil {
		t.Fatal("stale required-latest owner was accepted")
	}
}

func TestWtLogCovSessionReceivePureHelpers(t *testing.T) {
	t.Parallel()
	ref := sessionReceiveFetchRef("handoff-1")
	if !strings.HasPrefix(ref, "refs/wb/session-receive/") || len(strings.TrimPrefix(ref, "refs/wb/session-receive/")) != 64 {
		t.Fatalf("fetch ref = %q", ref)
	}
	if ref != sessionReceiveFetchRef("handoff-1") || ref == sessionReceiveFetchRef("handoff-2") {
		t.Fatal("fetch ref must be a deterministic function of the handoff id")
	}
	if !validInterruptedSessionStageName(".wb-stage-" + strings.Repeat("a", 32)) {
		t.Fatal("valid interrupted session stage name was refused")
	}
	for _, name := range []string{"", "stage-1", "../x", ".wb-stage-" + strings.Repeat("a", 31), ".wb-stage-" + strings.Repeat("z", 32), ".wb-stage-" + strings.Repeat("A", 32)} {
		if validInterruptedSessionStageName(name) {
			t.Errorf("invalid interrupted session stage name %q was accepted", name)
		}
	}
	for value, want := range map[string]string{
		"https://github.com/acme/app.git": "acme/app",
		"git@github.com:acme/app.git":     "acme/app",
	} {
		got, err := sessionReceiveRepositoryFromRemote(value)
		if err != nil || got != want {
			t.Errorf("sessionReceiveRepositoryFromRemote(%q) = %q/%v, want %q", value, got, err, want)
		}
	}
	if _, err := sessionReceiveRepositoryFromRemote("not-a-remote"); err == nil {
		t.Fatal("invalid remote was accepted")
	}
}
