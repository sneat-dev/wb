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
		"*head=*state=all*|*head=*state=open*) printf '%s\\n' \"$WB_TEST_HEAD_PRS\";;\n" +
		"*base=*state=open*) printf '%s\\n' \"$WB_TEST_BASE_PRS\";;\n" +
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
		`{"number":10,"html_url":"https://example.test/10","state":"open","head":{"ref":"fork-child"},"base":{"ref":"feature/shared","repo":{"full_name":"elsewhere/fork"}}}]`
	installBranchPullRequestFixture(t, head, base)
	evidence := exactBranchPullRequests(context.Background(), t.TempDir(), "acme/app", "feature/shared")
	if evidence.err != nil {
		t.Fatal(evidence.err)
	}
	if len(evidence.requests) != 4 {
		t.Fatalf("requests = %#v, want three head and one open base row", evidence.requests)
	}
	if evidence.openHead == nil || evidence.openHead.Number != 4 || evidence.openBase == nil || evidence.openBase.Number != 8 {
		t.Fatalf("open roles = head %#v, base %#v", evidence.openHead, evidence.openBase)
	}
	states := map[int]string{}
	for _, request := range evidence.requests {
		states[request.Number] = request.Role + ":" + request.State
	}
	for number, want := range map[int]string{4: "head:open", 5: "head:merged", 6: "head:closed", 8: "base:open"} {
		if states[number] != want {
			t.Errorf("PR #%d = %q, want %q", number, states[number], want)
		}
	}
}

