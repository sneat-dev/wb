package worktrees

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/worktreeclaims"
)

// completedAdoptedHistory grants no live placement authority. It recognizes
// only an absent checkout with native removed-terminal and completed cleanup
// evidence, so retired external registration is not required for history.
func completedAdoptedHistory(home, projectsRoot string, claim workLogClaim) (bool, error) {
	return completedAdoptedHistoryWithHistoryPorts(home, projectsRoot, claim, terminalHistoryPorts())
}

func completedAdoptedHistoryWithHistoryPorts(home, projectsRoot string, claim workLogClaim, history worktreeclaims.HistoryPorts) (bool, error) {
	adopted, err := claimLineageWasAdopted(home, claim)
	if err != nil || !adopted {
		return false, err
	}
	relocated, err := resolveRelocationChain(home, claim)
	if err != nil {
		return false, err
	}
	if _, err = os.Lstat(relocated.worktree); err == nil {
		return false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	run, _, err := history.OpenRun(home, claim.EffortID, claim.RunID, false)
	if err != nil {
		return false, err
	}
	terminal, err := readWorkLogTerminalAt(run, claim.ClaimID)
	_ = run.Close()
	if err != nil {
		return false, err
	}
	expectation := TerminalWorkLogExpectation{Task: claim.Task, Repository: claim.Repository, Worktree: claim.Worktree, Branch: claim.Branch, Base: claim.Base, FinalCommit: terminal.FinalCommit}
	if _, err = history.ReadRemovedTerminalWorkLogClaimBase(home, expectation); err != nil {
		return false, err
	}
	canonical, err := CanonicalRepositoryPath(projectsRoot, relocated.repository)
	if err != nil {
		return false, err
	}
	entry := ListResult{Task: claim.Task, Repository: relocated.repository, CanonicalDir: canonical, WorktreeDir: relocated.worktree, Branch: claim.Branch, HeadSHA: terminal.FinalCommit}
	directory, err := openLifecycleBacklogDirectory(home, false)
	if err != nil {
		return false, err
	}
	defer func() { _ = directory.Close() }()
	id := lifecycleBacklogID(entry, "removed")
	var backlog lifecycleBacklogRecord
	if err = readJSONAt(directory, id+".json", &backlog); err != nil {
		return false, err
	}
	if err = validateLifecycleBacklog(backlog); err != nil {
		return false, err
	}
	if backlog.ID != id || backlog.Stage != lifecycleStageComplete || !backlog.External || backlog.Base != claim.Base || filepath.Clean(backlog.ProjectsRoot) != filepath.Clean(projectsRoot) {
		return false, fmt.Errorf("adopted terminal history has no exact completed cleanup")
	}
	return true, nil
}
