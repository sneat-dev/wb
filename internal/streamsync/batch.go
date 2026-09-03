package streamsync

import (
	"context"
	"fmt"
	"strings"
)

// Element is one batch element: one dependency-bump commit, one lockstep
// family applied together, or one absorbed agent commit.
//
// The rebase onto a moved base is deliberately NOT an element — it cannot be
// reverted independently, so it is the batch's base rather than part of it.
type Element struct {
	Name        string `json:"name"`
	SHA         string `json:"sha"`
	Description string `json:"description,omitempty"`
	// Family groups a lockstep-versioned set (Angular, Nx, Ionic, Capacitor,
	// @sneat/*) that must be applied together and never split during prefix
	// re-apply.
	Family string `json:"family,omitempty"`
	// extra carries the additional commits of a lockstep family, which is
	// applied as one unit and never split during prefix re-apply.
	extra []string
}

// BatchResult is the outcome of verifying a batch.
type BatchResult struct {
	Passed bool `json:"passed"`
	// Runs is how many full verification runs it cost. One in the passing
	// case; 1+k when the culprit is element k.
	Runs int `json:"runs"`
	// Elements is the batch as applied, after lockstep families are grouped.
	Elements []Element `json:"elements"`
	// Culprit is the last element of the first failing prefix.
	Culprit *Element `json:"culprit,omitempty"`
	// ProvenGood are the elements every failing prefix had already cleared.
	ProvenGood []Element `json:"proven_good,omitempty"`
	// InteractionFailure is set when every prefix passed: the failure came
	// from the base or a rebased change, not from any element.
	InteractionFailure bool `json:"interaction_failure,omitempty"`
	// FailingCheck names what actually failed.
	FailingCheck string `json:"failing_check,omitempty"`
	// Skipped names CI mechanisms this run did not execute, printed only
	// where CI is proved to carry them.
	Skipped []string `json:"skipped,omitempty"`
	// Unguarded names mechanisms neither this run nor CI carries.
	Unguarded []string `json:"unguarded,omitempty"`
	// ScratchBranch is the local branch prefix re-apply ran on. It is never
	// pushed, which is what keeps the whole search to zero CI runs.
	ScratchBranch string `json:"scratch_branch,omitempty"`
}

// VerifyBatch applies the whole batch, runs the suite once, and on failure
// finds the culprit by cumulative prefix re-apply.
//
// The cost is honest: one full run when the batch passes, and `1 + k` runs when
// the culprit is element k — worst case `1 + N`. It is a linear prefix scan,
// not a bisection.
//
// Prefix re-apply runs on a local scratch branch that is NEVER pushed. Running
// it on the stream branch would push k intermediate states, each firing a
// stream-PR CI run — exactly the cost this design exists to remove.
//
// Implements: dependency-streams#req:batch-verifies-once,
// dependency-streams#req:batch-failure-is-found-by-prefix-re-apply,
// dependency-streams#req:a-lockstep-family-is-one-batch-element.
func (engine *Engine) VerifyBatch(ctx context.Context, options Options, elements []Element) (BatchResult, error) {
	grouped := groupLockstepFamilies(elements)
	result := BatchResult{Elements: grouped}

	first, err := engine.Verifier.Verify(ctx, options.Worktree)
	if err != nil {
		return result, err
	}
	result.Runs = 1
	skipped, unguarded := engine.classifySkipped(options, first)
	result.Skipped, result.Unguarded = skipped, unguarded
	if first.Passed {
		result.Passed = true
		engine.record(options, "batch", "success",
			fmt.Sprintf("%d element(s) verified in one run", len(grouped)), nil)
		return result, nil
	}
	result.FailingCheck = strings.Join(first.Details, "; ")
	if len(grouped) == 0 {
		// Nothing to bisect: the failure is the base itself.
		result.InteractionFailure = true
		return result, nil
	}

	head, err := engine.Git.Head(ctx, options.Worktree, options.Branch)
	if err != nil {
		return result, err
	}
	scratch := "wb/batch-" + options.Stream
	result.ScratchBranch = scratch
	base := result.baseRevision(head, grouped)
	if err := engine.Git.CreateBranch(ctx, options.Worktree, scratch, base); err != nil {
		return result, err
	}
	defer func() {
		// The tree is never left in the failed batch state, in any outcome.
		_ = engine.Git.Checkout(ctx, options.Worktree, options.Branch)
		_ = engine.Git.ResetHard(ctx, options.Worktree, head)
		_ = engine.Git.DeleteBranch(ctx, options.Worktree, scratch)
	}()
	if err := engine.Git.Checkout(ctx, options.Worktree, scratch); err != nil {
		return result, err
	}

	for index, element := range grouped {
		// Cumulative: element 1, then 1+2, then 1+2+3. Isolated re-application
		// would miss an interaction failure, where each element passes alone
		// and a pair does not.
		for _, sha := range element.shas() {
			if err := engine.Git.CherryPick(ctx, options.Worktree, sha); err != nil {
				result.FailingCheck = fmt.Sprintf("re-applying %s failed: %v", element.Name, err)
				culprit := grouped[index]
				result.Culprit = &culprit
				result.ProvenGood = grouped[:index]
				return result, nil
			}
		}
		run, err := engine.Verifier.Verify(ctx, options.Worktree)
		if err != nil {
			return result, err
		}
		result.Runs++
		if run.Passed {
			continue
		}
		culprit := grouped[index]
		result.Culprit = &culprit
		result.ProvenGood = grouped[:index]
		result.FailingCheck = strings.Join(run.Details, "; ")
		engine.record(options, "batch", "findings",
			fmt.Sprintf("prefix 1..%d failed; %s is the culprit", index+1, culprit.Name),
			map[string]string{"culprit": culprit.Name, "runs": fmt.Sprint(result.Runs)})
		return result, nil
	}

	// Every prefix passed, so the failure came from the base or from a rebased
	// change rather than from any element.
	result.InteractionFailure = true
	engine.record(options, "batch", "findings",
		"every prefix passed: the failure is in the base or a rebased change, not in any element",
		map[string]string{"elements": fmt.Sprint(len(grouped)), "base": base})
	return result, nil
}

