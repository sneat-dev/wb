package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func installBranchPullRequestFixture(t *testing.T, head, base string) {
	t.Helper()
	binDir := t.TempDir()
	script := filepath.Join(binDir, "gh")
	content := "#!/bin/sh\nset -eu\n" +
		"case \"$3\" in\n" +
		"*head=*) printf '%s\\n' \"$WB_TEST_HEAD_PRS\";;\n" +
		"*base=*) printf '%s\\n' \"$WB_TEST_BASE_PRS\";;\n" +
		"*) echo \"unexpected gh endpoint: $3\" >&2; exit 2;;\n" +
		"esac\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_TEST_HEAD_PRS", head)
	t.Setenv("WB_TEST_BASE_PRS", base)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestExactBranchPullRequestsSeparatesRolesStatesAndRepository(t *testing.T) {
	head := `[{"number":4,"html_url":"https://example.test/4","state":"open","head":{"ref":"feature/shared","sha":"old","repo":{"full_name":"acme/app"}},"base":{"ref":"main"}},` +
		`{"number":5,"html_url":"https://example.test/5","state":"closed","merged_at":"2026-09-01T00:00:00Z","head":{"ref":"feature/shared","sha":"older","repo":{"full_name":"acme/app"}},"base":{"ref":"main"}},` +
		`{"number":6,"html_url":"https://example.test/6","state":"closed","head":{"ref":"feature/shared","sha":"oldest","repo":{"full_name":"acme/app"}},"base":{"ref":"main"}},` +
		`{"number":7,"html_url":"https://example.test/7","state":"open","head":{"ref":"feature/shared","sha":"fork","repo":{"full_name":"elsewhere/fork"}},"base":{"ref":"main"}}]`
	base := `[{"number":8,"html_url":"https://example.test/8","state":"open","head":{"ref":"child"},"base":{"ref":"feature/shared","repo":{"full_name":"acme/app"}}},` +
		`{"number":9,"html_url":"https://example.test/9","state":"closed","head":{"ref":"old-child"},"base":{"ref":"feature/shared","repo":{"full_name":"acme/app"}}},` +
		`{"number":10,"html_url":"https://example.test/10","state":"open","head":{"ref":"fork-child"},"base":{"ref":"feature/shared","repo":{"full_name":"elsewhere/fork"}}}]`
	installBranchPullRequestFixture(t, head, base)
	evidence := exactBranchPullRequests(context.Background(), t.TempDir(), "acme/app", "feature/shared")
	if evidence.err != nil {
		t.Fatal(evidence.err)
	}
	if len(evidence.requests) != 5 {
		t.Fatalf("requests = %#v, want three head and two base", evidence.requests)
	}
	if evidence.openHead == nil || evidence.openHead.Number != 4 || evidence.openBase == nil || evidence.openBase.Number != 8 {
		t.Fatalf("open roles = head %#v, base %#v", evidence.openHead, evidence.openBase)
	}
	states := map[int]string{}
	for _, request := range evidence.requests {
		states[request.Number] = request.Role + ":" + request.State
	}
	for number, want := range map[int]string{4: "head:open", 5: "head:merged", 6: "head:closed", 8: "base:open", 9: "base:closed"} {
		if states[number] != want {
			t.Errorf("PR #%d = %q, want %q", number, states[number], want)
		}
	}
}

func TestBranchCleanupRefusesContainedRemoteBranchUsedAsOpenBase(t *testing.T) {
	fixture := newGitFixture(t)
	gitTest(t, fixture.canonical, "checkout", "-b", "feature/base")
	writeAndCommit(t, fixture.canonical, "base.txt", "v1\n", "base work")
	gitTest(t, fixture.canonical, "checkout", "main")
	gitTest(t, fixture.canonical, "merge", "--no-ff", "-m", "merge base", "feature/base")
	gitTest(t, fixture.canonical, "push", "origin", "main", "feature/base")
	installBranchPullRequestFixture(t, `[]`, `[{"number":8,"html_url":"https://example.test/8","state":"open","head":{"ref":"child"},"base":{"ref":"feature/base","repo":{"full_name":"acme/app"}}}]`)
	outcome, err := BranchCleanup(context.Background(), BranchCleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Base: "main", Scope: BranchScopeRemote, Apply: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := resultFor(t, outcome, "feature/base")
	if result.Applied || !result.PullRequestQueried || result.OpenBasePullRequest == nil || !strings.Contains(result.SkipReason, "base of open pull request") {
		t.Fatalf("open base PR did not block deletion: %#v", result)
	}
	if remoteBranchForTest(t, fixture.canonical, "feature/base") == "" {
		t.Fatal("remote branch used as open PR base was deleted")
	}
}

func TestExactBranchPullRequestsNamesQueryFailure(t *testing.T) {
	installPoisonedGitHubFixture(t)
	evidence := exactBranchPullRequests(context.Background(), t.TempDir(), "acme/app", "feature/fail")
	if evidence.err == nil || !strings.Contains(evidence.err.Error(), "head pull requests") {
		t.Fatalf("query error = %v, want named head query failure", evidence.err)
	}
}

func TestExactBranchPullRequestsRejectsUnverifiableRepositoryIdentity(t *testing.T) {
	installBranchPullRequestFixture(t,
		`[{"number":11,"state":"open","head":{"ref":"feature/ambiguous"},"base":{"ref":"main"}}]`,
		`[]`)
	evidence := exactBranchPullRequests(context.Background(), t.TempDir(), "acme/app", "feature/ambiguous")
	if evidence.err == nil || !strings.Contains(evidence.err.Error(), "no repository identity") {
		t.Fatalf("missing same-repository identity did not fail closed: %#v", evidence)
	}
}

func TestBranchListReportsPullRequestQueryFailureOnRemoteRow(t *testing.T) {
	fixture := newGitFixture(t)
	gitTest(t, fixture.canonical, "checkout", "-b", "feature/unavailable")
	writeAndCommit(t, fixture.canonical, "unavailable.txt", "v1\n", "unavailable")
	gitTest(t, fixture.canonical, "checkout", "main")
	gitTest(t, fixture.canonical, "push", "origin", "feature/unavailable")
	installPoisonedGitHubFixture(t)
	outcome, err := BranchList(context.Background(), BranchListOptions{
		ProjectsRoot: fixture.projectsRoot, Scope: BranchScopeRemote,
		Repository: "acme/app", Branch: "feature/unavailable",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Entries) != 1 {
		t.Fatalf("entries = %#v, want one remote row", outcome.Entries)
	}
	entry := outcome.Entries[0]
	if !entry.PullRequestQueryFailed || !strings.Contains(entry.PullRequestQueryError, "head pull requests") {
		t.Fatalf("remote row hid query failure: %#v", entry)
	}
}
