package streamsync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func tenElements() []Element {
	elements := make([]Element, 0, 10)
	for index := 1; index <= 10; index++ {
		elements = append(elements, Element{
			Name: fmt.Sprintf("lib-%d", index), SHA: fmt.Sprintf("sha-%d", index),
		})
	}
	return elements
}

// AC: ten-bumps-verify-once-then-prefix-re-apply — ten bumps where the seventh
// breaks the build: one full run, then cumulative prefixes stopping at the
// first failure, naming the seventh, listing one to six as proven good, and
// costing 1+7 runs rather than ten.
func TestABatchVerifiesOnceThenFindsTheCulpritByPrefixReApply(t *testing.T) {
	engine, git, _, verifier, events := newTestEngine()
	options := baseOptions()
	// Run 1 is the whole batch and fails. Then prefixes 1..1 … 1..7; the
	// seventh prefix is the first that fails.
	verifier.runs = []VerificationRun{{Passed: false, Details: []string{"backend: compilation failed"}}}
	for prefix := 1; prefix <= 10; prefix++ {
		verifier.runs = append(verifier.runs, VerificationRun{
			Passed: prefix < 7, Details: []string{"backend: compilation failed"},
		})
	}

	result, err := engine.VerifyBatch(context.Background(), options, tenElements())
	if err != nil {
		t.Fatalf("verify batch: %v", err)
	}
	if result.Passed {
		t.Fatal("a failing batch reported success")
	}
	if result.Culprit == nil || result.Culprit.Name != "lib-7" {
		t.Fatalf("culprit = %#v, want lib-7", result.Culprit)
	}
	if len(result.ProvenGood) != 6 {
		t.Fatalf("proven good = %d elements, want one to six", len(result.ProvenGood))
	}
	// The honest cost: 1 + k, not 1 + log N and not N.
	if result.Runs != 8 {
		t.Fatalf("runs = %d, want 1 + 7", result.Runs)
	}
	if result.FailingCheck == "" {
		t.Error("the failing check was not named")
	}
	// Prefix re-apply ran on a scratch branch that was never pushed.
	if result.ScratchBranch == "" || len(git.createdBranches) != 1 {
		t.Fatalf("scratch branch = %q, created = %v", result.ScratchBranch, git.createdBranches)
	}
	if git.pushed() {
		t.Fatalf("prefix re-apply pushed: %v", git.calls)
	}
	// The tree is never left in the failed batch state, and the scratch
	// branch is cleaned up.
	if len(git.deletedBranches) != 1 || git.deletedBranches[0] != result.ScratchBranch {
		t.Errorf("scratch branch was not deleted: %v", git.deletedBranches)
	}
	last := git.checkedOut[len(git.checkedOut)-1]
	if last != options.Branch {
		t.Errorf("the worktree was left on %q, want %q", last, options.Branch)
	}
	if len(events.withPhase("batch")) == 0 {
		t.Error("the batch outcome was not recorded")
	}
}

// With every element passing the total is exactly one full run.
func TestAPassingBatchCostsExactlyOneRun(t *testing.T) {
	engine, git, _, verifier, _ := newTestEngine()
	verifier.runs = []VerificationRun{{Passed: true}}

	result, err := engine.VerifyBatch(context.Background(), baseOptions(), tenElements())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Passed || result.Runs != 1 {
		t.Fatalf("result = %#v, want one passing run", result)
	}
	if len(git.createdBranches) != 0 {
		t.Errorf("a passing batch created a scratch branch: %v", git.createdBranches)
	}
	if verifier.calls != 1 {
		t.Fatalf("verifier ran %d times", verifier.calls)
	}
}

// If every prefix passes, the failure came from the base or a rebased change
// rather than any element — reported as an interaction failure, not blamed on
// the last element.
func TestEveryPrefixPassingIsReportedAsAnInteractionFailure(t *testing.T) {
	engine, _, _, verifier, _ := newTestEngine()
	verifier.runs = []VerificationRun{{Passed: false, Details: []string{"flaky integration test"}}}
	for prefix := 1; prefix <= 10; prefix++ {
		verifier.runs = append(verifier.runs, VerificationRun{Passed: true})
	}

	result, err := engine.VerifyBatch(context.Background(), baseOptions(), tenElements())
	if err != nil {
		t.Fatal(err)
	}
	if !result.InteractionFailure {
		t.Fatalf("result = %#v, want an interaction failure", result)
	}
	if result.Culprit != nil {
		t.Fatalf("culprit = %#v; no element may be blamed when every prefix passed", result.Culprit)
	}
}

