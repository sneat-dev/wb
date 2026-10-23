package worktrees

import "github.com/sneat-dev/wb/internal/worktreebranches"

func (sweep branchSweepOptions) branchPolicyOptions() worktreebranches.PolicyOptions {
	return worktreebranches.PolicyOptions{
		Base: sweep.Base, Scope: sweep.Scope, Only: sweep.Only,
		Branch: sweep.Branch, Name: sweep.Name,
		OlderThan: sweep.OlderThan, Now: sweep.Now,
	}
}
