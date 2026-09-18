package streamsync

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type stCovCreateBranchErrGit struct {
	*fakeGit
	err error
}

func (git stCovCreateBranchErrGit) CreateBranch(context.Context, string, string, string) error {
	return git.err
}

// stCovScriptedRun is one scripted verification outcome.
type stCovScriptedRun struct {
	run VerificationRun
	err error
}

type stCovScriptedVerifier struct {
	runs  []stCovScriptedRun
	calls int
}

func (verifier *stCovScriptedVerifier) Verify(context.Context, string) (VerificationRun, error) {
	index := verifier.calls
	verifier.calls++
	if index < len(verifier.runs) {
		return verifier.runs[index].run, verifier.runs[index].err
	}
	return VerificationRun{Passed: true}, nil
}

func TestVerifyBatchReportsAVerifierFailure(t *testing.T) {
	engine, _, _, _, _ := newTestEngine()
	engine.Verifier = stCovVerifierFunc(func(context.Context, string) (VerificationRun, error) {
		return VerificationRun{}, errors.New("verifier exploded")
	})
	if _, err := engine.VerifyBatch(context.Background(), baseOptions(), tenElements()); err == nil || !strings.Contains(err.Error(), "verifier exploded") {
		t.Fatalf("error = %v, want the verifier failure", err)
	}
}

// With no elements there is nothing to bisect, so a failing run is the base's
// failure rather than any element's.
func TestVerifyBatchBlamesTheBaseWhenThereIsNothingToBisect(t *testing.T) {
	engine, git, _, verifier, _ := newTestEngine()
	verifier.runs = []VerificationRun{{Passed: false, Details: []string{"backend: build failed"}}}

	result, err := engine.VerifyBatch(context.Background(), baseOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.InteractionFailure || result.Culprit != nil {
		t.Fatalf("result = %#v, want a base interaction failure with no culprit", result)
	}
	if !strings.Contains(result.FailingCheck, "backend: build failed") {
		t.Fatalf("failing check = %q", result.FailingCheck)
	}
	if len(git.createdBranches) != 0 {
		t.Fatalf("a batch with no elements created a scratch branch: %v", git.createdBranches)
	}
}

func TestVerifyBatchReportsAnUnreadableHead(t *testing.T) {
	git := newFakeGit()
	engine := &Engine{
		Git:      &stCovHeadCounterGit{fakeGit: git, revision: "stream/checkout", failAt: 0, err: errors.New("head unreadable")},
		Bumper:   newFakeBumper(),
		Verifier: &fakeVerifier{runs: []VerificationRun{{Passed: false, Details: []string{"boom"}}}},
		Events:   &fakeEvents{},
	}
	if _, err := engine.VerifyBatch(context.Background(), baseOptions(), tenElements()); err == nil || !strings.Contains(err.Error(), "head unreadable") {
		t.Fatalf("error = %v, want the head failure", err)
	}
}

func TestVerifyBatchReportsAFailedScratchBranchOrCheckout(t *testing.T) {
	ctx := context.Background()
	options := baseOptions()

	t.Run("scratch branch creation", func(t *testing.T) {
		git := newFakeGit()
		engine := &Engine{
			Git:      stCovCreateBranchErrGit{fakeGit: git, err: errors.New("branch refused")},
			Bumper:   newFakeBumper(),
			Verifier: &stCovScriptedVerifier{runs: []stCovScriptedRun{{run: VerificationRun{Passed: false, Details: []string{"boom"}}}}},
			Events:   &fakeEvents{},
		}
		if _, err := engine.VerifyBatch(ctx, options, tenElements()); err == nil || !strings.Contains(err.Error(), "branch refused") {
			t.Fatalf("error = %v, want the branch failure", err)
		}
	})

	t.Run("scratch checkout", func(t *testing.T) {
		git := newFakeGit()
		engine := &Engine{
			Git:      stCovCheckoutErrGit{fakeGit: git, err: errors.New("checkout refused")},
			Bumper:   newFakeBumper(),
			Verifier: &stCovScriptedVerifier{runs: []stCovScriptedRun{{run: VerificationRun{Passed: false, Details: []string{"boom"}}}}},
			Events:   &fakeEvents{},
		}
		if _, err := engine.VerifyBatch(ctx, options, tenElements()); err == nil || !strings.Contains(err.Error(), "checkout refused") {
			t.Fatalf("error = %v, want the checkout failure", err)
		}
	})
}

func TestVerifyBatchReportsAVerifierFailureDuringThePrefixScan(t *testing.T) {
	engine, _, _, _, _ := newTestEngine()
	engine.Verifier = &stCovScriptedVerifier{runs: []stCovScriptedRun{
		{run: VerificationRun{Passed: false, Details: []string{"boom"}}},
		{err: errors.New("second run could not start")},
	}}
	if _, err := engine.VerifyBatch(context.Background(), baseOptions(), tenElements()); err == nil || !strings.Contains(err.Error(), "second run could not start") {
		t.Fatalf("error = %v, want the mid-scan verifier failure", err)
	}
}

func TestBatchBaseSkipsEmptySHAsAndFallsBackToHead(t *testing.T) {
	engine := &Engine{Git: newFakeGit(), Bumper: newFakeBumper(), Verifier: &fakeVerifier{}, Events: &fakeEvents{}}
	ctx := context.Background()
	options := baseOptions()

	// An element with no SHA of its own still contributes its extra commits;
	// an empty entry inside them is skipped rather than resolved as a revision.
	base, err := engine.batchBase(ctx, options, []Element{
		{Name: "empty"},
		{Name: "family", extra: []string{"", "sha-real"}},
	}, "head-sha")
	if err != nil {
		t.Fatalf("batchBase: %v", err)
	}
	if base != "sha-sha-real^" {
		t.Fatalf("base = %q, want the first real element's parent", base)
	}

	// Nothing carries a commit at all, so the batch sits on the head.
	base, err = engine.batchBase(ctx, options, []Element{{Name: "no-commits"}}, "head-sha")
	if err != nil || base != "head-sha" {
		t.Fatalf("batchBase with no commits = %q, %v; want the head", base, err)
	}
}

func TestBatchBaseReportsAnUnreadableParent(t *testing.T) {
	git := newFakeGit()
	engine := &Engine{
		Git:      &stCovHeadCounterGit{fakeGit: git, revision: "sha-parent^", failAt: 0, err: errors.New("no such revision")},
		Bumper:   newFakeBumper(),
		Verifier: &fakeVerifier{},
		Events:   &fakeEvents{},
	}
	_, err := engine.batchBase(context.Background(), baseOptions(), []Element{{Name: "x", SHA: "sha-parent"}}, "head-sha")
	if err == nil || !strings.Contains(err.Error(), "resolve the parent of the first batch element sha-parent") {
		t.Fatalf("error = %v, want the unreadable parent named", err)
	}
}

func TestElementSHAsCarryTheFamilysExtraCommits(t *testing.T) {
	if got := (Element{Name: "empty"}).shas(); len(got) != 0 {
		t.Fatalf("shas of an element with no commit = %v, want none", got)
	}
	family := Element{Name: "family", extra: []string{"a", "b"}}
	if got := family.shas(); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("shas of a family without its own SHA = %v, want its extras", got)
	}
	mixed := Element{Name: "mixed", SHA: "head", extra: []string{"a"}}
	if got := mixed.shas(); len(got) != 2 || got[0] != "head" || got[1] != "a" {
		t.Fatalf("shas of a family with its own SHA = %v, want the head first", got)
	}
}

