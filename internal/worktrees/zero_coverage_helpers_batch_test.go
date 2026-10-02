package worktrees

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/worktreebranches"
	"github.com/sneat-dev/wb/internal/worktreeproof"
	"github.com/sneat-dev/wb/internal/worktreeretire"
)

func TestZeroCoverageBatchFilesystemAndContextHelpers(t *testing.T) {
	t.Parallel()

	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	contents := []byte("manifest contents\n")
	if err := os.WriteFile(manifestPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := QuarantineManifestDigest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest := fmt.Sprintf("%x", sha256.Sum256(contents))
	if digest != wantDigest {
		t.Fatalf("manifest digest = %q, want %q", digest, wantDigest)
	}
	if _, err := QuarantineManifestDigest(filepath.Join(t.TempDir(), "missing")); !os.IsNotExist(err) {
		t.Fatalf("missing manifest error = %v", err)
	}

	projectsRoot := t.TempDir()
	canonical := filepath.Join(projectsRoot, "github.com", "acme", "app")
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, err := CanonicalRepositoryPath(projectsRoot, "acme/app")
	wantCanonical := filepath.Join(projectsRoot, "acme", "app")
	if err != nil || resolved != wantCanonical {
		t.Fatalf("canonical repository path = %q, %v; want %q", resolved, err, wantCanonical)
	}
	remote, err := ExpectedRemoteURL(projectsRoot, canonical)
	if err != nil || remote != "https://github.com/acme/app" {
		t.Fatalf("expected remote URL = %q, %v", remote, err)
	}
	if _, err := CanonicalRepositoryPath("", "acme/app"); err == nil {
		t.Fatal("empty projects root resolved a canonical repository")
	}
	if _, err := ExpectedRemoteURL("", canonical); err == nil {
		t.Fatal("empty projects root resolved a remote URL")
	}
	if got := mustCountOutbox(t.TempDir()); got != 0 {
		t.Fatalf("missing local outbox count = %d", got)
	}

	parent, cancelParent := context.WithCancel(context.Background())
	cancelParent()
	rollback, cancelRollback := rollbackContext(parent)
	t.Cleanup(cancelRollback)
	if err := rollback.Err(); err != nil {
		t.Fatalf("rollback context inherited cancellation: %v", err)
	}
	if _, ok := rollback.Deadline(); !ok {
		t.Fatal("rollback context has no cleanup deadline")
	}
}

func TestZeroCoverageBatchParkedEvents(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, time.September, 29, 10, 0, 0, 0, time.UTC)
	claim := workLogClaim{
		EffortID: "effort", RunID: "run", ClaimID: "claim", Repository: "acme/app",
		Branch: "feature", Base: "main", BaseSHA: strings.Repeat("a", 40), RecordedAt: at,
	}
	public := preparedTargetPublicEvent(claim)
	if public.Type != "worktree.claimed" || public.ClaimID != claim.ClaimID || public.Lifecycle != "active" || !public.At.Equal(at) {
		t.Fatalf("prepared target public event = %#v", public)
	}
	projection := activeTargetProjection(claim)
	if projection.Version != 1 || projection.EffortID != claim.EffortID || projection.RunID != claim.RunID || projection.ClaimID != claim.ClaimID || projection.Lifecycle != "active" {
		t.Fatalf("active target projection = %#v", projection)
	}

	request := sessionpark.RemoteRequest{
		ResumeID: "resume", ParkedSessionID: "parked", PredecessorWBSessionID: "wbs-old", SuccessorWBSessionID: "wbs-new",
	}
	member := sessionpark.RemoteMember{MemberID: "member", Repository: "acme/app", SourceWorkLogReference: "effort/run/claim"}
	extra := parkedLocalEventExtra(request, member, "effort/run/target")
	if extra["resume_id"] != request.ResumeID || extra["repository"] != member.Repository || extra["target_work_log_reference"] != "effort/run/target" {
		t.Fatalf("parked local event extra = %#v", extra)
	}
	options := ParkedTargetCompletionOptions{
		Request: request, RequestDigest: sessionmove.Digest("digest"), Member: member,
		Successor: sessionlaunch.Result{AttemptID: "attempt", AttemptIndex: 2, PID: 1234, StartedAt: at},
	}
	event := parkedTargetCompletionEvent(options, "effort/run/target")
	if event.Result != "completed" || event.At != at || event.Extra["attempt_id"] != "attempt" || event.Extra["pid"] != 1234 {
		t.Fatalf("parked target completion event = %#v", event)
	}
}

