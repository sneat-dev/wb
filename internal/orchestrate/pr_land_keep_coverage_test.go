package orchestrate

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
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
	refusal := buildAt(context.Background(), nil, t.TempDir(), SourceCommit{SHA: "0123456789abcdef"}, nil)
	if refusal == nil || refusal.code != LandRefusalKeepDoesNotBuild {
		t.Fatalf("uninferable build refusal = %+v", refusal)
	}
	if !strings.Contains(refusal.reason, "cannot infer this repository's build") ||
		!strings.Contains(refusal.command, "--build-command") {
		t.Fatalf("uninferable build refusal = %+v", refusal)
	}
}

func TestOrchCovBuildAtReportsAFailedBuildAndAcceptsAPassingOne(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	build := []string{"go", "build", "./..."}
	run := runnertest.New(t)
	run.ExpectArgv(build, runner.Result{}, nil)
	run.ExpectArgv(build, runner.Result{CombinedOutput: "compile exploded\n"}, errors.New("exit status 1"))
	if refusal := buildAt(context.Background(), run, worktree, SourceCommit{SHA: "0123456789abcdef"}, build); refusal != nil {
		t.Fatalf("passing build refusal = %+v", refusal)
	}
	refusal := buildAt(context.Background(), run, worktree,
		SourceCommit{SHA: "0123456789abcdef", Subject: "add the thing"},
		build)
	if refusal == nil || refusal.code != LandRefusalKeepDoesNotBuild {
		t.Fatalf("failing build refusal = %+v", refusal)
	}
	if !strings.Contains(refusal.reason, "add the thing") || !strings.Contains(refusal.reason, "compile exploded") {
		t.Fatalf("failing build refusal = %+v", refusal)
	}
	if !strings.Contains(refusal.command, "0123456789ab") {
		t.Fatalf("failing build refusal command = %q", refusal.command)
	}
	for _, call := range run.Calls() {
		if call.Op != "RunOpts" || call.Dir != worktree || !call.Opts.CaptureCombined || strings.Join(call.Opts.Env, "\x00") != strings.Join(console.Env(), "\x00") {
			t.Fatalf("build runner call = %+v", call)
		}
	}
}

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

func TestOrchCovRunGitReportsFailuresWithTheirOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if output, err := runGit(context.Background(), dir, "init", "-b", "main"); err != nil {
		t.Fatalf("git init failed: %v (%s)", err, output)
	}
	_, err := runGit(context.Background(), dir, "rev-parse", "refs/heads/absent")
	if err == nil || !strings.Contains(err.Error(), "git rev-parse") {
		t.Fatalf("runGit failure = %v", err)
	}
}

func TestOrchCovShellQuoteEscapesSingleQuotes(t *testing.T) {
	t.Parallel()
	if got := shellQuote("a'b"); got != `'a'\''b'` {
		t.Fatalf("shellQuote = %q", got)
	}
}

func TestOrchCovCommitsBetweenAndPatchIdentityDescribeOneCommit(t *testing.T) {
	fixture := newLandFixture(t, "candidate", "a.txt", "b.txt", "c.txt")
	commits, err := commitsBetween(context.Background(), fixture.canonical, fixture.baseSHA, fixture.headSHA)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 3 || commits[0] != fixture.commitSHAs[0] || commits[2] != fixture.commitSHAs[2] {
		t.Fatalf("commits between = %v, want %v", commits, fixture.commitSHAs)
	}
	first, err := patchIdentity(context.Background(), fixture.canonical, commits[0])
	if err != nil || first == "" {
		t.Fatalf("patch identity = %q, err %v", first, err)
	}
	second, err := patchIdentity(context.Background(), fixture.canonical, commits[1])
	if err != nil || second == first {
		t.Fatalf("distinct commits shared a patch identity: %q vs %q (err %v)", first, second, err)
	}
}