// A CI read that fails is "I could not tell", never "CI does not run it".
func TestClassifySkippedTreatsAnUnreadableCIAsUnverified(t *testing.T) {
	engine := &Engine{CI: stCovCIFunc(func(string) (map[string]bool, bool, error) {
		return nil, false, errors.New("workflows unreadable")
	})}
	skipped, unguarded, unverified := engine.classifySkipped(baseOptions(), VerificationRun{Skipped: []string{"-race"}})
	if len(skipped) != 0 || len(unguarded) != 0 {
		t.Fatalf("skipped=%v unguarded=%v; want neither claimed", skipped, unguarded)
	}
	if len(unverified) != 1 || unverified[0] != "-race" {
		t.Fatalf("unverified = %v, want the mechanism reported as undecidable", unverified)
	}
}

func TestRefusalErrorListsItsSanctionedCommands(t *testing.T) {
	_, err := JustifyPush(PushTrigger("because-i-said-so"), "")
	if err == nil {
		t.Fatal("an unrecognised push trigger was justified")
	}
	message := err.Error()
	if !strings.Contains(message, "run: ") {
		t.Fatalf("error = %q, want it to offer the sanctioned commands", message)
	}
	for _, trigger := range []string{"--reason", "wb stream propagate", "wb stream ready"} {
		if !strings.Contains(message, trigger) {
			t.Errorf("error = %q, want it to name %q", message, trigger)
		}
	}
	// A refusal with no sanctioned commands renders as its message alone.
	bare := &Refusal{Code: "x", Message: "refused"}
	if bare.Error() != "refused" {
		t.Fatalf("bare refusal = %q", bare.Error())
	}
}

func TestDefaultReasonHasNoReasonForAnUnknownTrigger(t *testing.T) {
	for trigger, want := range map[PushTrigger]string{
		TriggerLanding: "landing after a green batch verification",
		TriggerReview:  "making the draft stream pull request ready for review",
		TriggerPark:    "parking unpushed work so a hand-off cannot lose it",
		"unknown":      "",
	} {
		if got := defaultReason(trigger); got != want {
			t.Errorf("defaultReason(%q) = %q, want %q", trigger, got, want)
		}
	}
}

func TestUnpushedReportRendersBothStates(t *testing.T) {
	if got := (UnpushedReport{Repository: "acme/app"}).String(); got != "acme/app: nothing unpushed" {
		t.Fatalf("empty report = %q", got)
	}
	got := (UnpushedReport{Repository: "acme/app", Branch: "stream/x", Commits: 3}).String()
	if got != "acme/app: 3 local commit(s) not pushed on stream/x" {
		t.Fatalf("unpushed report = %q", got)
	}
}

func TestBelowRefusesAnUnparseableReleaseTriple(t *testing.T) {
	if below("1.x.0", "2.0.0") {
		t.Fatal("an unreadable required version was compared anyway")
	}
	if _, ok := semver("1.2.x"); ok {
		t.Fatal("semver accepted a non-numeric release component")
	}
	if _, ok := semver("1.2"); ok {
		t.Fatal("semver accepted a two-component version")
	}
}

func TestVerifyBatchReportsAnUnreadableBatchBase(t *testing.T) {
	git := newFakeGit()
	engine := &Engine{
		Git:      &stCovHeadCounterGit{fakeGit: git, revision: "sha-1^", failAt: 0, err: errors.New("no parent")},
		Bumper:   newFakeBumper(),
		Verifier: &stCovScriptedVerifier{runs: []stCovScriptedRun{{run: VerificationRun{Passed: false, Details: []string{"boom"}}}}},
		Events:   &fakeEvents{},
	}
	if _, err := engine.VerifyBatch(context.Background(), baseOptions(), tenElements()); err == nil || !strings.Contains(err.Error(), "resolve the parent of the first batch element") {
		t.Fatalf("error = %v, want the unreadable batch base", err)
	}
}
