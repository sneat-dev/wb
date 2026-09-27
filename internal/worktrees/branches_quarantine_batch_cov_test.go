package worktrees

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/testenv"
)

func TestQuarantineValidationRejectsDuplicateAndNormalizesRequests(t *testing.T) {
	sha := strings.Repeat("a", 40)
	requests, err := validateQuarantineRequests([]BranchQuarantineRequest{
		{Repository: " zeta/app ", Ref: " feature/b ", SHA: " " + sha + " ", Reason: " old "},
		{Repository: " acme/app ", Ref: " feature/a ", SHA: sha, Reason: " done "},
	})
	if err != nil || len(requests) != 2 || requests[0].Repository != "acme/app" || requests[0].Ref != "feature/a" || requests[1].SHA != sha || requests[1].Reason != "old" {
		t.Fatalf("normalized requests = (%#v, %v)", requests, err)
	}
	_, err = validateQuarantineRequests([]BranchQuarantineRequest{
		{Repository: "acme/app", Ref: "feature/a", Reason: "one"},
		{Repository: " acme/app ", Ref: " feature/a ", Reason: "two"},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate request error = %v", err)
	}
	for _, tc := range []struct {
		name    string
		request BranchQuarantineRequest
		want    string
	}{
		{"repository", BranchQuarantineRequest{Repository: "acme", Ref: "feature/a", Reason: "old"}, "repository"},
		{"ref", BranchQuarantineRequest{Repository: "acme/app", Ref: "bad ref", Reason: "old"}, "ref"},
		{"sha", BranchQuarantineRequest{Repository: "acme/app", Ref: "feature/a", SHA: "bad", Reason: "old"}, "SHA"},
		{"reason", BranchQuarantineRequest{Repository: "acme/app", Ref: "feature/a"}, "reason"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateQuarantineRequests([]BranchQuarantineRequest{tc.request})
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.want)) {
				t.Fatalf("validation error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestQuarantineRepositoryDiscoveryAndDigestFailures(t *testing.T) {
	fixture := newGitFixture(t)
	paths, err := quarantineRepositoryPaths(fixture.projectsRoot, []BranchQuarantineRequest{{Repository: "acme/app"}})
	if err != nil || paths["acme/app"] != fixture.canonical {
		t.Fatalf("discovered path = (%#v, %v)", paths, err)
	}
	if _, err := quarantineRepositoryPaths(fixture.projectsRoot, []BranchQuarantineRequest{{Repository: "other/missing"}}); err == nil || !strings.Contains(err.Error(), "not discovered") {
		t.Fatalf("missing repository error = %v", err)
	}
	missing := filepath.Join(t.TempDir(), "missing.json")
	if _, err := QuarantineManifestDigest(missing); !os.IsNotExist(err) {
		t.Fatalf("missing digest input error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := QuarantineManifestDigest(path)
	if err != nil || len(first) != 64 {
		t.Fatalf("digest a = (%q, %v)", first, err)
	}
	if err := os.WriteFile(path, []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := QuarantineManifestDigest(path)
	if err != nil || first == second {
		t.Fatalf("changed content digest = (%q, %v), first %q", second, err, first)
	}
}

func TestQuarantinePlanRejectsMovedProtectedAndCheckedOutRefs(t *testing.T) {
	fixture := newGitFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	missing := planBranchQuarantine(ctx, fixture.projectsRoot, fixture.canonical, BranchQuarantineRequest{Repository: "acme/app", Ref: "missing", Reason: "old"}, now)
	if missing.Outcome != "refused" || !strings.Contains(missing.Error, "source ref unavailable") {
		t.Fatalf("missing ref plan = %#v", missing)
	}
	mainSHA := gitTestOutput(t, fixture.canonical, "rev-parse", "main")
	moved := planBranchQuarantine(ctx, fixture.projectsRoot, fixture.canonical, BranchQuarantineRequest{Repository: "acme/app", Ref: "main", SHA: strings.Repeat("a", 40), Reason: "old"}, now)
	if moved.Outcome != "refused" || !strings.Contains(moved.Error, "source moved") {
		t.Fatalf("stale SHA plan = %#v", moved)
	}
	protected := planBranchQuarantine(ctx, fixture.projectsRoot, fixture.canonical, BranchQuarantineRequest{Repository: "acme/app", Ref: "main", SHA: mainSHA, Reason: "old"}, now)
	if protected.Outcome != "refused" || !strings.Contains(protected.Error, "protected") {
		t.Fatalf("protected plan = %#v", protected)
	}
	gitTest(t, fixture.canonical, "checkout", "-b", "feature/in-use")
	checked := planBranchQuarantine(ctx, fixture.projectsRoot, fixture.canonical, BranchQuarantineRequest{Repository: "acme/app", Ref: "feature/in-use", Reason: "old"}, now)
	if checked.Outcome != "refused" || !strings.Contains(checked.Error, "protected") {
		t.Fatalf("current branch plan = %#v", checked)
	}
}

func TestQuarantineApplyRejectsChangesBeforeCAS(t *testing.T) {
	fixture := newGitFixture(t)
	ctx := context.Background()
	gitTest(t, fixture.canonical, "branch", "feature/old")
	head := gitTestOutput(t, fixture.canonical, "rev-parse", "feature/old")
	for _, tc := range []struct{ name, ref, sha, want string }{
		{"missing", "feature/missing", head, "source disappeared"},
		{"moved", "feature/old", strings.Repeat("a", 40), "source moved"},
		{"protected", "main", head, "protected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := BranchQuarantineResult{BranchQuarantineRequest: BranchQuarantineRequest{Repository: "acme/app", Ref: tc.ref, SHA: tc.sha}, Destination: "retired/test", Outcome: "planned"}
			applyBranchQuarantine(ctx, fixture.projectsRoot, fixture.canonical, &result)
			if result.Outcome != "failed" || !strings.Contains(result.Error, tc.want) {
				t.Fatalf("apply result = %#v, want %q", result, tc.want)
			}
		})
	}
}

func TestOpenPullRequestUsingBranchAsBaseDistinguishesMatchingAndFailedQueries(t *testing.T) {
	bin := t.TempDir()
	script := filepath.Join(bin, "gh")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeScript := func(body string) {
		t.Helper()
		if err := testenv.WriteExecutableFile(script, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	writeScript("printf '%s\\n' '[{\"number\":17,\"url\":\"https://example.test/pr/17\",\"state\":\"open\",\"base\":{\"ref\":\"feature/old\"},\"head\":{\"sha\":\"abc\"}}]'\n")
	pull, err := openPullRequestUsingBranchAsBase(ctx, t.TempDir(), "acme/app", "feature/old")
	if err != nil || pull == nil || pull.Number != 17 || pull.Base != "feature/old" {
		t.Fatalf("matching base pull = (%#v, %v)", pull, err)
	}
	pull, err = openPullRequestUsingBranchAsBase(ctx, t.TempDir(), "acme/app", "feature/other")
	if err != nil || pull != nil {
		t.Fatalf("unmatched base pull = (%#v, %v)", pull, err)
	}
	writeScript("printf 'not-json\\n'\n")
	if _, err := openPullRequestUsingBranchAsBase(ctx, t.TempDir(), "acme/app", "feature/old"); err == nil || !strings.Contains(err.Error(), "decode open pull requests") {
		t.Fatalf("invalid JSON error = %v", err)
	}
	writeScript("echo unavailable >&2\nexit 7\n")
	if _, err := openPullRequestUsingBranchAsBase(ctx, t.TempDir(), "acme/app", "feature/old"); err == nil || !strings.Contains(err.Error(), "query open pull requests") {
		t.Fatalf("failed query error = %v", err)
	}
}

func TestQuarantinePlanRejectsExistingRetiredDestination(t *testing.T) {
	fixture := newGitFixture(t)
	bin := t.TempDir()
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nprintf '[]\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	gitTest(t, fixture.canonical, "branch", "feature/old")
	sha := gitTestOutput(t, fixture.canonical, "rev-parse", "feature/old")
	now := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	destination := retiredBranchDestination(now, "feature/old", sha)
	gitTest(t, fixture.canonical, "branch", destination)
	result := planBranchQuarantine(context.Background(), fixture.projectsRoot, fixture.canonical, BranchQuarantineRequest{Repository: "acme/app", Ref: "feature/old", SHA: sha, Reason: "old"}, now)
	if result.Outcome != "refused" || !strings.Contains(result.Error, "destination already exists") {
		t.Fatalf("destination collision plan = %#v", result)
	}
}

func TestQuarantinePlanRejectsUnprovableAndOpenPullRequests(t *testing.T) {
	fixture := newGitFixture(t)
	gitTest(t, fixture.canonical, "branch", "feature/old")
	sha := gitTestOutput(t, fixture.canonical, "rev-parse", "feature/old")
	bin := t.TempDir()
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nprintf '%s\\n' \"$WB_TEST_GH_BODY\"\nexit \"${WB_TEST_GH_EXIT:-0}\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	now := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	request := BranchQuarantineRequest{Repository: "acme/app", Ref: "feature/old", SHA: sha, Reason: "old"}
	for _, tc := range []struct{ name, body, exit, want string }{
		{"query failed", "unavailable", "7", "cannot prove pull-request safety"},
		{"invalid response", "not-json", "0", "cannot prove pull-request safety"},
		{"open head", `[{"number":19,"html_url":"https://example.test/pull/19","state":"open","base":{"ref":"main"},"head":{"ref":"feature/old","sha":"` + sha + `","repo":{"full_name":"acme/app"}}}]`, "0", "head of open pull request"},
		{"open base", `[{"number":20,"html_url":"https://example.test/pull/20","state":"open","base":{"ref":"feature/old"},"head":{"ref":"other","sha":"` + sha + `","repo":{"full_name":"acme/app"}}}]`, "0", "base of open pull request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("WB_TEST_GH_BODY", tc.body)
			t.Setenv("WB_TEST_GH_EXIT", tc.exit)
			result := planBranchQuarantine(context.Background(), fixture.projectsRoot, fixture.canonical, request, now)
			if result.Outcome != "refused" || !strings.Contains(result.Error, tc.want) {
				t.Fatalf("pull request safety plan = %#v, want %q", result, tc.want)
			}
		})
	}
}

func TestQuarantineApplyRefusesDestinationAppearingBeforeCAS(t *testing.T) {
	fixture := newGitFixture(t)
	gitTest(t, fixture.canonical, "branch", "feature/old")
	sha := gitTestOutput(t, fixture.canonical, "rev-parse", "feature/old")
	destination := retiredBranchDestination(time.Now(), "feature/old", sha)
	gitTest(t, fixture.canonical, "branch", destination)
	result := BranchQuarantineResult{BranchQuarantineRequest: BranchQuarantineRequest{Repository: "acme/app", Ref: "feature/old", SHA: sha}, Destination: destination, Outcome: "planned"}
	applyBranchQuarantine(context.Background(), fixture.projectsRoot, fixture.canonical, &result)
	if result.Outcome != "failed" || !strings.Contains(result.Error, "destination appeared") {
		t.Fatalf("destination collision apply = %#v", result)
	}
	if !gitRefExists(fixture.canonical, "refs/heads/feature/old") {
		t.Fatal("source changed despite destination collision")
	}
}

func TestBranchQuarantineManifestRefusesFlattenedDestinationCollision(t *testing.T) {
	fixture := newGitFixture(t)
	gitTest(t, fixture.canonical, "branch", "feature/a-b")
	gitTest(t, fixture.canonical, "branch", "feature/a/b")
	sha := gitTestOutput(t, fixture.canonical, "rev-parse", "main")
	bin := t.TempDir()
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nprintf '[]\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	manifest := BranchQuarantineManifest{Entries: []BranchQuarantineRequest{
		{Repository: "acme/app", Ref: "feature/a-b", SHA: sha, Reason: "old"},
		{Repository: "acme/app", Ref: "feature/a/b", SHA: sha, Reason: "old"},
	}}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	outcome, err := BranchQuarantine(context.Background(), BranchQuarantineOptions{
		ProjectsRoot: fixture.projectsRoot, Manifest: path,
		Now: func() time.Time { return time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil || len(outcome.Results) != 2 {
		t.Fatalf("collision plan = (%#v, %v)", outcome, err)
	}
	for _, result := range outcome.Results {
		if result.Outcome != "refused" || !strings.Contains(result.Error, "destinations collide") {
			t.Fatalf("colliding result = %#v", result)
		}
		if !gitRefExists(fixture.canonical, "refs/heads/"+result.Ref) {
			t.Fatalf("source %s changed during planning", result.Ref)
		}
	}
}

func TestBranchQuarantineRejectsReusedReportDirectoryBeforeChangingRef(t *testing.T) {
	fixture := newGitFixture(t)
	gitTest(t, fixture.canonical, "branch", "feature/old")
	sha := gitTestOutput(t, fixture.canonical, "rev-parse", "feature/old")
	bin := t.TempDir()
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nprintf '[]\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	reportDir := filepath.Join(t.TempDir(), "existing")
	if err := os.Mkdir(reportDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outcome, err := BranchQuarantine(context.Background(), BranchQuarantineOptions{
		ProjectsRoot: fixture.projectsRoot, Repository: "acme/app", Branch: "feature/old", SHA: sha,
		Reason: "old", Apply: true, ReportDir: reportDir,
	})
	if err == nil || !strings.Contains(err.Error(), "reserve exclusive quarantine report directory") {
		t.Fatalf("reused report directory outcome = (%#v, %v)", outcome, err)
	}
	if !gitRefExists(fixture.canonical, "refs/heads/feature/old") {
		t.Fatal("source changed before report directory was reserved")
	}
}
