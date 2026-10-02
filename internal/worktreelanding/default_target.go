package worktreelanding

import "context"

// The states a recorded base can be in when the repository default branch is
// judged in its place.
const (
	// RecordedBaseAbsent: origin no longer has the recorded base branch, the
	// shape an integration branch leaves once its pull request merges and
	// GitHub deletes it.
	RecordedBaseAbsent = "absent"
	// RecordedBaseIntegrated: origin still has the recorded base and its tip is
	// itself contained in the default branch, the shape of a task stacked on
	// another task's branch after the stack landed.
	RecordedBaseIntegrated = "integrated"
)

// DefaultTargetPorts are the three observations the default-branch proof
// needs. Every one is local and exact: the default branch origin advertises,
// the freshly fetched head of a branch, and Git ancestry between two commits.
type DefaultTargetPorts struct {
	DefaultBranch   func(context.Context) (string, error)
	FetchTargetHead func(ctx context.Context, branch string) (string, error)
	IsAncestor      func(ctx context.Context, ancestor, descendant string) (bool, error)
}

// DefaultTarget is the repository default branch standing in for a recorded
// base that can no longer answer for itself. Contained is the only fact that
// authorizes anything: the exact head is a Git ancestor of the exact fetched
// default-branch head.
type DefaultTarget struct {
	Target            string
	TargetSHA         string
	RecordedBase      string
	RecordedBaseState string
	Contained         bool
}

// Proof names the target that proved integration, for a report an operator
// reads before anything is deleted.
func (target DefaultTarget) Proof() string {
	return "contained in origin/" + target.Target + " at " + shortSHA(target.TargetSHA) +
		", via recorded base " + target.RecordedBase + " (" + target.baseState() + ")"
}

func (target DefaultTarget) baseState() string {
	if target.RecordedBaseState == RecordedBaseIntegrated {
		return "integrated into origin/" + target.Target
	}
	return target.RecordedBaseState
}

// ResolveDefaultTarget decides whether the repository default branch may be
// judged in place of recordedBase, and whether it contains head.
//
// recordedBaseSHA is the freshly fetched head of the recorded base, or empty
// when origin no longer has that branch. A base that is still live is replaced
// only when its own tip is contained in the default branch: a live feature
// branch that has not landed is still the task's target, and work that reached
// the default branch some other way does not change that.
//
// It returns nil, without error, when the default branch cannot stand in: it
// is the recorded base itself, or the live recorded base has not landed there.
// It never widens what counts as landed: Contained is plain Git ancestry
// against the exact fetched default-branch head.
func ResolveDefaultTarget(ctx context.Context, ports DefaultTargetPorts, head, recordedBase, recordedBaseSHA string) (*DefaultTarget, error) {
	branch, err := ports.DefaultBranch(ctx)
	if err != nil {
		return nil, err
	}
	if branch == recordedBase {
		return nil, nil
	}
	targetSHA, err := ports.FetchTargetHead(ctx, branch)
	if err != nil {
		return nil, err
	}
	target := &DefaultTarget{Target: branch, TargetSHA: targetSHA, RecordedBase: recordedBase, RecordedBaseState: RecordedBaseAbsent}
	if recordedBaseSHA != "" {
		baseLanded, err := ports.IsAncestor(ctx, recordedBaseSHA, targetSHA)
		if err != nil || !baseLanded {
			return nil, err
		}
		target.RecordedBaseState = RecordedBaseIntegrated
	}
	target.Contained, err = ports.IsAncestor(ctx, head, targetSHA)
	if err != nil {
		return nil, err
	}
	return target, nil
}

// NotIntegratedReason is the refusal for a head the judged target does not
// contain. It names the exact ref and SHA the head was compared against and
// what is known about the source branch on origin, because "awaiting push" on
// its own is wrong for a branch that is fully pushed and merely unmerged.
func NotIntegratedReason(head, base, targetSHA, branch, remoteHead string) string {
	reason := "current branch head " + shortSHA(head) + " is not integrated into the exact origin target"
	if base != "" && targetSHA != "" {
		reason += " origin/" + base + " at " + shortSHA(targetSHA)
	}
	if remoteHead != "" && remoteHead == head {
		return reason + " (pushed to origin/" + branch + ", awaiting merge)"
	}
	return reason + " (awaiting push)"
}