// A lockstep family is ONE element and is never split during prefix re-apply:
// a prefix carrying half of Angular cannot build by construction and would
// blame the wrong element.
func TestALockstepFamilyIsOneElementAndIsNeverSplit(t *testing.T) {
	engine, git, _, verifier, _ := newTestEngine()
	elements := []Element{
		{Name: "unrelated", SHA: "sha-a"},
		{Name: "@angular/core", SHA: "sha-b", Family: "angular"},
		{Name: "@angular/router", SHA: "sha-c", Family: "angular"},
		{Name: "@angular/forms", SHA: "sha-d", Family: "angular"},
	}
	verifier.runs = []VerificationRun{{Passed: false, Details: []string{"boom"}}, {Passed: true}, {Passed: false, Details: []string{"boom"}}}

	result, err := engine.VerifyBatch(context.Background(), baseOptions(), elements)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Elements) != 2 {
		t.Fatalf("elements = %#v, want the family collapsed into one", result.Elements)
	}
	if result.Culprit == nil || !strings.Contains(result.Culprit.Name, "angular") {
		t.Fatalf("culprit = %#v, want the angular family", result.Culprit)
	}
	// All three family commits were applied together before the verify.
	applied := 0
	for _, call := range git.calls {
		if strings.HasPrefix(call, "cherry-pick sha-b") || strings.HasPrefix(call, "cherry-pick sha-c") || strings.HasPrefix(call, "cherry-pick sha-d") {
			applied++
		}
	}
	if applied != 3 {
		t.Fatalf("family commits applied = %d, want all three as one unit", applied)
	}
}

// A mechanism may only be named as skipped after CI is proved to run it;
// anything neither side carries is reported as unguarded.
func TestSkippedMechanismsAreOnlyClaimedWhenCIProvablyRunsThem(t *testing.T) {
	engine, _, _, verifier, _ := newTestEngine()
	engine.CI = fakeCI{present: map[string]bool{"-race": true}}
	verifier.runs = []VerificationRun{{Passed: true, Skipped: []string{"-race", "playwright-e2e"}}}

	result, err := engine.VerifyBatch(context.Background(), baseOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skipped) != 1 || !strings.Contains(result.Skipped[0], "-race") {
		t.Fatalf("skipped = %v, want only the mechanism CI provably runs", result.Skipped)
	}
	if len(result.Unguarded) != 1 || result.Unguarded[0] != "playwright-e2e" {
		t.Fatalf("unguarded = %v, want the mechanism neither side runs", result.Unguarded)
	}
}

// With no way to read CI, nothing may be claimed as covered: an unverified
// "CI owns it" is worse than no gate.
func TestWithoutCIEvidenceNothingIsClaimedAsCovered(t *testing.T) {
	engine, _, _, verifier, _ := newTestEngine()
	engine.CI = nil
	verifier.runs = []VerificationRun{{Passed: true, Skipped: []string{"-race"}}}

	result, err := engine.VerifyBatch(context.Background(), baseOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skipped) != 0 {
		t.Fatalf("skipped = %v; nothing may be claimed without evidence", result.Skipped)
	}
	if len(result.Unguarded) != 1 {
		t.Fatalf("unguarded = %v, want the mechanism reported as unguarded", result.Unguarded)
	}
}

// A cherry-pick that cannot be re-applied names the element it failed on
// rather than reporting a spurious pass.
func TestAFailedReApplyNamesItsElement(t *testing.T) {
	engine, git, _, verifier, _ := newTestEngine()
	verifier.runs = []VerificationRun{{Passed: false, Details: []string{"boom"}}, {Passed: true}}
	git.cherryErr["sha-2"] = errors.New("conflict in backend/handler.go")

	result, err := engine.VerifyBatch(context.Background(), baseOptions(), tenElements())
	if err != nil {
		t.Fatal(err)
	}
	if result.Culprit == nil || result.Culprit.Name != "lib-2" {
		t.Fatalf("culprit = %#v, want lib-2", result.Culprit)
	}
	if !strings.Contains(result.FailingCheck, "conflict in backend/handler.go") {
		t.Errorf("failing check does not carry the re-apply error: %q", result.FailingCheck)
	}
}