func TestZeroCoverageBatchValueHelpers(t *testing.T) {
	t.Parallel()

	if got := branchInUseKey("acme/app", "feature"); got != "acme/app|feature" {
		t.Fatalf("branch in-use key = %q", got)
	}
	evidence := LocalGitEvidence{Branch: "feature", Head: strings.Repeat("a", 40), Dirty: true}
	pointer := ptrLocalGit(evidence)
	pointer.Branch = "changed"
	if evidence.Branch != "feature" || pointer.Head != evidence.Head {
		t.Fatalf("Git evidence pointer did not copy its value: original=%#v pointer=%#v", evidence, pointer)
	}
	first := LocalWorkLogEvent{ID: "event", Git: &evidence, Extra: map[string]any{"count": float64(1)}}
	second := first
	if !sameLocalEvent(first, second) {
		t.Fatal("equal local events were not equal")
	}
	second.ID = "other"
	if sameLocalEvent(first, second) {
		t.Fatal("different local events were equal")
	}
	if sameLocalEvent(LocalWorkLogEvent{Extra: map[string]any{"bad": make(chan int)}}, first) {
		t.Fatal("an unencodable local event was equal")
	}

	if got := worktreeretire.ArchiveManifestPreserve(retireArchiveManifest{}); got != "branch" {
		t.Fatalf("default archive preservation = %q", got)
	}
	if got := worktreeretire.ArchiveManifestPreserve(retireArchiveManifest{Preserve: "tag"}); got != "tag" {
		t.Fatalf("explicit archive preservation = %q", got)
	}
	if worktreebranches.MustAtoi("") != 0 || worktreebranches.MustAtoi("2048") != 2048 {
		t.Fatal("decimal parser returned an unexpected value")
	}
	left := &SupersessionReceipt{Version: 1, Repository: "acme/app", Task: "old"}
	right := *left
	if !worktreeproof.SameSupersessionReceipt(nil, nil) || worktreeproof.SameSupersessionReceipt(left, nil) || !worktreeproof.SameSupersessionReceipt(left, &right) {
		t.Fatal("supersession receipt equality mishandled nil or equal values")
	}
	right.Task = "new"
	if worktreeproof.SameSupersessionReceipt(left, &right) {
		t.Fatal("different supersession receipts were equal")
	}

	if got := trimSecureGitOutput([]byte(sandboxTempDirectoryWarning + "  result\n")); got != "result" {
		t.Fatalf("trimmed secure Git output = %q", got)
	}
	if !validWorktreeParentSegment("acme") || !validWorktreeParentSegment("github.com:443") || validWorktreeParentSegment("..") {
		t.Fatal("worktree parent segment validation is inconsistent")
	}
	root := filepath.Join(string(filepath.Separator), "projects", "acme")
	if !pathWithin(root, root) || !pathWithin(root, filepath.Join(root, "app")) || pathWithin(root, filepath.Join(filepath.Dir(root), "other")) {
		t.Fatal("path containment is inconsistent")
	}
	if got := worktreeAddArguments("checkout", "feature", "origin/main", true); !reflect.DeepEqual(got, []string{"worktree", "add", "--quiet", "checkout", "feature"}) {
		t.Fatalf("existing-branch worktree arguments = %#v", got)
	}
	if got := worktreeAddArguments("checkout", "feature", "origin/main", false); !reflect.DeepEqual(got, []string{"worktree", "add", "--quiet", "-b", "feature", "checkout", "origin/main"}) {
		t.Fatalf("new-branch worktree arguments = %#v", got)
	}
	prefix := taskBoundLocalStagePrefix("coverage-batch")
	if !strings.HasPrefix(prefix, ".wb-stage-task-") || !strings.HasSuffix(prefix, "-") || prefix != taskBoundLocalStagePrefix("coverage-batch") {
		t.Fatalf("task-bound stage prefix = %q", prefix)
	}
}
