package orchestrate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/landinglane"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestOrchCovMatchesHoldUsesPathMatchSemantics(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		slug     string
		patterns []string
		want     bool
	}{
		{name: "no patterns", slug: "acme/app"},
		{name: "blank patterns are ignored", slug: "acme/app", patterns: []string{"", "   "}},
		{name: "exact", slug: "acme/app", patterns: []string{"acme/app"}, want: true},
		{name: "owner glob", slug: "acme/app", patterns: []string{"acme/*"}, want: true},
		{name: "glob never crosses a slash", slug: "acme/team/app", patterns: []string{"acme/*"}},
		{name: "malformed pattern is ignored", slug: "acme/app", patterns: []string{"[", "acme/app"}, want: true},
		{name: "later pattern still matches", slug: "acme/app", patterns: []string{"other/app", "acme/app"}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := MatchesHold(test.slug, test.patterns); got != test.want {
				t.Fatalf("MatchesHold(%q, %v) = %t, want %t", test.slug, test.patterns, got, test.want)
			}
		})
	}
}

func TestOrchCovRunCommandReportsATimeout(t *testing.T) {
	t.Parallel()
	_, attempts, err := runCommand(context.Background(), 20*time.Millisecond, 0, t.TempDir(), "sh", "-c", "sleep 5")
	if err == nil || !strings.Contains(err.Error(), "timed out after 20ms") {
		t.Fatalf("timed-out command error = %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestOrchCovLastNonEmptyLineIgnoresTrailingBlanks(t *testing.T) {
	t.Parallel()
	if got := lastNonEmptyLine("first\n\n  second  \n\n"); got != "second" {
		t.Fatalf("lastNonEmptyLine = %q", got)
	}
	if got := lastNonEmptyLine("   \n\n"); got != "" {
		t.Fatalf("blank input = %q", got)
	}
}

func TestOrchCovFetchMemoIsANoOpWhenNilAndCountsWhenNot(t *testing.T) {
	t.Parallel()
	var nilMemo *FetchMemo
	if nilMemo.SkipFetch("/repo") {
		t.Fatal("a nil memo skipped a fetch")
	}
	if nilMemo.Skips() != 0 {
		t.Fatalf("nil memo skips = %d", nilMemo.Skips())
	}
	memo := NewFetchMemo()
	stale := time.Now().Add(-2 * FetchMemoMaxAge)
	memo.now = func() time.Time { return stale }
	memo.MarkFetched("/stale")
	memo.now = time.Now
	if memo.SkipFetch("/stale") {
		t.Fatal("a stale fetch was reused")
	}
	memo.now = time.Now
	memo.MarkFetched("/fresh")
	if !memo.SkipFetch("/fresh") {
		t.Fatal("a fresh, untouched fetch was not reused")
	}
	if memo.Skips() != 1 {
		t.Fatalf("memo skips = %d, want 1", memo.Skips())
	}
	memo.MarkTouched("/fresh")
	if memo.SkipFetch("/fresh") {
		t.Fatal("a touched clone was reused")
	}
}

func TestOrchCovGitHubReadReportsACommandFailure(t *testing.T) {
	orchCovInstallGH(t, `#!/bin/sh
echo "boom" >&2
exit 1
`)
	if output, err := githubRead(context.Background(), "", "api", "repos/acme/app"); err == nil || output != "" {
		t.Fatalf("githubRead = %q, %v, want a failure", output, err)
	}
}

func TestOrchCovLandingLaneGuardIsANoOpWithoutAResolvableProjectsRoot(t *testing.T) {
	for _, name := range []string{"WB_HOME", "HOME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH"} {
		t.Setenv(name, "")
	}
	// The state home derives from the projects root now, so an unusable
	// projects root is passed instead of relying on an unresolvable WB_HOME.
	blocker := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	projectsRoot := filepath.Join(blocker, "projects")
	if err := releaseLandingLane(projectsRoot, "acme/app", "main", "session-1"); err != nil {
		t.Fatalf("release without a resolvable projects root = %v, want nil", err)
	}
	if err := refreshLandingLaneHeartbeat(projectsRoot, "acme/app", "main", "session-1"); err != nil {
		t.Fatalf("heartbeat without a resolvable projects root = %v, want nil", err)
	}
	// Acquiring, unlike releasing, must fail loudly: a guard that silently
	// did not run would let two sessions land on one target.
	if _, err := acquireLandingLane(projectsRoot, "acme/app", "main", LaneGuardRequest{
		Owner: landinglane.Owner{WBSessionID: "session-1"},
	}); err == nil {
		t.Fatal("acquire without a resolvable projects root silently skipped the guard")
	}
}

func TestOrchCovReleaseLandingLaneIgnoresAnEmptySession(t *testing.T) {
	t.Parallel()
	if err := releaseLandingLane(t.TempDir(), "acme/app", "main", "  "); err != nil {
		t.Fatalf("release with no session = %v, want nil", err)
	}
}

func TestOrchCovLandingLaneHeartbeatRefreshesTheHeldLane(t *testing.T) {
	fixture := newEngineFixture(t)
	owner := landinglane.Owner{WBSessionID: "session-1", PID: os.Getpid()}
	if _, err := acquireLandingLane(fixture.githubDir, "acme/app", "main", LaneGuardRequest{Owner: owner}); err != nil {
		t.Fatal(err)
	}
	home, err := wbhome.Root(fixture.githubDir)
	if err != nil {
		t.Fatal(err)
	}
	before, found, err := landinglane.Read(home, "acme/app", "main")
	if err != nil || !found {
		t.Fatalf("lane record before heartbeat: found=%t err=%v", found, err)
	}

	stop := startLandingLaneHeartbeat(fixture.githubDir, "acme/app", "main", "session-1", 5*time.Millisecond)
	deadline := time.Now().Add(5 * time.Second)
	var after landinglane.Record
	for time.Now().Before(deadline) {
		after, _, err = landinglane.Read(home, "acme/app", "main")
		if err != nil {
			t.Fatal(err)
		}
		if after.Owner.HeartbeatAt.After(before.Owner.HeartbeatAt) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	if !after.Owner.HeartbeatAt.After(before.Owner.HeartbeatAt) {
		t.Fatalf("heartbeat did not advance: before=%s after=%s", before.Owner.HeartbeatAt, after.Owner.HeartbeatAt)
	}
	if after.Owner.WBSessionID != "session-1" {
		t.Fatalf("heartbeat changed the owner: %+v", after.Owner)
	}
}

func TestOrchCovLandingLaneHeartbeatClampsANonPositiveInterval(t *testing.T) {
	t.Parallel()
	// A non-positive interval has to be clamped to the documented default: a
	// zero duration would panic time.NewTicker inside the refresh goroutine,
	// taking the process with it.
	stop := startLandingLaneHeartbeat(t.TempDir(), "acme/app", "main", "session-1", 0)
	stop()
}

func TestOrchCovLandingLaneHeartbeatWithoutASessionIsANoOp(t *testing.T) {
	t.Parallel()
	stop := startLandingLaneHeartbeat(t.TempDir(), "acme/app", "main", "  ", time.Millisecond)
	stop()
}

func TestOrchCovWorktreeMergeLaneReleasableOnlyHoldsLiveReceipts(t *testing.T) {
	t.Parallel()
	for _, status := range []WorktreeMergeStatus{
		WorktreeMergePreparing, WorktreeMergePrepared, WorktreeMergePublished, WorktreeMergeChecksPending,
	} {
		if WorktreeMergeLaneReleasable(status) {
			t.Fatalf("a still-live receipt in status %q released its lane", status)
		}
	}
	for _, status := range []WorktreeMergeStatus{
		WorktreeMergeLanded, WorktreeMergeConflict, WorktreeMergeValidationFailed,
		WorktreeMergeChecksFailed, WorktreeMergeComplete, WorktreeMergeStatus("unknown"),
	} {
		if !WorktreeMergeLaneReleasable(status) {
			t.Fatalf("a terminal receipt in status %q kept its lane", status)
		}
	}
}

func TestOrchCovSectionFromHunkHeaderRejectsUnusableContext(t *testing.T) {
	t.Parallel()
	if got := sectionFromHunkHeader("@@ -1,3 +1,3 @@   \"dependencies\": {"); len(got) != 1 || got[0] != "dependencies" {
		t.Fatalf("hunk header section = %v", got)
	}
	for _, line := range []string{
		"@@ -1,3 +1,3 @@",
		"@@ no second marker",
		"@@ -1,3 +1,3 @@ not a json property",
		"@@ -1,3 +1,3 @@ \"dependencies\": []",
	} {
		if got := sectionFromHunkHeader(line); got != nil {
			t.Fatalf("sectionFromHunkHeader(%q) = %v, want nil", line, got)
		}
	}
}

func TestOrchCovNonMechanicalGoModuleRefusesGraphDirectives(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		patch string
		want  string
	}{
		{name: "require is a bump", patch: "@@ -1 +1 @@\n-require example.test/a v1.0.0\n+require example.test/a v1.1.0\n"},
		{name: "replace", patch: "@@ -1 +1 @@\n+replace example.test/a => ../a\n", want: "replace directive"},
		{name: "exclude", patch: "@@ -1 +1 @@\n+exclude example.test/a v1.0.0\n", want: "exclude directive"},
		{name: "go directive", patch: "@@ -1 +1 @@\n-go 1.22\n+go 1.23\n", want: "go directive"},
		{name: "toolchain", patch: "@@ -1 +1 @@\n+toolchain go1.23.1\n", want: "toolchain directive"},
		// A bare marker line survives as an empty entry, which carries no
		// directive to judge.
		{name: "empty patch line", patch: "+\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			reason := nonMechanicalGoModule("go.mod", test.patch)
			if test.want == "" {
				if reason != "" {
					t.Fatalf("reason = %q, want none", reason)
				}
				return
			}
			if !strings.Contains(reason, test.want) {
				t.Fatalf("reason = %q, want it to name %q", reason, test.want)
			}
		})
	}
}

func TestOrchCovNonMechanicalWorkspaceRefusesGraphRewritingKeys(t *testing.T) {
	t.Parallel()
	clean := nonMechanicalWorkspace("pnpm-workspace.yaml", "packages:\n  - apps/*\n")
	if clean != "" {
		t.Fatalf("clean workspace reason = %q", clean)
	}
	reason := nonMechanicalWorkspace("pnpm-workspace.yaml", "@@ -1 +1 @@\n overrides:\n-  semver: 7.5.0\n+  semver: 7.6.0\n")
	if !strings.Contains(reason, "overrides") || !strings.Contains(reason, "rewrites what the workspace resolves") {
		t.Fatalf("overrides reason = %q", reason)
	}
}

func TestOrchCovChangedPatchLinesSkipsFileHeaders(t *testing.T) {
	t.Parallel()
	lines := changedPatchLines("--- a/go.mod\n+++ b/go.mod\n@@ -1 +1 @@\n-require a v1\n+require a v2\nplain\n+\n")
	if len(lines) != 3 || lines[0] != "require a v1" || lines[1] != "require a v2" || lines[2] != "" {
		t.Fatalf("changed patch lines = %q", lines)
	}
}

func TestOrchCovNonMechanicalContentReasonsAboutAPackageManifest(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		file  string
		patch string
		want  string
	}{
		{
			name:  "dependency bump",
			patch: "--- a/package.json\n+++ b/package.json\n@@ -1,3 +1,3 @@\n \"dependencies\": {\n-  \"lodash\": \"^4.17.20\"\n+  \"lodash\": \"^4.17.21\"\n",
		},
		{
			name:  "blank diff line",
			patch: "--- a/package.json\n+++ b/package.json\n@@ -1,3 +1,3 @@\n \"dependencies\": {\n \n-  \"lodash\": \"^4.17.20\"\n+  \"lodash\": \"^4.17.21\"\n",
		},
		{
			name:  "changed section that is not a dependency section",
			patch: "@@ -1,3 +1,4 @@\n-\"scripts\": {\n+\"scripts\": {\n",
			want:  "which is not a dependency section",
		},
		{
			name:  "hunk header supplies the enclosing section",
			patch: "@@ -12,7 +12,7 @@   \"dependencies\": {\n-  \"lodash\": \"^4.17.20\"\n+  \"lodash\": \"^4.17.21\"\n",
		},
		{
			name:  "graph rewriting key inside a dependency section",
			patch: "@@ -1,3 +1,3 @@\n \"dependencies\": {\n+  \"catalog\": \"1.0.0\"\n",
			want:  "rewrites what the resolver produces",
		},
		{
			name:  "graph rewriting section",
			patch: "@@ -1,3 +1,3 @@   \"overrides\": {\n-  \"semver\": \"7.5.0\"\n+  \"semver\": \"7.6.0\"\n",
			want:  "rewrites what the resolver produces",
		},
		{
			name:  "outside any dependency section",
			patch: "@@ -1,3 +1,3 @@\n+  \"private\": true\n",
			want:  "which a dependency bump does not touch",
		},
		{
			name:  "a line that is not a single json property",
			patch: "@@ -1,3 +1,3 @@\n+  garbage without a colon\n",
			want:  "not a single JSON property",
		},
		{
			name:  "not a version range",
			patch: "@@ -1,3 +1,3 @@\n \"dependencies\": {\n+  \"build\": \"tsc && node x.js\"\n",
			want:  "not a version range",
		},
		{
			name:  "no diff at all",
			patch: "   \n",
			want:  "has no diff GitHub could show",
		},
		{
			name:  "lockfile carries no decision of its own",
			file:  "go.sum",
			patch: "--- a/go.sum\n+++ b/go.sum\n@@ -1 +1 @@\n-example.test/a v1.0.0 h1:x\n+example.test/a v1.1.0 h1:y\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			name := test.file
			if name == "" {
				name = "package.json"
			}
			reason := nonMechanicalContent(name, test.patch)
			if test.want == "" {
				if reason != "" {
					t.Fatalf("reason = %q, want none", reason)
				}
				return
			}
			if !strings.Contains(reason, test.want) {
				t.Fatalf("reason = %q, want it to contain %q", reason, test.want)
			}
		})
	}
}

