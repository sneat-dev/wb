package worktrees

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

//nolint:paralleltest // newGitFixture sets process-wide Git and WB environment variables.
func TestCleanupReceiptProofRequiresExactSourceAndLandingHistory(t *testing.T) {
	fixture := newGitFixture(t)
	ctx := context.Background()
	base := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	gitTest(t, fixture.canonical, "checkout", "-b", "source")
	source := writeAndCommit(t, fixture.canonical, "source.txt", "source\n", "source")
	candidate := writeAndCommit(t, fixture.canonical, "candidate.txt", "candidate\n", "candidate")
	gitTest(t, fixture.canonical, "checkout", "main")
	// A squash landing has the candidate's tree but not its ancestry.
	gitTest(t, fixture.canonical, "merge", "--squash", "source")
	gitTest(t, fixture.canonical, "commit", "-m", "land candidate")
	landing := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	other := writeAndCommit(t, fixture.canonical, "later.txt", "later\n", "later")
	worktree := filepath.Join(fixture.projectsRoot, ".worktrees", "task", "github.com", "acme", "app")
	entry := ListResult{Task: "task", Repository: "acme/app", CanonicalDir: fixture.canonical,
		WorktreeDir: worktree, Branch: "source", Base: "main", HeadSHA: source, RemoteTargetSHA: other}
	proof := MergeReceiptCleanupProof{Repository: entry.Repository, Target: entry.Base, SourceTask: entry.Task,
		SourceWorktree: entry.WorktreeDir, SourceBranch: entry.Branch, SourceSHA: source,
		CandidateSHA: candidate, LandingSHA: landing}
	originalEntry := entry
	if rejection := mergeReceiptCleanupProofRejection(ctx, proof, entry); rejection != "" {
		t.Fatalf("valid squash proof rejected: %s", rejection)
	}
	if got := mergeReceiptCleanupTargetOverride(ctx, []MergeReceiptCleanupProof{proof}, entry); got != "main" {
		t.Fatalf("target override = %q", got)
	}
	if err := applyMergeReceiptCleanupProof(ctx, []MergeReceiptCleanupProof{proof}, &entry); err != nil {
		t.Fatal(err)
	}
	if !entry.IntegratedAtOrigin || !entry.AbsorbedAtOrigin || entry.AbsorbedBySHA != landing || entry.mergeReceiptCandidateSHA != candidate {
		t.Fatalf("valid proof did not grant exact absorption: %+v", entry)
	}

	for _, test := range []struct {
		name string
		edit func(*MergeReceiptCleanupProof, *ListResult)
		want string
	}{
		{"wrong source", func(p *MergeReceiptCleanupProof, _ *ListResult) { p.SourceSHA = base }, "source identity"},
		{"missing target", func(p *MergeReceiptCleanupProof, _ *ListResult) { p.Target = "" }, "receipt has no target"},
		{"invalid candidate", func(p *MergeReceiptCleanupProof, _ *ListResult) { p.CandidateSHA = "broken" }, "invalid candidate SHA"},
		{"missing fetched target", func(_ *MergeReceiptCleanupProof, e *ListResult) { e.RemoteTargetSHA = "" }, "fetched target identity"},
		{"source not ancestor", func(p *MergeReceiptCleanupProof, _ *ListResult) { p.CandidateSHA = base }, "not an ancestor"},
		{"candidate object absent", func(p *MergeReceiptCleanupProof, _ *ListResult) { p.CandidateSHA = strings.Repeat("f", 40) }, "verify source"},
		{"landing tree differs", func(p *MergeReceiptCleanupProof, _ *ListResult) { p.LandingSHA = other }, "does not equal"},
		{"landing not on target", func(_ *MergeReceiptCleanupProof, e *ListResult) { e.RemoteTargetSHA = base }, "not contained"},
	} {
		//nolint:paralleltest // Subtests share the fixture's environment and canonical Git checkout.
		t.Run(test.name, func(t *testing.T) {
			p, e := proof, originalEntry
			test.edit(&p, &e)
			if rejection := mergeReceiptCleanupProofRejection(ctx, p, e); !strings.Contains(rejection, test.want) {
				t.Fatalf("rejection = %q, want %q", rejection, test.want)
			}
			if err := applyMergeReceiptCleanupProof(ctx, []MergeReceiptCleanupProof{p}, &e); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(e.AbsorbedByRejection, test.want) {
				t.Fatalf("proof rejection not reported: %+v", e)
			}
			if e.AbsorbedAtOrigin || e.IntegratedAtOrigin {
				t.Fatalf("rejected proof granted cleanup authority: %+v", e)
			}
		})
	}
	if got := mergeReceiptCleanupTargetOverride(ctx, []MergeReceiptCleanupProof{{SourceWorktree: "other"}, proof}, entry); got != "main" {
		t.Fatalf("unrelated proof prevented exact target selection: %q", got)
	}
	bad := proof
	bad.CandidateSHA = "broken"
	if got := mergeReceiptCleanupTargetOverride(ctx, []MergeReceiptCleanupProof{bad}, entry); got != "" {
		t.Fatalf("malformed proof changed target to %q", got)
	}
}

