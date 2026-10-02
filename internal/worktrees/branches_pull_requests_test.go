package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
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
	if err := testenv.WriteExecutableFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_TEST_HEAD_PRS", head)
	t.Setenv("WB_TEST_BASE_PRS", base)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
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
			if err := testenv.WriteExecutableFile(script, []byte(content), 0o755); err != nil {
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
