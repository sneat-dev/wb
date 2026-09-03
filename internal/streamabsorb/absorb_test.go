package streamabsorb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/streamsync"
)

// fakeGit records what absorb did, so "never pushes" and "never merges" are
// asserted on the calls rather than on the absence of an error.
type fakeGit struct {
	streamsync.Git
	calls     []string
	commits   []Commit
	head      string
	conflicts []string
	rebaseErr error
	squashSHA string
	squashErr error
	buildErr  map[string]error
	clean     bool
}

func newFakeGit() *fakeGit {
	return &fakeGit{head: "agent-head-sha", squashSHA: "squashed-sha", buildErr: map[string]error{}, clean: true}
}

func (git *fakeGit) IsClean(context.Context, string) (bool, error) { return git.clean, nil }

func (git *fakeGit) Head(context.Context, string, string) (string, error) { return git.head, nil }

func (git *fakeGit) Rebase(_ context.Context, _, branch, upstream string) ([]string, error) {
	git.calls = append(git.calls, "rebase "+branch+" onto "+upstream)
	return git.conflicts, git.rebaseErr
}

func (git *fakeGit) AbortRebase(context.Context, string) error {
	git.calls = append(git.calls, "abort-rebase")
	return nil
}

func (git *fakeGit) CommitsNotIn(context.Context, string, string, string) ([]Commit, error) {
	return git.commits, nil
}

func (git *fakeGit) SquashOnto(_ context.Context, _, branch, upstream, message string) (string, error) {
	git.calls = append(git.calls, "squash "+branch+" onto "+upstream)
	git.calls = append(git.calls, "message:"+message)
	return git.squashSHA, git.squashErr
}

func (git *fakeGit) BuildCheck(_ context.Context, _, sha string) error {
	git.calls = append(git.calls, "build-check "+sha)
	return git.buildErr[sha]
}

func (git *fakeGit) pushed() bool {
	for _, call := range git.calls {
		if strings.HasPrefix(call, "push") {
			return true
		}
	}
	return false
}

func (git *fakeGit) merged() bool {
	for _, call := range git.calls {
		if strings.HasPrefix(call, "merge") {
			return true
		}
	}
	return false
}

// memoryLedger is an append-only ledger, like the real event log.
type memoryLedger struct{ records []Record }

func (ledger *memoryLedger) Record(record Record) error {
	if record.RecordedAt.IsZero() {
		record.RecordedAt = time.Now().UTC().Add(time.Duration(len(ledger.records)) * time.Second)
	}
	ledger.records = append(ledger.records, record)
	return nil
}

func (ledger *memoryLedger) Approval(_ string, fingerprint string) (Record, bool, error) {
	var newest Record
	found := false
	for _, record := range ledger.records {
		if record.Fingerprint != fingerprint {
			continue
		}
		if !found || !record.RecordedAt.Before(newest.RecordedAt) {
			newest, found = record, true
		}
	}
	return newest, found, nil
}

func newTestEngine() (*Engine, *fakeGit, *memoryLedger) {
	git, ledger := newFakeGit(), &memoryLedger{}
	return &Engine{Git: git, Ledger: ledger}, git, ledger
}

func options() Options {
	return Options{
		Stream: "checkout", AgentWorktree: "/wt/agent", AgentBranch: "agent/one",
		StreamWorktree: "/wt/app", StreamBranch: "stream/checkout", Repository: "acme/app",
		Title: "feat(checkout): accept saved cards",
	}
}

func sourceCommits() []Commit {
	return []Commit{
		{SHA: "1111111aaaa", Subject: "wip: start", PatchID: "p1", Files: []string{"backend/pay.go"}},
		{SHA: "2222222bbbb", Subject: "feat: accept saved cards", PatchID: "p2", Files: []string{"backend/pay.go"}},
		{SHA: "3333333cccc", Subject: "test: cover the new path", PatchID: "p3", Files: []string{"backend/pay_test.go"}},
	}
}