func TestCleanupClassificationScopesOnlyProvenForeignArtifacts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	results := []CleanupResult{
		{ListResult: ListResult{Task: "one", WorktreesRoot: root, Repository: "acme/app"}, Eligible: true},
		{ListResult: ListResult{Task: "one", WorktreesRoot: root, Repository: "acme/lib"}, Eligible: true},
		{ListResult: ListResult{Task: "two", WorktreesRoot: root, Repository: "acme/app"}, Eligible: true},
	}
	artifacts := []LifecycleArtifact{
		{Task: "one", WorktreesRoot: root, Path: "foreign-stage", Repository: "acme/other", Reason: "unsafe"},
		{Task: "one", WorktreesRoot: root, Path: "unknown-stage", Reason: "unclassified"},
		{Task: "two", WorktreesRoot: root, Path: "empty-namespace", Kind: lifecycleArtifactKindTaskNamespace, Reason: "empty"},
	}
	scopeLifecycleArtifacts(artifacts, "acme/app", "")
	if !artifacts[0].NonBlocking || artifacts[1].NonBlocking || artifacts[2].NonBlocking {
		t.Fatalf("artifact scope = %+v", artifacts)
	}
	blockArtifactTasks(results, artifacts)
	if results[0].Eligible || results[1].Eligible || !results[2].Eligible || !strings.Contains(results[0].Reason, "unknown-stage") {
		t.Fatalf("artifact coordination = %+v", results)
	}
	for i := range results {
		results[i].Eligible, results[i].Reason = true, ""
	}
	backlogPath := filepath.Join(root, "backlog")
	diagnostics := []ListDiagnostic{
		{Task: "one", WorktreesRoot: root, Path: backlogPath, Message: "handled by backlog"},
		{Task: "two", WorktreesRoot: root, Path: filepath.Join(root, "bad"), Message: "malformed"},
	}
	blockDiagnosedTasks(results, diagnostics, map[string]bool{backlogPath: true})
	if !results[0].Eligible || !results[1].Eligible || results[2].Eligible || !strings.Contains(results[2].Reason, "malformed") {
		t.Fatalf("diagnostic coordination = %+v", results)
	}
	results[0].Eligible, results[0].Reason = false, "dirty"
	blockUnsafeTasks(results)
	if results[1].Eligible || !strings.Contains(results[1].Reason, "dirty") {
		t.Fatalf("unsafe sibling did not block task: %+v", results)
	}
}

