package worktrees

import (
	"os"

	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktreeclaims"
)

// ClaimPullRequestBinding is the durable sidecar fact for an active claim.
type ClaimPullRequestBinding = worktreeclaims.ClaimPullRequestBinding

const pullRequestBindingSuffix = worktreeclaims.PullRequestBindingSuffix

// RegisteredPullRequestBinding identifies a binding and its active claim.
type RegisteredPullRequestBinding = worktreeclaims.RegisteredPullRequestBinding

func RecordClaimPullRequestBinding(projectsRoot, worktree string, binding ClaimPullRequestBinding) (task, claimID string, err error) {
	return claimBindingPorts().RecordClaimPullRequestBinding(projectsRoot, worktree, binding)
}

func ListRegisteredPullRequestBindings(projectsRoot string) ([]RegisteredPullRequestBinding, error) {
	return claimBindingPorts().ListRegisteredPullRequestBindings(projectsRoot)
}

func claimBindingPorts() worktreeclaims.BindingPorts {
	return worktreeclaims.BindingPorts{
		Root: wbhome.Root,
		Active: func(home, worktree string) (worktreeclaims.ActiveClaim, error) {
			claim, _, path, err := activeWorkLogClaim(home, worktree)
			return worktreeclaims.ActiveClaim{Task: claim.Task, ClaimID: claim.ClaimID, Path: path}, err
		},
		Homes: func(projectsRoot string) ([]string, error) {
			resolution, err := wbhome.Resolve(projectsRoot)
			if err != nil {
				return nil, err
			}
			homes := make([]string, 0, len(resolution.Read))
			for _, layout := range resolution.Read {
				homes = append(homes, layout.Home)
			}
			return homes, nil
		},
		Walk: func(home string, visit func(*os.File, string, string)) error {
			return walkActiveWorkLogClaims(home, func(claims *os.File, claimID string, claim workLogClaim) {
				visit(claims, claimID, claim.Task)
			})
		},
		ReadJSONAt: readJSONAt,
	}
}