func TestExactBranchPullRequestsReadsEveryPaginatedArray(t *testing.T) {
	head := `[{"number":1,"html_url":"https://example.test/1","state":"closed","merged_at":"2026-09-01T00:00:00Z","head":{"ref":"feature/paged","repo":{"full_name":"acme/app"}},"base":{"ref":"main"}}]` + "\n" +
		`[{"number":2,"html_url":"https://example.test/2","state":"open","head":{"ref":"feature/paged","repo":{"full_name":"acme/app"}},"base":{"ref":"main"}}]`
	installBranchPullRequestFixture(t, head, `[]`)
	evidence := exactBranchPullRequests(context.Background(), t.TempDir(), "acme/app", "feature/paged")
	if evidence.err != nil {
		t.Fatal(evidence.err)
	}
	if len(evidence.requests) != 2 || evidence.requests[0].Number != 1 || evidence.requests[1].Number != 2 ||
		evidence.openHead == nil || evidence.openHead.Number != 2 {
		t.Fatalf("second page was not included: %#v", evidence)
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

func TestExactBranchPullRequestsRejectsUnverifiableBaseIdentity(t *testing.T) {
	installBranchPullRequestFixture(t, `[]`,
		`[{"number":13,"state":"open","head":{"ref":"child"},"base":{"ref":"feature/ambiguous"}}]`)
	evidence := exactBranchPullRequests(context.Background(), t.TempDir(), "acme/app", "feature/ambiguous")
	if evidence.err == nil || !strings.Contains(evidence.err.Error(), "base pull request #13") {
		t.Fatalf("missing base repository identity did not fail closed: %#v", evidence)
	}
}

func TestExactBranchPullRequestsRejectsInvalidIdentityAndBaseQueryFailure(t *testing.T) {
	if evidence := exactBranchPullRequests(context.Background(), t.TempDir(), "acme", "feature/x"); evidence.err == nil {
		t.Fatal("invalid repository was accepted")
	}
	installBranchPullRequestFixture(t, `[]`, `not-json`)
	evidence := exactBranchPullRequests(context.Background(), t.TempDir(), "acme/app", "feature/x")
	if evidence.err == nil || !strings.Contains(evidence.err.Error(), "query base pull requests") {
		t.Fatalf("base query failure was hidden: %#v", evidence)
	}
}

func TestExactBranchPullRequestsIgnoresUnknownState(t *testing.T) {
	installBranchPullRequestFixture(t,
		`[{"number":14,"state":"drafting","head":{"ref":"feature/x","repo":{"full_name":"acme/app"}},"base":{"ref":"main"}}]`,
		`[]`)
	evidence := exactBranchPullRequests(context.Background(), t.TempDir(), "acme/app", "feature/x")
	if evidence.err != nil || len(evidence.requests) != 0 {
		t.Fatalf("unknown PR state was reported as actionable: %#v", evidence)
	}
}

func TestExactBranchPullRequestsRejectsMalformedOrEmptyPages(t *testing.T) {
	for _, test := range []struct{ name, body, want string }{
		{"malformed", `[{`, "decode"},
		{"null", `null`, "got null"},
		{"empty", ``, "empty response"},
	} {
		t.Run(test.name, func(t *testing.T) {
			installBranchPullRequestFixture(t, test.body, `[]`)
			evidence := exactBranchPullRequests(context.Background(), t.TempDir(), "acme/app", "feature/x")
			if evidence.err == nil || !strings.Contains(evidence.err.Error(), test.want) {
				t.Fatalf("invalid GitHub payload %q was accepted: %#v", test.body, evidence)
			}
		})
	}
}

func TestBranchListReportsPullRequestQueryFailureOnRemoteRow(t *testing.T) {
	fixture := newGitFixture(t)
	gitTest(t, fixture.canonical, "checkout", "-b", "feature/unavailable")
	writeAndCommit(t, fixture.canonical, "unavailable.txt", "v1\n", "unavailable")
	gitTest(t, fixture.canonical, "checkout", "main")
	gitTest(t, fixture.canonical, "push", "origin", "feature/unavailable")
	installPoisonedGitHubFixture(t)
	defaultOutcome, err := BranchList(context.Background(), BranchListOptions{
		ProjectsRoot: fixture.projectsRoot, Scope: BranchScopeRemote,
		Repository: "acme/app", Branch: "feature/unavailable",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(defaultOutcome.Entries) != 1 || defaultOutcome.Entries[0].PullRequestQueried || defaultOutcome.Entries[0].PullRequestQueryFailed {
		t.Fatalf("default remote list unexpectedly queried GitHub: %#v", defaultOutcome.Entries)
	}
	outcome, err := BranchList(context.Background(), BranchListOptions{
		ProjectsRoot: fixture.projectsRoot, Scope: BranchScopeRemote,
		Repository: "acme/app", Branch: "feature/unavailable", WithPRs: true,
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

func TestBranchListWithPRsIncludesInUseRemoteBranch(t *testing.T) {
	fixture := newGitFixture(t)
	worktreeDir := filepath.Join(t.TempDir(), "in-use")
	gitTest(t, fixture.canonical, "worktree", "add", "-b", "feature/in-use-pr", worktreeDir, "main")
	writeAndCommit(t, worktreeDir, "in-use.txt", "v1\n", "in-use PR work")
	gitTest(t, fixture.canonical, "push", "origin", "feature/in-use-pr")
	installBranchPullRequestFixture(t,
		`[{"number":12,"html_url":"https://example.test/12","state":"open","head":{"ref":"feature/in-use-pr","repo":{"full_name":"acme/app"}},"base":{"ref":"main"}}]`,
		`[]`)
	outcome, err := BranchList(context.Background(), BranchListOptions{
		ProjectsRoot: fixture.projectsRoot, Scope: BranchScopeRemote,
		Repository: "acme/app", Branch: "feature/in-use-pr", WithPRs: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Entries) != 1 {
		t.Fatalf("entries = %#v, want one remote row", outcome.Entries)
	}
	entry := outcome.Entries[0]
	if entry.Disposition != BranchInUse || !entry.PullRequestQueried || entry.OpenPullRequest == nil || entry.OpenPullRequest.Number != 12 {
		t.Fatalf("in-use remote row lacks open PR evidence: %#v", entry)
	}
}

func TestBranchCleanupRechecksOpenPullRequestsBeforeRemoteDeletion(t *testing.T) {
	for _, test := range []struct{ name, mode, want string }{
		{"query failure", "fail", "recheck pull-request evidence"},
		{"new head PR", "head", "became the head of open pull request"},
		{"new base PR", "base", "became the base of open pull request"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGitFixture(t)
			gitTest(t, fixture.canonical, "checkout", "-b", "feature/race")
			writeAndCommit(t, fixture.canonical, "race.txt", "v1\n", "race work")
			gitTest(t, fixture.canonical, "checkout", "main")
			gitTest(t, fixture.canonical, "merge", "--no-ff", "-m", "merge race work", "feature/race")
			gitTest(t, fixture.canonical, "push", "origin", "main", "feature/race")

			binDir := t.TempDir()
			script := filepath.Join(binDir, "gh")
			calls := filepath.Join(t.TempDir(), "calls")
			if err := os.WriteFile(calls, []byte("0\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			content := "#!/bin/sh\nset -eu\n" +
				"count=$(cat \"$WB_TEST_GH_CALLS\")\ncount=$((count + 1))\nprintf '%s\\n' \"$count\" > \"$WB_TEST_GH_CALLS\"\n" +
				"if [ \"$count\" -le 2 ]; then printf '[]\\n'; exit 0; fi\n" +
				"case \"$WB_TEST_RACE_MODE\" in\n" +
				"fail) echo 'GitHub unavailable' >&2; exit 7;;\n" +
				"head) if [ \"$count\" -eq 3 ]; then printf '%s\\n' \"$WB_TEST_RACE_HEAD\"; else printf '[]\\n'; fi;;\n" +
				"base) if [ \"$count\" -eq 4 ]; then printf '%s\\n' \"$WB_TEST_RACE_BASE\"; else printf '[]\\n'; fi;;\n" +
				"esac\n"
			if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("WB_TEST_GH_CALLS", calls)
			t.Setenv("WB_TEST_RACE_MODE", test.mode)
			t.Setenv("WB_TEST_RACE_HEAD", `[{"number":21,"html_url":"https://example.test/21","state":"open","head":{"ref":"feature/race","repo":{"full_name":"acme/app"}},"base":{"ref":"main"}}]`)
			t.Setenv("WB_TEST_RACE_BASE", `[{"number":22,"html_url":"https://example.test/22","state":"open","head":{"ref":"child"},"base":{"ref":"feature/race","repo":{"full_name":"acme/app"}}}]`)
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			outcome, err := BranchCleanup(context.Background(), BranchCleanupOptions{
				ProjectsRoot: fixture.projectsRoot, Scope: BranchScopeRemote, Base: "main",
				Repository: "acme/app", Branch: "feature/race", Apply: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			result := resultFor(t, outcome, "feature/race")
			if result.Applied || result.Outcome != "failed" || !strings.Contains(result.Error, test.want) {
				t.Fatalf("new PR evidence did not refuse deletion: %#v", result)
			}
			if remoteBranchForTest(t, fixture.canonical, "feature/race") == "" {
				t.Fatal("remote branch was deleted after PR state changed")
			}
		})
	}
}