func TestCleanupLiveDescendantBlocksOnlyParentEffort(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, name := range []string{"parent", "parent.child", "other", ".wb-stage-active"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	results := []CleanupResult{
		{ListResult: ListResult{Task: "parent"}, Eligible: true},
		{ListResult: ListResult{Task: "other"}, Eligible: true},
		{ListResult: ListResult{Task: ""}, Eligible: true},
	}
	blockEffortsWithLiveDescendants(results, []string{root, filepath.Join(root, "absent")})
	if results[0].Eligible || !strings.Contains(results[0].Reason, "parent.child") || !results[1].Eligible || !results[2].Eligible {
		t.Fatalf("descendant cleanup eligibility = %+v", results)
	}
}

func TestCleanupErrorClassifiersRejectGenericFailures(t *testing.T) {
	t.Parallel()
	for _, message := range []string{"bad object deadbeef", "unknown revision", "not a valid commit name", "could not get object info"} {
		if !isUnfetchedGitObjectError(errors.New(message)) {
			t.Fatalf("unfetched object not recognized: %q", message)
		}
	}
	for _, message := range []string{"couldn't find remote ref", "could not find remote ref", "remote ref does not exist", "no such ref"} {
		if !isMissingRemoteTargetError(errors.New(message)) {
			t.Fatalf("missing ref not recognized: %q", message)
		}
	}
	for _, err := range []error{nil, errors.New("network timeout"), errors.New("authentication failed")} {
		if isMissingRemoteTargetError(err) || isUnfetchedGitObjectError(err) {
			t.Fatalf("generic failure misclassified: %v", err)
		}
	}
}

func TestCleanupTaskSelectionAndApplyKeepLocalAndSharedTasksDistinct(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	shared := filepath.Join(t.TempDir(), "shared")
	localRoot := filepath.Join(home, "worktrees")
	outcome := CleanupOutcome{
		Results: []CleanupResult{
			{ListResult: ListResult{Task: "same", WorktreesRoot: filepath.Join(home, "ignored"), Local: true}, Eligible: true},
			{ListResult: ListResult{Task: "same", WorktreesRoot: shared}, Eligible: true},
			{ListResult: ListResult{Task: "same", WorktreesRoot: shared}, Eligible: true, Applied: true},
			{ListResult: ListResult{Task: "untracked", WorktreesRoot: shared}, Eligible: true, BacklogID: "recovery"},
		},
		Artifacts: []LifecycleArtifact{
			{Task: "same", WorktreesRoot: shared, Eligible: true, Applied: true},
			{Task: "", WorktreesRoot: shared, Eligible: false},
		},
	}
	selections := cleanupTaskSelections(outcome, home)
	if len(selections) != 2 || selections[0].Task != "same" || selections[1].Task != "same" {
		t.Fatalf("cleanup task selections = %+v", selections)
	}
	if !cleanupTaskCanApply(outcome, cleanupTaskKey(localRoot, "same"), home) ||
		!cleanupTaskCanApply(outcome, cleanupTaskKey(shared, "same"), home) ||
		!cleanupTaskHasEligibleWorktree(outcome, cleanupTaskKey(shared, "same"), home) {
		t.Fatalf("eligible local/shared task refused: %+v", outcome)
	}
	if cleanupTaskCanApply(outcome, cleanupTaskKey(shared, "untracked"), home) ||
		cleanupTaskHasEligibleWorktree(outcome, cleanupTaskKey(shared, "untracked"), home) {
		t.Fatal("backlog-only result authorized normal cleanup")
	}
	outcome.Results[1].Eligible = false
	if cleanupTaskCanApply(outcome, cleanupTaskKey(shared, "same"), home) {
		t.Fatal("one unsafe shared sibling did not block shared task")
	}
	if !cleanupTaskCanApply(outcome, cleanupTaskKey(localRoot, "same"), home) {
		t.Fatal("unsafe shared sibling blocked unrelated local task")
	}
	outcome.Results[1].Eligible = true
	outcome.Artifacts[0].Eligible = false
	if cleanupTaskCanApply(outcome, cleanupTaskKey(shared, "same"), home) {
		t.Fatal("unsafe artifact did not block shared task")
	}
}

func TestLikelyTaskWorktreePathRecognizesSupportedDepthsOnly(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "store")
	predicted := filepath.Join(root, "task", "github.com", "acme", "app")
	for _, path := range []string{
		predicted,
		filepath.Join(root, ".worktrees", "task"),
		filepath.Join(root, "task", "acme", "app"),
		filepath.Join(root, "task", "github.com", "acme", "other"),
	} {
		if !likelyTaskWorktreePath(path, predicted, "task") {
			t.Fatalf("task path not recognized: %s", path)
		}
	}
	for _, path := range []string{filepath.Join(root, "other", "acme", "app"), filepath.Join(root, "other", "github.com", "acme", "app")} {
		if likelyTaskWorktreePath(path, predicted, "task") {
			t.Fatalf("unrelated task path recognized: %s", path)
		}
	}
}

