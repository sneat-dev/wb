package remoterun

import (
	"context"
	"fmt"

	"github.com/sneat-dev/wb/internal/remotestate"
)

func runRemoteRelease(deps Dependencies, projectsRoot, task string, force bool) (ReleaseResult, error) {
	if err := remotestate.ValidTaskName(task); err != nil {
		return ReleaseResult{}, deps.ExitError(2, err.Error())
	}
	cfg, provider, err := loadRemote(deps, projectsRoot)
	if err != nil {
		return ReleaseResult{}, err
	}
	login, err := deps.Login()
	if err != nil || login == "" {
		return ReleaseResult{}, deps.ExitError(2, fmt.Sprintf("wb remote needs the GitHub login to key this machine's entry (gh auth status): %v", err))
	}

	outcome, err := provider.Release(context.Background(), task, login, cfg.Machine, force)
	if err != nil {
		return ReleaseResult{}, deps.ExitError(1, "release "+task+": "+err.Error())
	}

	switch outcome.Kind {
	case remotestate.Released:
		return releaseResult(outcome, fmt.Sprintf("released %s\n", task))
	case remotestate.ReleaseNoop:
		return releaseResult(outcome, fmt.Sprintf("no remote claim on %s\n", task))
	default: // remotestate.ReleaseHeldByOther
		mine := remotestate.Claim{Login: login, Machine: cfg.Machine}
		return ReleaseResult{}, deps.ExitError(1, fmt.Sprintf("remote claim on %s is held by %s, not you; --force to release it anyway", task, HolderDesc(mine, *outcome.Current)))
	}
}
func releaseResult(outcome remotestate.ReleaseOutcome, text string) (ReleaseResult, error) {
	return ReleaseResult{Outcome: outcome, Text: text}, nil
}