func approve(ledger *memoryLedger, commits []Commit, head string) PatchSet {
	ids := make([]string, 0, len(commits))
	for _, commit := range commits {
		ids = append(ids, commit.PatchID)
	}
	set := NewPatchSet(head, ids)
	_ = ledger.Record(Record{Verdict: VerdictApprove, Round: 1, By: "reviewer", Fingerprint: set.Fingerprint(), PatchSet: set})
	return set
}

// AC: absorb-refuses-without-an-approval-for-this-exact-patch-set.
func TestAbsorbRefusesWithoutAnApprovalForThisExactPatchSet(t *testing.T) {
	engine, git, _ := newTestEngine()
	git.commits = sourceCommits()

	_, err := engine.Absorb(context.Background(), options())
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != RefusalUnapprovedPatchSet {
		t.Fatalf("error = %v, want an %s refusal", err, RefusalUnapprovedPatchSet)
	}
	joined := strings.Join(refusal.Sanctioned, " ")
	if !strings.Contains(joined, "wb review request") || !strings.Contains(joined, "wb review record") {
		t.Errorf("refusal does not name the review verbs: %v", refusal.Sanctioned)
	}
	if len(git.calls) != 0 {
		t.Errorf("a refused absorb still acted: %v", git.calls)
	}
}

// AC: a-stream-of-five-agents-opens-one-pull-request — five absorbs produce
// five commits on the stream branch and open no pull request, push nothing and
// create no merge.
func TestFiveAgentsAbsorbLocallyAndOpenNoPullRequest(t *testing.T) {
	for agent := 1; agent <= 5; agent++ {
		engine, git, ledger := newTestEngine()
		git.commits = []Commit{{
			SHA: fmt.Sprintf("%daaaaaa", agent), Subject: fmt.Sprintf("feat: agent %d", agent),
			PatchID: fmt.Sprintf("p%d", agent), Files: []string{"backend/main.go"},
		}}
		git.head = fmt.Sprintf("head-%d", agent)
		git.squashSHA = fmt.Sprintf("squashed-%d", agent)
		approve(ledger, git.commits, git.head)

		options := options()
		options.AgentBranch = fmt.Sprintf("agent/%d", agent)
		result, err := engine.Absorb(context.Background(), options)
		if err != nil {
			t.Fatalf("agent %d: %v", agent, err)
		}
		if result.Commit != fmt.Sprintf("squashed-%d", agent) {
			t.Fatalf("agent %d produced %q, want one squashed commit", agent, result.Commit)
		}
		if result.Pushed || git.pushed() {
			t.Fatalf("agent %d pushed: %v", agent, git.calls)
		}
		if git.merged() {
			t.Fatalf("agent %d created a merge: %v", agent, git.calls)
		}
	}
}

// An approval survives a content-identical rebase — the SHAs move, the patch
// set does not — and lapses the moment the content changes.
func TestAnApprovalSurvivesARebaseButNotAContentChange(t *testing.T) {
	engine, git, ledger := newTestEngine()
	git.commits = sourceCommits()
	approve(ledger, git.commits, git.head)

	// Same content, new SHAs and a new head: a rebase.
	rebased := sourceCommits()
	for index := range rebased {
		rebased[index].SHA = "rebased" + rebased[index].SHA
	}
	git.commits = rebased
	git.head = "rebased-head"
	if _, err := engine.Absorb(context.Background(), options()); err != nil {
		t.Fatalf("a content-identical rebase must carry the approval forward: %v", err)
	}

	// Now the content changes: one patch id differs.
	changed := sourceCommits()
	changed[1].PatchID = "p2-modified"
	git.commits = changed
	_, err := engine.Absorb(context.Background(), options())
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != RefusalUnapprovedPatchSet {
		t.Fatalf("error = %v, want the approval to lapse after a content change", err)
	}
}