func TestSecureStageNameClassificationKeepsLocalStagesTaskBound(t *testing.T) {
	t.Parallel()
	active := taskBoundLocalStagePrefix("task") + "1234"
	retired := taskBoundLocalRetiredStagePrefix("task") + "1234"
	if !isTaskBoundLocalStageCheckout(filepath.Join("/tmp", active, "checkout"), "task") ||
		isTaskBoundLocalStageCheckout(filepath.Join("/tmp", active, "checkout"), "other") ||
		isTaskBoundLocalStageCheckout(filepath.Join("/tmp", active, "wrong"), "task") {
		t.Fatal("task-bound checkout classification crossed task or path boundary")
	}
	if !isMatchingRetiredStageDirectory(retired, taskBoundLocalRetiredStagePrefix("task")) ||
		isMatchingRetiredStageDirectory(retired, taskBoundLocalRetiredStagePrefix("other")) ||
		isMatchingRetiredStageDirectory(retired, ".wb-retired-stage-") ||
		isMatchingRetiredStageDirectory(".wb-retired-stage-", ".wb-retired-stage-") {
		t.Fatal("retired stage classification crossed task boundary")
	}
}

//nolint:paralleltest // Subtests use one prompt path that the parent later rewrites.
func TestWorkLogPromptSnapshotRejectsMissingChangedAndUnstableSources(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "request.txt")
	if err := os.WriteFile(path, []byte("  exact originating request\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := WorkLogOptions{OriginalPrompt: path, RequireOriginalPrompt: true}
	if err := snapshotOriginalPrompt(&options); err != nil {
		t.Fatal(err)
	}
	if options.OriginalPrompt != path || string(options.originalPromptContents) != "  exact originating request\n" {
		t.Fatalf("prompt snapshot changed exact bytes or source: %+v", options)
	}
	if err := os.WriteFile(path, []byte("changed later"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := snapshotOriginalPrompt(&options); err != nil {
		t.Fatalf("immutable prepared snapshot reread changed file: %v", err)
	}
	options.originalPromptDigest = "wrong"
	if err := snapshotOriginalPrompt(&options); err == nil || !strings.Contains(err.Error(), "internally inconsistent") {
		t.Fatalf("corrupted prepared digest accepted: %v", err)
	}
	for _, test := range []struct {
		name    string
		options WorkLogOptions
		want    string
	}{
		{"missing required", WorkLogOptions{RequireOriginalPrompt: true}, "is required"},
		{"missing file", WorkLogOptions{OriginalPrompt: filepath.Join(root, "absent")}, "open original prompt"},
		{"directory", WorkLogOptions{OriginalPrompt: root}, "regular file"},
	} {
		//nolint:paralleltest // The parent reuses and rewrites the shared prompt path after these cases.
		t.Run(test.name, func(t *testing.T) {
			if err := snapshotOriginalPrompt(&test.options); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("snapshot error = %v, want %q", err, test.want)
			}
		})
	}
	if err := os.WriteFile(path, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	blank := WorkLogOptions{OriginalPrompt: path}
	if err := snapshotOriginalPrompt(&blank); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("whitespace-only prompt accepted: %v", err)
	}
	if _, err := (WorkLogOptions{}).WithOriginalPromptFromStdin([]byte(" \n")); err == nil {
		t.Fatal("whitespace-only stdin accepted")
	}
	stdin, err := (WorkLogOptions{}).WithOriginalPromptFromStdin([]byte("exact stdin\n"))
	if err != nil {
		t.Fatal(err)
	}
	if stdin.OriginalPrompt != originalPromptStdinMarker || string(stdin.originalPromptContents) != "exact stdin\n" {
		t.Fatalf("stdin bytes/source changed: %+v", stdin)
	}
	if err := snapshotOriginalPrompt(&stdin); err != nil {
		t.Fatal(err)
	}
}

func TestWorkLogClaimExtensionUsesExactArchivedPrompt(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	worktree := t.TempDir()
	gitTest(t, worktree, "init")
	request := []byte("Original request with exact newline\n")
	prepared, err := (WorkLogOptions{EffortID: "task", RunID: "run", Model: "unknown", TaskSummary: "Extend repository work"}).WithOriginalPromptFromStdin(request)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := recordWorkLogWithHooks(home, "task", CreateResult{
		Repository: "acme/app", WorktreeDir: worktree, Branch: "task", Base: "main", BaseSHA: "abc123",
	}, prepared, workLogPublicationHooks{})
	if err != nil {
		t.Fatal(err)
	}
	claim := outcome.claim
	extended, err := workLogOptionsForClaimExtension(home, WorkLogOptions{Model: "unknown"}, claim)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(request)
	if extended.EffortID != "task" || extended.RunID != "run" || extended.TaskSummary != claim.TaskSummary ||
		string(extended.originalPromptContents) != string(request) || extended.originalPromptDigest != hex.EncodeToString(digest[:]) {
		t.Fatalf("extension did not reuse immutable run identity and prompt: %+v", extended)
	}
	if _, err := workLogOptionsForClaimExtension(home, WorkLogOptions{Model: "different"}, claim); err == nil || !strings.Contains(err.Error(), "different model") {
		t.Fatalf("different runtime identity accepted: %v", err)
	}
	if _, err := workLogOptionsForClaimExtension(home, WorkLogOptions{Model: "unknown", RequireOriginalPrompt: true}, claim); err == nil || !strings.Contains(err.Error(), "is required") {
		t.Fatalf("required missing prompt accepted: %v", err)
	}
	wrong, err := (WorkLogOptions{Model: "unknown"}).WithOriginalPromptFromStdin([]byte("different request"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workLogOptionsForClaimExtension(home, wrong, claim); err == nil || !strings.Contains(err.Error(), "prompt") {
		t.Fatalf("different immutable prompt accepted: %v", err)
	}
}

//nolint:paralleltest // Subtests share the checkout path that the parent later replaces.
func TestCleanupWorktreeHandlesRejectSymlinksAndPathReplacement(t *testing.T) {
	root := t.TempDir()
	checkout := filepath.Join(root, "checkout")
	if err := os.Mkdir(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	rootPath := string(filepath.Separator)
	for _, opener := range []struct {
		name          string
		open          func(string) (*cleanupWorktreeHandle, error)
		wantRootError string
	}{
		{"adopted", openAdoptedCleanupWorktree, "adopted worktree path " + rootPath + " has no repository segment to open"},
		{"relocated", func(path string) (*cleanupWorktreeHandle, error) {
			return openRelocatedManagedCleanupWorktree(nil, path)
		}, "relocated managed worktree path " + rootPath + " has no checkout segment"},
		{"local", func(path string) (*cleanupWorktreeHandle, error) { return openCanonicalLocalCleanupWorktree(nil, path) }, "canonical local worktree path " + rootPath + " has no task segment"},
	} {
		//nolint:paralleltest // All cases use one checkout path that the parent later renames.
		t.Run(opener.name, func(t *testing.T) {
			if _, err := opener.open(rootPath); err == nil || err.Error() != opener.wantRootError {
				t.Fatalf("root path error = %v, want %q", err, opener.wantRootError)
			}
			handle, err := opener.open(checkout)
			if err != nil {
				t.Fatal(err)
			}
			if !handle.ownParent || handle.closeParent {
				t.Fatalf("external parent ownership changed: %+v", handle)
			}
			if err := handle.validate(); err != nil {
				t.Fatal(err)
			}
			handle.close()
			linked := filepath.Join(root, opener.name+"-link")
			if err := os.Symlink(checkout, linked); err != nil {
				t.Fatal(err)
			}
			if _, err := opener.open(linked); err == nil || !strings.Contains(err.Error(), "without following links") {
				t.Fatalf("symlink checkout accepted: %v", err)
			}
			missing := filepath.Join(root, opener.name+"-missing")
			if _, err := opener.open(missing); err == nil {
				t.Fatal("missing checkout accepted")
			}
		})
	}
	handle, err := openAdoptedCleanupWorktree(checkout)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.close()
	if err := os.Rename(checkout, filepath.Join(root, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := handle.validate(); err == nil || !strings.Contains(err.Error(), "path changed") {
		t.Fatalf("replaced checkout passed descriptor validation: %v", err)
	}
}

func TestRecordedWorktreeBaseUsesValidatedImmutableManifest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	worktree := t.TempDir()
	gitTest(t, worktree, "init")
	if base, err := resolveRecordedWorktreeBase(ctx, "", worktree, ""); err != nil || base != "main" {
		t.Fatalf("legacy default base = %q, %v", base, err)
	}
	if base, err := resolveRecordedWorktreeBase(ctx, "", worktree, "feature/fallback"); err != nil || base != "feature/fallback" {
		t.Fatalf("legacy explicit fallback = %q, %v", base, err)
	}
	if _, err := resolveRecordedWorktreeBase(ctx, "", worktree, "bad base"); err == nil || !strings.Contains(err.Error(), "invalid fallback") {
		t.Fatalf("invalid fallback accepted: %v", err)
	}
	manifest := newCreatedManifest("task")
	manifest.Worktree = worktree
	manifest.Base = "feature/recorded"
	if err := WriteManifest(worktree, manifest); err != nil {
		t.Fatal(err)
	}
	if base, err := resolveRecordedWorktreeBase(ctx, "", worktree, "feature/fallback"); err != nil || base != "feature/recorded" {
		t.Fatalf("manifest base was not authoritative: %q, %v", base, err)
	}
	manifestPath := filepath.Join(worktree, ".wb", "local", "manifest.yaml")
	if err := os.WriteFile(manifestPath, []byte("not: [valid yaml"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveRecordedWorktreeBase(ctx, "", worktree, "main"); err == nil || !strings.Contains(err.Error(), "read worktree target record") {
		t.Fatalf("corrupt immutable manifest accepted: %v", err)
	}
}

//nolint:paralleltest // newGitFixture and this test set process-wide WB configuration variables.
func TestFleetInventoryRecoversClaimedCheckoutAfterSharedRootChanges(t *testing.T) {
	fixture := newGitFixture(t)
	configHome := t.TempDir()
	oldRoot := filepath.Join(t.TempDir(), "old-shared")
	newRoot := filepath.Join(t.TempDir(), "new-shared")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	mustWriteBranchConfig(t, filepath.Join(configHome, "wb", "worktrees.yaml"), "version: 1\nworktrees:\n  root: "+oldRoot+"\n")
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "fleet-root-drift", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil || len(created) != 1 {
		t.Fatalf("create claimed checkout = %+v, %v", created, err)
	}
	mustWriteBranchConfig(t, filepath.Join(configHome, "wb", "worktrees.yaml"), "version: 1\nworktrees:\n  root: "+newRoot+"\n")
	listed, err := ListWithDiagnostics(context.Background(), ListOptions{ProjectsRoot: fixture.projectsRoot, Filter: "acme/app", Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Results) != 1 || listed.Results[0].WorktreeDir != created[0].WorktreeDir || listed.Results[0].Local || listed.Results[0].External {
		t.Fatalf("fleet registry did not recover claimed shared checkout: results=%+v diagnostics=%+v", listed.Results, listed.Diagnostics)
	}
	if err := os.RemoveAll(created[0].WorktreeDir); err != nil {
		t.Fatal(err)
	}
	missing, err := ListWithDiagnostics(context.Background(), ListOptions{ProjectsRoot: fixture.projectsRoot, Filter: "acme/app", Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, diagnostic := range missing.Diagnostics {
		if diagnostic.Task == "fleet-root-drift" && diagnostic.Path == created[0].WorktreeDir && strings.Contains(diagnostic.Message, "still registers") {
			return
		}
	}
	t.Fatalf("fleet registry omitted missing claimed checkout: %+v", missing.Diagnostics)
}

func TestCreateRecoveryPreservesPreexistingCheckoutWhenClaimFails(t *testing.T) {
	t.Parallel()
	result := CreateResult{Repository: "acme/app", WorktreeDir: filepath.Join(t.TempDir(), "checkout"), Branch: "task"}
	if err := os.Mkdir(result.WorktreeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	attempt := createAttempt{plan: &createPlan{result: result}}
	outcomes, err := recoverFailedCreatePublications(context.Background(), t.TempDir(), CreateOptions{Operation: "task"}, []createAttempt{attempt}, errors.New("claim failed"))
	if err == nil || len(outcomes) != 1 || outcomes[0].Result.Action != "recovery_required" || outcomes[0].RollbackCompleted {
		t.Fatalf("preexisting checkout recovery = %+v, %v", outcomes, err)
	}
	if info, statErr := os.Stat(result.WorktreeDir); statErr != nil || !info.IsDir() {
		t.Fatalf("preexisting checkout was removed: %v, %v", info, statErr)
	}
	if got := boundedRecoveryFailure(nil); !strings.Contains(got, "did not complete") {
		t.Fatalf("nil failure = %q", got)
	}
	long := strings.Repeat("x", 2100) + "\x00sensitive"
	if got := boundedRecoveryFailure(errors.New(long)); len(got) != 2000 || strings.ContainsRune(got, '\x00') {
		t.Fatalf("unbounded or null-containing recovery failure: length=%d", len(got))
	}
}

func TestLegacySingletonClaimMigrationRecordsLostRepositoryCardinality(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	runDir, runPath, err := openWorkLogRun(home, "task", "run", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runDir.Close() })
	legacy := legacyWorkLogClaim{Version: 1, EffortID: "task", RunID: "run", Task: "task",
		Repository: "acme/app", Worktree: "/tmp/app", Branch: "task", Base: "main", BaseSHA: "base",
		RecordedAt: time.Now().UTC(), PromptArchive: filepath.Join(runPath, "original-prompt.txt")}
	if err := writeJSONImmutableAt(runDir, "claim.json", legacy, false); err != nil {
		t.Fatal(err)
	}
	for _, repo := range []string{"app", "lib"} {
		path := filepath.Join(home, "worktrees", "task", "acme", repo, legacyWorkLogProjectionName)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"effort_id":"task","run_id":"run"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateLegacySingletonClaim(runDir, runPath, home, "task", "run"); err != nil {
		t.Fatal(err)
	}
	claimID := workLogClaimID("task", CreateResult{Repository: "acme/app", WorktreeDir: "/tmp/app", Branch: "task", Base: "main", BaseSHA: "base"})
	var migrated workLogClaim
	claimBytes, err := os.ReadFile(filepath.Join(runPath, "claims", claimID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(claimBytes, &migrated); err != nil {
		t.Fatal(err)
	}
	if migrated.PromptArchive != "original-prompt.txt" || migrated.Repository != "acme/app" {
		t.Fatalf("migrated claim lost immutable identity: %+v", migrated)
	}
	var receipt legacyClaimMigration
	if err := readJSONAt(runDir, "legacy-claim-migration.json", &receipt); err != nil {
		t.Fatal(err)
	}
	if !receipt.LostCardinality || receipt.RecoveredClaims != 1 || receipt.ObservedProjections != 2 {
		t.Fatalf("lost-cardinality receipt = %+v", receipt)
	}
	if err := migrateLegacySingletonClaim(runDir, runPath, home, "task", "run"); err != nil {
		t.Fatalf("migration was not idempotent: %v", err)
	}
}
