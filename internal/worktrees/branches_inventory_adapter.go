package worktrees

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/worktreebranches"
	"github.com/sneat-dev/wb/internal/worktreelanding"
)

func branchInventoryRepository(repository discover.Repo) worktreebranches.Repository {
	return worktreebranches.Repository{Slug: repository.Slug(), Path: repository.Path}
}

func (sweep branchSweepOptions) branchInventorySweep() worktreebranches.InventorySweep {
	return worktreebranches.InventorySweep{
		ProjectsRoot: sweep.ProjectsRoot, Base: sweep.Base, Scope: sweep.Scope,
		Only: sweep.Only, OlderThan: sweep.OlderThan, Filter: sweep.Filter,
		Repository: sweep.Repository, Org: sweep.Org, Branch: sweep.Branch, Name: sweep.Name,
		Progress: sweep.Progress, Now: sweep.Now, Receipts: sweep.Receipts, WithPRs: sweep.WithPRs,
		Cleanup: sweep.Cleanup, AbsorbedBy: sweep.AbsorbedBy, SupersededBy: sweep.SupersededBy,
		IncludeRetired: sweep.IncludeRetired,
	}
}

func branchInventoryService() worktreebranches.InventoryService {
	return worktreebranches.InventoryService{Ports: worktreebranches.InventoryPorts{
		Discover: func(projectsRoot string) ([]worktreebranches.Repository, error) {
			repositories, err := discover.ScanLocal(projectsRoot)
			if err != nil {
				return nil, err
			}
			selected := make([]worktreebranches.Repository, 0, len(repositories))
			for _, repository := range repositories {
				selected = append(selected, branchInventoryRepository(repository))
			}
			return selected, nil
		},
		ListInUse: func(ctx context.Context, root, filter string) ([]worktreebranches.BranchUse, error) {
			outcome, err := ListWithDiagnostics(ctx, ListOptions{ProjectsRoot: root, Filter: filter})
			if err != nil {
				return nil, err
			}
			uses := make([]worktreebranches.BranchUse, 0, len(outcome.Results))
			for _, result := range outcome.Results {
				uses = append(uses, worktreebranches.BranchUse{Repository: result.Repository, Branch: result.Branch, Task: result.Task})
			}
			return uses, nil
		},
		Git:              git,
		FetchTarget:      fetchRemoteTargetHead,
		IsAncestor:       isAncestor,
		ContentContained: contentContained,
		CommitTree:       commitTree,
		Supersession: func(ctx context.Context, path string, repository worktreebranches.Repository, ref worktreebranches.BranchRef, base, targetSHA string) (*worktreebranches.SupersessionEvidence, string) {
			receipt, rejection := supersessionReceiptForEntry(ctx, path, ListResult{Repository: repository.Slug, Branch: ref.Name, HeadSHA: ref.SHA,
				Base: base, RemoteTargetSHA: targetSHA, CanonicalDir: repository.Path})
			if rejection != "" {
				return nil, rejection
			}
			return &worktreebranches.SupersessionEvidence{Reviewer: receipt.Approval.Actor, ReceiptID: receipt.Approval.ReceiptID}, ""
		},
		SupersessionDigest: supersessionFileSHA256,
		AttestedReceipt: func(ctx context.Context, repository worktreebranches.Repository, ref worktreebranches.BranchRef, base, targetSHA, absorbedBy string) (*worktreelanding.VerifiedCandidate, string, error) {
			receipt, rejection, err := attestedAbsorbedReceipt(ctx, repository.Path, repository.Path, repository.Slug, ref.SHA, base, targetSHA, absorbedBy)
			if receipt == nil || err != nil {
				return nil, rejection, err
			}
			return &worktreelanding.VerifiedCandidate{LandingSHA: receipt.LandingSHA, PullRequest: receipt.PullRequest}, rejection, nil
		},
		PullRequestsForHead: func(ctx context.Context, repository worktreebranches.Repository, sha string) ([]worktreebranches.GitHubPullRequest, error) {
			return githubPullRequests(ctx, repository.Path, repository.Slug, sha)
		},
		AbsorbingPR: absorbingPullRequest,
		PullRequestAPI: func(ctx context.Context, path, endpoint string) ([]byte, error) {
			response := githubobserver.Execute(ctx, path, "api", "--paginate", endpoint)
			if response.Err != nil {
				return nil, fmt.Errorf("%w: %s", response.Err, strings.TrimSpace(string(response.Stderr)+string(response.Stdout)))
			}
			return response.Stdout, nil
		},
		HostName:  os.Hostname,
		TempDir:   func() (string, error) { return os.MkdirTemp("", "wb-retired-tag-metadata-") },
		RemoveDir: os.RemoveAll,
	}}
}