func TestOrchCovJSONLineKeyRefusesUnparseableProperties(t *testing.T) {
	t.Parallel()
	key, value, ok := jsonLineKey(`"lodash": "^4.17.21",`)
	if !ok || key != "lodash" || value != `"^4.17.21"` {
		t.Fatalf("jsonLineKey = %q/%q/%t", key, value, ok)
	}
	for _, body := range []string{"not quoted", `"unterminated`, `"key" value`, ""} {
		if _, _, ok := jsonLineKey(body); ok {
			t.Fatalf("jsonLineKey(%q) accepted an unparseable property", body)
		}
	}
}

func TestOrchCovLooksLikeDependencyEntryAcceptsEveryVersionSpelling(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		value string
		want  bool
	}{
		{value: `"^4.17.21"`, want: true},
		{value: `"~1.2.3"`, want: true},
		{value: `">=1.0.0"`, want: true},
		{value: `"*"`, want: true},
		{value: `"1.2.3"`, want: true},
		{value: `"workspace:*"`, want: true},
		{value: `"catalog:default"`, want: true},
		{value: `"npm:other@1.0.0"`, want: true},
		{value: `"file:../local"`, want: true},
		{value: `"git+https://example.test/a.git"`, want: true},
		{value: `not json`, want: false},
		{value: `""`, want: false},
		{value: `"   "`, want: false},
		{value: `"tsc && node x.js"`, want: false},
		{value: `"latest"`, want: false},
	} {
		if got := looksLikeDependencyEntry(test.value); got != test.want {
			t.Fatalf("looksLikeDependencyEntry(%s) = %t, want %t", test.value, got, test.want)
		}
	}
}

