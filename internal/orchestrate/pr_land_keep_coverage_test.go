package orchestrate

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// orchCovSourceCommits reads the commits a landing would carry, in the order
// the branch holds them, without going through GitHub.
func orchCovSourceCommits(t *testing.T, directory, from, to string) []SourceCommit {
	t.Helper()
	output := runEngineGit(t, directory, "log", "--reverse", "--format=%H%x00%s", from+".."+to)
	commits := make([]SourceCommit, 0, 4)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.SplitN(line, "\x00", 2)
		commit := SourceCommit{SHA: fields[0]}
		if len(fields) == 2 {
			commit.Subject = fields[1]
		}
		commits = append(commits, commit)
	}
	return commits
}

func TestOrchCovPlanKeptCommitsRefusesAnAmbiguousPrefix(t *testing.T) {
	t.Parallel()
	commits := []SourceCommit{{SHA: "aaa111"}, {SHA: "aaa222"}, {SHA: "bbb333"}}
	plan, refusal := planKeptCommits(commits, []string{"aaa"})
	if refusal == nil || refusal.code != LandRefusalKeepUnknownCommit {
		t.Fatalf("ambiguous prefix refusal = %+v", refusal)
	}
	if !strings.Contains(refusal.reason, "matches more than one commit") || len(plan.steps) != 0 {
		t.Fatalf("ambiguous prefix plan=%+v reason=%q", plan, refusal.reason)
	}
}

func TestOrchCovPlanKeptCommitsIgnoresBlankEntries(t *testing.T) {
	t.Parallel()
	commits := []SourceCommit{{SHA: "aaa111"}, {SHA: "bbb222"}}
	plan, refusal := planKeptCommits(commits, []string{"   ", ""})
	if refusal != nil {
		t.Fatal(refusal.reason)
	}
	if len(plan.kept) != 0 || len(plan.steps) != 1 || !plan.steps[0].aggregate || len(plan.steps[0].sources) != 2 {
		t.Fatalf("blank keep entries changed the plan: %+v", plan)
	}
}

func TestOrchCovPlanKeptCommitsKeepsEveryNamedCommitWhenNoneIsAggregated(t *testing.T) {
	t.Parallel()
	commits := []SourceCommit{{SHA: "aaa111"}, {SHA: "bbb222"}}
	plan, refusal := planKeptCommits(commits, []string{"bbb", "aaa"})
	if refusal != nil {
		t.Fatal(refusal.reason)
	}
	if len(plan.steps) != 2 || plan.steps[0].aggregate || plan.steps[1].aggregate {
		t.Fatalf("all-kept plan = %+v", plan)
	}
	if strings.Join(plan.kept, ",") != "aaa111,bbb222" {
		t.Fatalf("kept = %v, want them sorted", plan.kept)
	}
}

func TestOrchCovBuildAtRefusesABuildItCannotInfer(t *testing.T) {
	t.Parallel()
	refusal := buildAt(context.Background(), t.TempDir(), SourceCommit{SHA: "0123456789abcdef"}, nil)
	if refusal == nil || refusal.code != LandRefusalKeepDoesNotBuild {
		t.Fatalf("uninferable build refusal = %+v", refusal)
	}
	if !strings.Contains(refusal.reason, "cannot infer this repository's build") ||
		!strings.Contains(refusal.command, "--build-command") {
		t.Fatalf("uninferable build refusal = %+v", refusal)
	}
}

// TestOrchCovBuildAtReportsAFailedBuildAndAcceptsAPassingOne,
// TestOrchCovCommitsBetweenAndPatchIdentityDescribeOneCommit,
// TestOrchCovPatchIdentityHasNoIdentityForAMergeCommit,
// TestOrchCovMapLandedCommitsPairsKeptSourcesByPatchIdentity,
// TestOrchCovMapLandedCommitsLeavesEverythingUnpairedWithoutAnAggregate,
// TestOrchCovRewriteBranchForKeptCommits* and
// TestOrchCovLandKeepingCommitsRewritesThePublishedBranch moved to
// pr_land_keep_e2e_test.go (spec/plans/coverage-to-100 task-17): each calls
// a function that now runs real git through orchestrateGit/orchestrateRunner
// (internal/runner), which task-24's runtime guard blocks outside the e2e
// tier.

