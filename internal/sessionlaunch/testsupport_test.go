package sessionlaunch

import (
	"time"

	"github.com/sneat-dev/wb/internal/sessionauthority"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

// Helpers kept for tests only: no production caller remains.

func ValidateHarnessSelection(sourceRuntime, requested string) error {
	_, err := NormalizeRuntime(sourceRuntime, requested)
	return err
}

func launchPrompt(request sessionmove.Request) string {
	return launchPromptForAuthority(sessionauthority.Launch{
		AggregateID: request.HandoffID, SuccessorWBSessionID: request.SuccessorWBSessionID,
		PredecessorWBSessionID: request.PredecessorWBSessionID, ContinuationKind: requestContinuationKind(request),
		ContinuationPath: request.HandoverPath,
	})
}

func loadPlan(root, handoffID string) (launchPlan, sessionmove.Digest, error) {
	state, err := openLaunchState(root, handoffID, false)
	if err != nil {
		return launchPlan{}, "", err
	}
	defer func() { _ = state.Close() }()
	return state.loadPlan()
}

func loadReady(root, handoffID string, pid int) (launcherReady, sessionmove.Digest, error) {
	state, err := openLaunchState(root, handoffID, false)
	if err != nil {
		return launcherReady{}, "", err
	}
	defer func() { _ = state.Close() }()
	attempt, err := latestAttempt(state)
	if err != nil {
		return launcherReady{}, "", err
	}
	defer func() { _ = attempt.Close() }()
	return attempt.loadReady(pid)
}

func loadRelease(root, handoffID string) (launcherRelease, sessionmove.Digest, error) {
	state, err := openLaunchState(root, handoffID, false)
	if err != nil {
		return launcherRelease{}, "", err
	}
	defer func() { _ = state.Close() }()
	attempt, err := latestAttempt(state)
	if err != nil {
		return launcherRelease{}, "", err
	}
	defer func() { _ = attempt.Close() }()
	return attempt.loadRelease()
}

// Path-opening wrappers are intentionally limited to tests and isolated
// one-shot inspection. Authorization flows retain one launchState and call its
// methods directly so no transaction crosses directory identities.
func savePlan(root string, plan launchPlan) (launchPlan, sessionmove.Digest, bool, error) {
	state, err := openLaunchState(root, plan.HandoffID, true)
	if err != nil {
		return launchPlan{}, "", false, err
	}
	defer func() { _ = state.Close() }()
	return state.savePlan(plan)
}

func saveRelease(root string, plan launchPlan, planDigest sessionmove.Digest, ready launcherReady, targetWorkLogRef string, now time.Time) (launcherRelease, bool, error) {
	state, err := openLaunchState(root, plan.HandoffID, true)
	if err != nil {
		return launcherRelease{}, false, err
	}
	defer func() { _ = state.Close() }()
	attempt, err := state.openAttempt(ready.AttemptID)
	if err != nil {
		return launcherRelease{}, false, err
	}
	defer func() { _ = attempt.Close() }()
	return attempt.saveRelease(plan, planDigest, ready, targetWorkLogRef, now)
}