func TestOrchCovTrimForMessageBoundsLongValues(t *testing.T) {
	t.Parallel()
	if got := trimForMessage("short"); got != "short" {
		t.Fatalf("short value = %q", got)
	}
	long := strings.Repeat("x", 100)
	got := trimForMessage(long)
	if len([]rune(got)) != 61 || !strings.HasSuffix(got, "…") {
		t.Fatalf("long value = %q", got)
	}
}

func TestOrchCovMechanicalVerdictSummaryExplainsItself(t *testing.T) {
	t.Parallel()
	if got := (MechanicalVerdict{Mechanical: true}).Summary(); !strings.Contains(got, "every changed line") {
		t.Fatalf("mechanical summary = %q", got)
	}
	if got := (MechanicalVerdict{}).Summary(); got != "the change is not a mechanical dependency bump" {
		t.Fatalf("reasonless summary = %q", got)
	}
	got := (MechanicalVerdict{Reasons: []string{"a.go is not a dependency manifest", "b.go is not a dependency manifest"}}).Summary()
	for _, want := range []string{"a.go", "b.go"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary %q is missing %q", got, want)
		}
	}
}

func TestOrchCovFindAbsorbedConflictAcknowledgementSkipsUnusableReportEntries(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSourceOnBase(t, fixture, "task-skip", "feature/skip", "main", "skip.txt", "skip\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main",
		Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	reportsDir := filepath.Dir(receipt.ReceiptPath)
	if err := os.MkdirAll(filepath.Join(reportsDir, "0000-dir.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{
		"0001-sidecar.json": "ignored",
		"0002-foo.ack.json": "ignored",
		"0003-notes.txt":    "ignored",
		"0004-garbage.json": "{not json",
	} {
		if err := os.WriteFile(filepath.Join(reportsDir, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A readable receipt for a different candidate is skipped rather than
	// mistaken for this one.
	other := receipt
	other.Candidate.Task = "another-task"
	other.Candidate.Worktree = filepath.Join(t.TempDir(), "elsewhere")
	encoded, err := jsonMarshalIndent(other)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reportsDir, "0005-other.json"), encoded, 0o644); err != nil {
		t.Fatal(err)
	}

	lookup, found, err := FindAbsorbedConflictAcknowledgement(fixture.githubDir, receipt.Candidate.Task, receipt.Candidate.Worktree)
	if err != nil {
		t.Fatal(err)
	}
	if !found || lookup.ReceiptPath != receipt.ReceiptPath || lookup.Acknowledged {
		t.Fatalf("lookup = %+v found=%t", lookup, found)
	}
}

func TestOrchCovFindAbsorbedConflictAcknowledgementFailsClosedOnATamperedSidecar(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSourceOnBase(t, fixture, "task-tamper", "feature/tamper", "main", "tamper.txt", "tamper\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main",
		Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absorbedConflictAcknowledgementPath(receipt.ReceiptPath), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, found, err := FindAbsorbedConflictAcknowledgement(fixture.githubDir, receipt.Candidate.Task, receipt.Candidate.Worktree); err == nil || found {
		t.Fatalf("tampered sidecar: found=%t err=%v, want an error", found, err)
	}
}

func TestOrchCovFindAbsorbedConflictAcknowledgementReportsAnUnreadableReportsDirectory(t *testing.T) {
	fixture := newEngineFixture(t)
	home, err := wbhome.Root(fixture.githubDir)
	if err != nil {
		t.Fatal(err)
	}
	reportsDir := filepath.Join(home, "reports", "worktree-merge")
	if err := os.MkdirAll(filepath.Dir(reportsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reportsDir, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := FindAbsorbedConflictAcknowledgement(fixture.githubDir, "task", "/worktree"); err == nil ||
		!strings.Contains(err.Error(), "read worktree-merge reports") {
		t.Fatalf("unreadable reports directory error = %v", err)
	}
}

func TestOrchCovFindAbsorbedConflictAcknowledgementNeedsAResolvableProjectsRoot(t *testing.T) {
	for _, name := range []string{"WB_HOME", "HOME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH"} {
		t.Setenv(name, "")
	}
	// The state home derives from the projects root now, so an unusable
	// projects root is passed instead of relying on an unresolvable WB_HOME.
	blocker := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := FindAbsorbedConflictAcknowledgement(filepath.Join(blocker, "projects"), "task", "/worktree"); err == nil {
		t.Fatal("a lookup without a resolvable projects root reported no error")
	}
}

func TestOrchCovRefreshPublishedCandidateRefusesUnusableInput(t *testing.T) {
	t.Parallel()
	if err := refreshPublishedWorktreeMergeCandidateTarget(context.Background(), nil, "abc", time.Minute, 0); err == nil ||
		!strings.Contains(err.Error(), "receipt is required") {
		t.Fatalf("nil receipt error = %v", err)
	}
	if err := refreshPublishedWorktreeMergeCandidateTarget(context.Background(), &WorktreeMergeReceipt{}, "  ", time.Minute, 0); err == nil ||
		!strings.Contains(err.Error(), "remote target revision is required") {
		t.Fatalf("empty target error = %v", err)
	}
}

func TestOrchCovRefreshPublishedCandidateReportsAnUnmergeableTarget(t *testing.T) {
	t.Parallel()
	dir := orchCovGitRepo(t)
	receipt := &WorktreeMergeReceipt{
		Candidate:   WorktreeMergeCandidate{Worktree: dir, SHA: "0123456789abcdef"},
		TargetSHA:   "fedcba9876543210",
		PullRequest: "https://example.test/acme/app/pull/41",
		ReceiptPath: "/tmp/receipt.json",
	}
	err := refreshPublishedWorktreeMergeCandidateTarget(context.Background(), receipt, "not-a-revision", time.Minute, 0)
	if err == nil || !strings.Contains(err.Error(), "failed to merge cleanly") ||
		!strings.Contains(err.Error(), "https://example.test/acme/app/pull/41") {
		t.Fatalf("unmergeable target error = %v", err)
	}
	// A refusal must leave the recorded candidate and target untouched.
	if receipt.Candidate.SHA != "0123456789abcdef" || receipt.TargetSHA != "fedcba9876543210" || len(receipt.TargetRefreshes) != 0 {
		t.Fatalf("refused refresh changed the receipt: %+v", receipt)
	}
}

func TestOrchCovRefreshPublishedCandidateNamesTheConflictingPaths(t *testing.T) {
	t.Parallel()
	dir := orchCovGitRepo(t)
	runEngineGit(t, dir, "checkout", "-b", "side")
	writeEngineFile(t, filepath.Join(dir, "conflict.txt"), "side\n")
	runEngineGit(t, dir, "add", "-A")
	runEngineGit(t, dir, "-c", "user.name=WB Test", "-c", "user.email=wb@example.test", "commit", "-m", "side change")
	runEngineGit(t, dir, "checkout", "main")
	writeEngineFile(t, filepath.Join(dir, "conflict.txt"), "main\n")
	runEngineGit(t, dir, "add", "-A")
	runEngineGit(t, dir, "-c", "user.name=WB Test", "-c", "user.email=wb@example.test", "commit", "-m", "main change")

	receipt := &WorktreeMergeReceipt{
		Candidate:   WorktreeMergeCandidate{Worktree: dir, SHA: "0123456789abcdef"},
		TargetSHA:   "fedcba9876543210",
		PullRequest: "https://example.test/acme/app/pull/41",
		ReceiptPath: "/tmp/receipt.json",
	}
	err := refreshPublishedWorktreeMergeCandidateTarget(context.Background(), receipt, "side", time.Minute, 0)
	if err == nil || !strings.Contains(err.Error(), "conflicts in conflict.txt") ||
		!strings.Contains(err.Error(), "wb worktree merge resume /tmp/receipt.json") {
		t.Fatalf("conflicting refresh error = %v", err)
	}
	// The failed merge was aborted, so the worktree is usable again.
	if status := strings.TrimSpace(runEngineGit(t, dir, "status", "--porcelain")); status != "" {
		t.Fatalf("aborted merge left the worktree dirty: %q", status)
	}
}

func TestOrchCovConflictingWorktreeMergePathsReportsAGitFailure(t *testing.T) {
	t.Parallel()
	if _, err := conflictingWorktreeMergePaths(context.Background(), t.TempDir()); err == nil {
		t.Fatal("conflicting paths accepted a non-repository")
	}
}

// orchCovGitRepo creates a one-commit repository on main. The repository is
// given its own identity because production code commits into it through plain
// `git` invocations (a merge, for example), which do not carry the `-c`
// overrides the fixture's own commits use. A developer machine lets git
// auto-derive an identity; a CI runner has none, and the commit fails there.
func orchCovGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runEngineGit(t, dir, "init", "-b", "main")
	runEngineGit(t, dir, "config", "user.name", "WB Test")
	runEngineGit(t, dir, "config", "user.email", "wb@example.test")
	writeEngineFile(t, filepath.Join(dir, "base.txt"), "base\n")
	runEngineGit(t, dir, "add", "-A")
	runEngineGit(t, dir, "commit", "-m", "initial")
	return dir
}

// jsonMarshalIndent keeps the fixtures readable without importing encoding/json
// at every call site.
func jsonMarshalIndent(value any) ([]byte, error) {
	return json.MarshalIndent(value, "", "  ")
}