func TestOrchCovDefaultBuildCommandRecognisesOnlyGo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if got := defaultBuildCommand(dir); got != nil {
		t.Fatalf("non-Go repository build command = %v", got)
	}
	writeEngineFile(t, filepath.Join(dir, "go.mod"), "module example.test/app\n\ngo 1.24\n")
	got := defaultBuildCommand(dir)
	if len(got) != 3 || got[0] != "go" || got[1] != "build" || got[2] != "./..." {
		t.Fatalf("Go repository build command = %v", got)
	}
}

func TestOrchCovLastLinesKeepsTheTailOfTheOutput(t *testing.T) {
	t.Parallel()
	if got := lastLines("a\nb\nc\n", 5); got != "a\nb\nc" {
		t.Fatalf("short output = %q", got)
	}
	if got := lastLines("a\nb\nc", 2); got != "b\nc" {
		t.Fatalf("trimmed output = %q", got)
	}
}

// runGit itself (the direct exec.CommandContext helper this file used to
// define) moved onto orchestrateGit's named port methods and
// internal/gitcli.Client.run (spec/plans/coverage-to-100 task-17); its own
// error-formatting behaviour is now internal/gitcli/gitcli_test.go's
// TestClientRevParseWrapsAFailureAsGitError and its neighbours, run against
// runnertest.Fake rather than a real `git init` in a unit test.

func TestOrchCovShellQuoteEscapesSingleQuotes(t *testing.T) {
	t.Parallel()
	if got := shellQuote("a'b"); got != `'a'\''b'` {
		t.Fatalf("shellQuote = %q", got)
	}
}

func TestOrchCovLandKeepingCommitsRefusesWithoutAnIdentifiableCheckout(t *testing.T) {
	fixture := newLandFixture(t, "candidate", "a.txt", "b.txt")
	commits := orchCovSourceCommits(t, fixture.canonical, fixture.baseSHA, fixture.headSHA)
	view := PullRequestView{Number: 7, Title: "feat: the change"}
	view.Head.Ref, view.Head.SHA, view.Base.Ref = "candidate", fixture.headSHA, "main"

	options := PullRequestLandOptions{
		Repository: "acme/app", ProjectsRoot: t.TempDir(),
		KeepCommits: []string{commits[0].SHA}, Reason: "standalone",
	}
	_, _, refusal, err := landKeepingCommits(context.Background(), options, view, commits, "7", "")
	if err != nil {
		t.Fatal(err)
	}
	if refusal == nil || refusal.code != LandRefusalKeepNeedsCheckout {
		t.Fatalf("missing checkout refusal = %+v", refusal)
	}
}

func TestOrchCovLocateBranchCheckoutFindsTheTaskThatOwnsTheBranch(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "keep-task", "wb/keep/candidate", "candidate.txt", "one\n")

	canonical, task, found, err := locateBranchCheckout(context.Background(), fixture.githubDir, "acme/app", "wb/keep/candidate", "main")
	if err != nil {
		t.Fatal(err)
	}
	if !found || task != "keep-task" || filepath.Clean(canonical) != filepath.Clean(fixture.canonical) {
		t.Fatalf("located canonical=%q task=%q found=%t", canonical, task, found)
	}
	if filepath.Clean(canonical) != filepath.Clean(source.CanonicalDir) || source.Branch != "wb/keep/candidate" {
		t.Fatalf("located %q for a source created at %q on %q", canonical, source.CanonicalDir, source.Branch)
	}
	if _, _, found, err := locateBranchCheckout(context.Background(), fixture.githubDir, "acme/app", "wb/keep/absent", "main"); err != nil || found {
		t.Fatalf("unowned branch found=%t err=%v", found, err)
	}
}