// Commit ORDER is not content: a reorder that changes nothing keeps the
// approval, because the fingerprint is over the sorted set.
func TestAReorderDoesNotInvalidateAnApproval(t *testing.T) {
	engine, git, ledger := newTestEngine()
	git.commits = sourceCommits()
	approve(ledger, git.commits, git.head)

	reordered := []Commit{git.commits[2], git.commits[0], git.commits[1]}
	git.commits = reordered
	if _, err := engine.Absorb(context.Background(), options()); err != nil {
		t.Fatalf("a reorder changed nothing, so the approval must stand: %v", err)
	}
}

// APPROVE-WITH-FIXES does not clear absorption: the fixes change the content,
// so absorbing the unfixed set would land what the reviewer asked to change.
func TestApproveWithFixesDoesNotClearAbsorption(t *testing.T) {
	engine, git, ledger := newTestEngine()
	git.commits = sourceCommits()
	ids := []string{"p1", "p2", "p3"}
	set := NewPatchSet(git.head, ids)
	_ = ledger.Record(Record{Verdict: VerdictApproveWithFixes, Round: 1, Fingerprint: set.Fingerprint(), PatchSet: set})

	_, err := engine.Absorb(context.Background(), options())
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != RefusalUnapprovedPatchSet {
		t.Fatalf("error = %v, want APPROVE-WITH-FIXES not to clear absorption", err)
	}
	if !strings.Contains(refusal.Message, "APPROVE-WITH-FIXES") {
		t.Errorf("refusal does not name the standing verdict: %s", refusal.Message)
	}
}

// A later REJECT supersedes an earlier APPROVE for the same content.
func TestTheNewestVerdictWins(t *testing.T) {
	engine, git, ledger := newTestEngine()
	git.commits = sourceCommits()
	set := approve(ledger, git.commits, git.head)
	_ = ledger.Record(Record{
		Verdict: VerdictReject, Round: 2, Fingerprint: set.Fingerprint(), PatchSet: set,
		RecordedAt: time.Now().UTC().Add(time.Hour),
	})

	_, err := engine.Absorb(context.Background(), options())
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != RefusalUnapprovedPatchSet {
		t.Fatalf("error = %v, want the later REJECT to win", err)
	}
}

// AC: keep-commits without --reason is refused.
func TestKeepCommitsWithoutAReasonIsRefused(t *testing.T) {
	engine, git, ledger := newTestEngine()
	git.commits = sourceCommits()
	approve(ledger, git.commits, git.head)

	options := options()
	options.KeepCommits = []string{"1111111aaaa", "2222222bbbb"}
	_, err := engine.Absorb(context.Background(), options)
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != RefusalKeepWithoutReason {
		t.Fatalf("error = %v, want a %s refusal", err, RefusalKeepWithoutReason)
	}

	options.Reason = "two independent migrations"
	result, err := engine.Absorb(context.Background(), options)
	if err != nil {
		t.Fatalf("with a reason: %v", err)
	}
	if len(result.Kept) != 2 || result.Commit != "" {
		t.Fatalf("result = %#v, want the commits kept and nothing squashed", result)
	}
}

// Every kept commit must build on its own; keeping commits is only better than
// squashing if the history stays bisectable.
func TestAKeptCommitThatDoesNotBuildIsRefused(t *testing.T) {
	engine, git, ledger := newTestEngine()
	git.commits = sourceCommits()
	approve(ledger, git.commits, git.head)
	git.buildErr["2222222bbbb"] = errors.New("undefined: helper")

	options := options()
	options.KeepCommits = []string{"1111111aaaa", "2222222bbbb"}
	options.Reason = "two independent migrations"
	_, err := engine.Absorb(context.Background(), options)
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != RefusalKeptCommitDoesNotBuild {
		t.Fatalf("error = %v, want a %s refusal", err, RefusalKeptCommitDoesNotBuild)
	}
	if !strings.Contains(strings.Join(refusal.Sanctioned, " "), "squash to one commit") {
		t.Errorf("refusal does not offer the squash: %v", refusal.Sanctioned)
	}
}