func TestOrchCovPatchIdentityHasNoIdentityForAMergeCommit(t *testing.T) {
	fixture := newLandFixture(t, "candidate", "a.txt")
	runEngineGit(t, fixture.canonical, "checkout", "-b", "side", fixture.baseSHA)
	writeEngineFile(t, filepath.Join(fixture.canonical, "side.txt"), "side\n")
	runEngineGit(t, fixture.canonical, "add", "-A")
	runEngineGit(t, fixture.canonical, "commit", "-m", "side work")
	sideSHA := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, fixture.canonical, "checkout", "main")
	writeEngineFile(t, filepath.Join(fixture.canonical, "main.txt"), "main\n")
	runEngineGit(t, fixture.canonical, "add", "-A")
	runEngineGit(t, fixture.canonical, "commit", "-m", "main work")
	runEngineGit(t, fixture.canonical, "merge", "--no-ff", "-m", "merge side", sideSHA)
	mergeSHA := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))

	identity, err := patchIdentity(context.Background(), fixture.canonical, mergeSHA)
	if err != nil || identity != "" {
		t.Fatalf("merge commit patch identity = %q, err %v", identity, err)
	}

	// A source WB cannot fingerprint is left without a pairing rather than
	// guessed at, and the unclaimed landed commit still carries the aggregate.
	landed := []LandedCommit{
		{SourceSHA: mergeSHA, Subject: "merge side", Kept: true},
		{SourceSHA: sideSHA, Subject: "side work"},
	}
	mapped, err := MapLandedCommits(context.Background(), fixture.canonical, "main", fixture.baseSHA, landed)
	if err != nil {
		t.Fatal(err)
	}
	if mapped[0].LandedSHA != "" {
		t.Fatalf("unfingerprintable kept source was paired with %q", mapped[0].LandedSHA)
	}
	if mapped[1].LandedSHA == "" {
		t.Fatalf("aggregated source was left unpaired: %+v", mapped)
	}
}

func TestOrchCovMapLandedCommitsPairsKeptSourcesByPatchIdentity(t *testing.T) {
	fixture := newLandFixture(t, "candidate", "a.txt", "b.txt", "c.txt")
	runEngineGit(t, fixture.canonical, "checkout", "-b", "landed", fixture.baseSHA)
	runEngineGit(t, fixture.canonical, "cherry-pick", fixture.commitSHAs[1])
	keptLanded := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, fixture.canonical, "cherry-pick", "--no-commit", fixture.commitSHAs[0], fixture.commitSHAs[2])
	runEngineGit(t, fixture.canonical, "commit", "-m", "aggregate remaining")
	aggregate := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, fixture.canonical, "checkout", "main")

	landed := []LandedCommit{
		{SourceSHA: fixture.commitSHAs[1], Subject: "change b.txt", Kept: true},
		{SourceSHA: fixture.commitSHAs[0], Subject: "change a.txt"},
		{SourceSHA: fixture.commitSHAs[2], Subject: "change c.txt"},
	}
	mapped, err := MapLandedCommits(context.Background(), fixture.canonical, "landed", fixture.baseSHA, landed)
	if err != nil {
		t.Fatal(err)
	}
	if mapped[0].LandedSHA != keptLanded {
		t.Fatalf("kept source paired with %q, want %q", mapped[0].LandedSHA, keptLanded)
	}
	if mapped[1].LandedSHA != aggregate || mapped[2].LandedSHA != aggregate {
		t.Fatalf("aggregated sources = %q/%q, want %q", mapped[1].LandedSHA, mapped[2].LandedSHA, aggregate)
	}
	if !mapped[0].Kept || mapped[1].Kept || mapped[2].Kept {
		t.Fatalf("kept flags changed: %+v", mapped)
	}
}