// baseRevision is the commit the batch was applied on top of: head minus every
// element commit.
func (result BatchResult) baseRevision(head string, elements []Element) string {
	count := 0
	for _, element := range elements {
		count += len(element.shas())
	}
	if count == 0 {
		return head
	}
	return fmt.Sprintf("%s~%d", head, count)
}

// shas returns the commits one element carries. A lockstep family carries
// several and is applied as one unit.
func (element Element) shas() []string {
	if element.SHA == "" {
		return element.extra
	}
	return append([]string{element.SHA}, element.extra...)
}

// groupLockstepFamilies collapses a lockstep-versioned family into ONE element.
//
// Angular, Nx, Ionic, Capacitor and @sneat/* move together; splitting them
// during prefix re-apply would produce a prefix that cannot build by
// construction and would blame the wrong element.
func groupLockstepFamilies(elements []Element) []Element {
	grouped := make([]Element, 0, len(elements))
	byFamily := map[string]int{}
	for _, element := range elements {
		if element.Family == "" {
			grouped = append(grouped, element)
			continue
		}
		if index, seen := byFamily[element.Family]; seen {
			grouped[index].extra = append(grouped[index].extra, element.SHA)
			grouped[index].Name = grouped[index].Family + " (lockstep family)"
			continue
		}
		byFamily[element.Family] = len(grouped)
		grouped = append(grouped, element)
	}
	return grouped
}

// classifySkipped decides what may be printed as "CI owns it".
//
// A mechanism is only ever named as skipped after the member's stream-PR
// workflows are read and proved to carry it. Anything neither this run nor CI
// carries is reported as UNGUARDED — an unverified assurance is worse than no
// gate.
func (engine *Engine) classifySkipped(options Options, run VerificationRun) (skipped, unguarded []string) {
	if len(run.Skipped) == 0 {
		return nil, nil
	}
	if engine.CI == nil {
		return nil, run.Skipped
	}
	present, err := engine.CI.Present(options.Worktree)
	if err != nil {
		return nil, run.Skipped
	}
	for _, mechanism := range run.Skipped {
		if present[mechanism] {
			skipped = append(skipped, mechanism+" (CI on the stream pull request runs it)")
			continue
		}
		unguarded = append(unguarded, mechanism)
	}
	return skipped, unguarded
}