// The squash message aggregates: subject, summary, one line per source commit,
// and the reviewed patch set.
func TestTheSquashMessageAggregatesEverySourceCommit(t *testing.T) {
	engine, git, ledger := newTestEngine()
	git.commits = sourceCommits()
	approve(ledger, git.commits, git.head)

	options := options()
	options.Summary = "Saved cards now settle through the tokenised rail."
	result, err := engine.Absorb(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"feat(checkout): accept saved cards",
		"Saved cards now settle through the tokenised rail.",
		"1111111 wip: start",
		"2222222 feat: accept saved cards",
		"3333333 test: cover the new path",
		"Reviewed-patch-set:",
	} {
		if !strings.Contains(result.Message, want) {
			t.Errorf("message is missing %q:\n%s", want, result.Message)
		}
	}
}

// A mechanical bump skips the ledger, exactly as it does at landing.
func TestAMechanicalBumpSkipsTheLedger(t *testing.T) {
	engine, git, _ := newTestEngine()
	git.commits = []Commit{{
		SHA: "aaaa111", Subject: "fix(deps): bump the library", PatchID: "pb",
		Files: []string{"backend/go.mod", "backend/go.sum"},
	}}

	result, err := engine.Absorb(context.Background(), options())
	if err != nil {
		t.Fatalf("a mechanical bump must not need a review: %v", err)
	}
	if !result.Mechanical || result.Approval != nil {
		t.Fatalf("result = %#v, want it classified mechanical with no approval", result)
	}
}

// Anything beyond a manifest or lockfile is NOT mechanical: wrongly skipping a
// review is the damaging direction.
func TestACommitTouchingCodeIsNotMechanical(t *testing.T) {
	if IsMechanical([]Commit{{Files: []string{"backend/go.mod", "backend/pay.go"}}}) {
		t.Fatal("a commit touching code was classified mechanical")
	}
	if IsMechanical([]Commit{{Files: nil}}) {
		t.Fatal("a commit with no known files was classified mechanical")
	}
	if !IsMechanical([]Commit{{Files: []string{"go.mod", "pnpm-lock.yaml"}}}) {
		t.Fatal("a manifest-and-lockfile-only commit was not classified mechanical")
	}
}

// A conflict is reported and nothing is squashed.
func TestAConflictWithTheStreamBranchIsReportedNotSquashed(t *testing.T) {
	engine, git, ledger := newTestEngine()
	git.commits = sourceCommits()
	approve(ledger, git.commits, git.head)
	git.conflicts = []string{"backend/pay.go"}

	result, err := engine.Absorb(context.Background(), options())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Failed() || result.Commit != "" {
		t.Fatalf("result = %#v, want the conflict reported and nothing squashed", result)
	}
	if !strings.Contains(strings.Join(result.Errors, " "), "backend/pay.go") {
		t.Errorf("errors do not name the conflicting path: %v", result.Errors)
	}
}

func TestNothingToAbsorbIsRefused(t *testing.T) {
	engine, _, _ := newTestEngine()
	_, err := engine.Absorb(context.Background(), options())
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != RefusalNothingToAbsorb {
		t.Fatalf("error = %v, want a %s refusal", err, RefusalNothingToAbsorb)
	}
}

func TestDefaultTitleRefusesAPlaceholderSubject(t *testing.T) {
	if _, ok := DefaultTitle([]Commit{{Subject: "wip: start"}}); ok {
		t.Error("a wip subject was accepted as a title")
	}
	title, ok := DefaultTitle([]Commit{{Subject: "feat: real work"}})
	if !ok || title != "feat: real work" {
		t.Fatalf("title = %q, ok = %t", title, ok)
	}
}