func TestOrchCovMapLandedCommitsLeavesEverythingUnpairedWithoutAnAggregate(t *testing.T) {
	fixture := newLandFixture(t, "candidate", "a.txt", "b.txt")
	runEngineGit(t, fixture.canonical, "checkout", "-b", "landed", fixture.baseSHA)
	runEngineGit(t, fixture.canonical, "cherry-pick", fixture.commitSHAs[0])
	keptLanded := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, fixture.canonical, "checkout", "main")

	landed := []LandedCommit{{SourceSHA: fixture.commitSHAs[0], Subject: "change a.txt", Kept: true}}
	mapped, err := MapLandedCommits(context.Background(), fixture.canonical, "landed", fixture.baseSHA, landed)
	if err != nil {
		t.Fatal(err)
	}
	if mapped[0].LandedSHA != keptLanded {
		t.Fatalf("kept source paired with %q, want %q", mapped[0].LandedSHA, keptLanded)
	}
}

func TestOrchCovRewriteBranchForKeptCommitsLandsKeptAndAggregatedCommits(t *testing.T) {
	fixture := newLandFixture(t, "candidate", "a.txt", "b.txt", "c.txt")
	commits := orchCovSourceCommits(t, fixture.canonical, fixture.baseSHA, fixture.headSHA)
	if len(commits) != 3 {
		t.Fatalf("fixture commits = %+v", commits)
	}
	plan, refusal := planKeptCommits(commits, []string{commits[1].SHA})
	if refusal != nil {
		t.Fatal(refusal.reason)
	}
	view := PullRequestView{Number: 7, Title: "feat: the change"}
	view.Head.Ref, view.Head.SHA, view.Base.Ref = "candidate", fixture.headSHA, "main"

	run := runnertest.New(t)
	run.ExpectArgv([]string{"sh", "-c", "exit 0"}, runner.Result{}, nil)
	landed, head, refusal, err := rewriteBranchForKeptCommits(context.Background(), fixture.canonical,
		"acme/app", "candidate", fixture.baseSHA, plan, view, commits, "reviewer@example.test",
		"these two stand alone", []string{"sh", "-c", "exit 0"}, run)
	if err != nil {
		t.Fatal(err)
	}
	if refusal != nil {
		t.Fatalf("rewrite refusal = %+v", refusal)
	}
	if head == "" || head == fixture.headSHA || len(landed) != 3 {
		t.Fatalf("rewrite head=%q landed=%+v", head, landed)
	}
	if !landed[0].Kept || landed[0].SourceSHA != commits[1].SHA {
		t.Fatalf("kept pairing = %+v", landed[0])
	}
	if landed[1].Kept || landed[2].Kept {
		t.Fatalf("aggregated pairings marked kept: %+v", landed)
	}
	published := strings.TrimSpace(runEngineGit(t, fixture.root,
		"--git-dir="+fixture.remote, "rev-parse", "refs/heads/candidate"))
	if published != head {
		t.Fatalf("published head = %q, want %q", published, head)
	}
	subjects := strings.TrimSpace(runEngineGit(t, fixture.root,
		"--git-dir="+fixture.remote, "log", "--format=%s", fixture.baseSHA+"..candidate"))
	lines := strings.Split(subjects, "\n")
	if len(lines) != 2 || lines[0] != "feat: the change" || lines[1] != "change b.txt" {
		t.Fatalf("landed subjects = %q", subjects)
	}
}

func TestOrchCovRewriteBranchForKeptCommitsRefusesAStaleLease(t *testing.T) {
	fixture := newLandFixture(t, "candidate", "a.txt", "b.txt")
	commits := orchCovSourceCommits(t, fixture.canonical, fixture.baseSHA, fixture.headSHA)
	plan, refusal := planKeptCommits(commits, nil)
	if refusal != nil {
		t.Fatal(refusal.reason)
	}
	view := PullRequestView{Number: 7, Title: "feat: the change"}
	view.Head.Ref, view.Base.Ref = "candidate", "main"
	view.Head.SHA = strings.Repeat("b", 40)

	_, _, refusal, err := rewriteBranchForKeptCommits(context.Background(), fixture.canonical,
		"acme/app", "candidate", fixture.baseSHA, plan, view, commits, "", "", []string{"sh", "-c", "exit 0"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if refusal == nil || refusal.code != LandRefusalHeadMoved {
		t.Fatalf("stale lease refusal = %+v", refusal)
	}
	if !strings.Contains(refusal.command, "wb pr land acme/app#7") {
		t.Fatalf("stale lease command = %q", refusal.command)
	}
}

func TestOrchCovRewriteBranchForKeptCommitsRefusesAConflictWithoutMovingTheBranch(t *testing.T) {
	fixture := newLandFixture(t, "candidate", "a.txt", "b.txt", "c.txt")
	// The base now carries its own a.txt, so the first kept commit cannot
	// replay. The kept commit is planned first, which is the path this proves.
	writeEngineFile(t, filepath.Join(fixture.canonical, "a.txt"), "base owns this file\n")
	runEngineGit(t, fixture.canonical, "add", "-A")
	runEngineGit(t, fixture.canonical, "commit", "-m", "base takes a.txt")
	advanced := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	commits := orchCovSourceCommits(t, fixture.canonical, fixture.baseSHA, fixture.headSHA)
	plan, planRefusal := planKeptCommits(commits, []string{commits[0].SHA})
	if planRefusal != nil {
		t.Fatal(planRefusal.reason)
	}
	if plan.steps[0].aggregate || plan.steps[0].sources[0].SHA != commits[0].SHA {
		t.Fatalf("plan does not lead with the kept commit: %+v", plan)
	}
	view := PullRequestView{Number: 7, Title: "feat: the change"}
	view.Head.Ref, view.Head.SHA, view.Base.Ref = "candidate", fixture.headSHA, "main"

	_, head, refusal, err := rewriteBranchForKeptCommits(context.Background(), fixture.canonical,
		"acme/app", "candidate", advanced, plan, view, commits, "", "", []string{"sh", "-c", "exit 0"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if refusal == nil || refusal.code != LandRefusalMergeRejected {
		t.Fatalf("conflicting kept commit refusal = %+v (head %q)", refusal, head)
	}
	if !strings.Contains(refusal.reason, "does not replay cleanly onto the base") {
		t.Fatalf("conflicting kept commit reason = %q", refusal.reason)
	}
	published := strings.TrimSpace(runEngineGit(t, fixture.root,
		"--git-dir="+fixture.remote, "rev-parse", "refs/heads/candidate"))
	if published != fixture.headSHA {
		t.Fatalf("a refused rewrite moved the published branch to %q", published)
	}
}

func TestOrchCovRewriteBranchForKeptCommitsRefusesAnAggregateConflict(t *testing.T) {
	fixture := newLandFixture(t, "candidate", "a.txt", "b.txt")
	writeEngineFile(t, filepath.Join(fixture.canonical, "a.txt"), "base owns this file\n")
	runEngineGit(t, fixture.canonical, "add", "-A")
	runEngineGit(t, fixture.canonical, "commit", "-m", "base takes a.txt")
	advanced := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	commits := orchCovSourceCommits(t, fixture.canonical, fixture.baseSHA, fixture.headSHA)
	plan, planRefusal := planKeptCommits(commits, nil)
	if planRefusal != nil {
		t.Fatal(planRefusal.reason)
	}
	view := PullRequestView{Number: 7, Title: "feat: the change"}
	view.Head.Ref, view.Head.SHA, view.Base.Ref = "candidate", fixture.headSHA, "main"

	_, _, refusal, err := rewriteBranchForKeptCommits(context.Background(), fixture.canonical,
		"acme/app", "candidate", advanced, plan, view, commits, "", "", []string{"sh", "-c", "exit 0"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if refusal == nil || refusal.code != LandRefusalMergeRejected ||
		!strings.Contains(refusal.reason, "aggregated commits do not replay cleanly") {
		t.Fatalf("aggregate conflict refusal = %+v", refusal)
	}
}

func TestOrchCovRewriteBranchForKeptCommitsRefusesAKeptCommitThatDoesNotBuild(t *testing.T) {
	fixture := newLandFixture(t, "candidate", "a.txt", "b.txt")
	commits := orchCovSourceCommits(t, fixture.canonical, fixture.baseSHA, fixture.headSHA)
	plan, planRefusal := planKeptCommits(commits, []string{commits[0].SHA})
	if planRefusal != nil {
		t.Fatal(planRefusal.reason)
	}
	view := PullRequestView{Number: 7, Title: "feat: the change"}
	view.Head.Ref, view.Head.SHA, view.Base.Ref = "candidate", fixture.headSHA, "main"

	run := runnertest.New(t)
	run.ExpectArgv([]string{"sh", "-c", "exit 7"}, runner.Result{}, errors.New("exit status 7"))
	_, _, refusal, err := rewriteBranchForKeptCommits(context.Background(), fixture.canonical,
		"acme/app", "candidate", fixture.baseSHA, plan, view, commits, "", "",
		[]string{"sh", "-c", "exit 7"}, run)
	if err != nil {
		t.Fatal(err)
	}
	if refusal == nil || refusal.code != LandRefusalKeepDoesNotBuild {
		t.Fatalf("unbuildable kept commit refusal = %+v", refusal)
	}
	published := strings.TrimSpace(runEngineGit(t, fixture.root,
		"--git-dir="+fixture.remote, "rev-parse", "refs/heads/candidate"))
	if published != fixture.headSHA {
		t.Fatalf("a refused rewrite moved the published branch to %q", published)
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

func TestOrchCovLandKeepingCommitsRewritesThePublishedBranch(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "keep-task", "wb/keep/candidate", "candidate.txt", "one\n")
	writeEngineFile(t, filepath.Join(source.WorktreeDir, "second.txt"), "two\n")
	runEngineGit(t, source.WorktreeDir, "add", "second.txt")
	runEngineGit(t, source.WorktreeDir, "commit", "-m", "add second")
	runEngineGit(t, source.WorktreeDir, "push", "-u", "origin", "wb/keep/candidate")
	head := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	commits := orchCovSourceCommits(t, source.WorktreeDir, "main", "HEAD")
	if len(commits) != 2 {
		t.Fatalf("source commits = %+v", commits)
	}
	view := PullRequestView{Number: 7, Title: "feat: the change"}
	view.Head.Ref, view.Head.SHA, view.Base.Ref = "wb/keep/candidate", head, "main"
	options := PullRequestLandOptions{
		Repository: "acme/app", ProjectsRoot: fixture.githubDir,
		KeepCommits: []string{commits[0].SHA}, Reason: "the first commit stands alone",
		BuildCommand: []string{"sh", "-c", "exit 0"},
	}
	run := runnertest.New(t)
	run.ExpectArgv(options.BuildCommand, runner.Result{}, nil)
	options.run = run

	landed, rewritten, refusal, err := landKeepingCommits(context.Background(), options, view, commits, "7", "reviewer@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if refusal != nil {
		t.Fatalf("keep refusal = %+v", refusal)
	}
	if calls := run.Calls(); len(calls) != 1 || calls[0].Op != "RunOpts" || !calls[0].Opts.CaptureCombined {
		t.Fatalf("injected build runner calls = %+v", calls)
	}
	if rewritten == "" || rewritten == head || len(landed) != 2 {
		t.Fatalf("rewritten head=%q landed=%+v", rewritten, landed)
	}
	var kept int
	for _, commit := range landed {
		if commit.Kept {
			kept++
		}
	}
	if kept != 1 {
		t.Fatalf("kept commits = %+v", landed)
	}
	published := strings.TrimSpace(runEngineGit(t, source.WorktreeDir,
		"--git-dir="+fixture.repository.CloneURL, "rev-parse", "refs/heads/wb/keep/candidate"))
	if published != rewritten {
		t.Fatalf("published head = %q, want %q", published, rewritten)
	}
	// The rewrite happens in a throwaway worktree: the task's own checkout
	// must still be where the agent left it.
	if still := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD")); still != head {
		t.Fatalf("the source worktree moved to %q, want %q", still, head)
	}
}
