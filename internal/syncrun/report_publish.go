package syncrun

import (
	"context"

	"github.com/sneat-dev/wb/internal/remotestate/gitrepo"
	"github.com/sneat-dev/wb/internal/syncreport"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func PublishSyncReport(ctx context.Context, repository, root string, report syncreport.Report) (gitrepo.SyncReportPublishResult, error) {
	clonePath, err := syncReportClonePath(root, repository)
	if err != nil {
		return gitrepo.SyncReportPublishResult{}, err
	}
	provider := gitrepo.New(gitrepo.Options{ClonePath: clonePath, CloneURL: syncReportCloneURL(repository)})
	return provider.PublishSyncReport(ctx, report, syncreport.ValidatePath)
}
func syncReportCloneURL(repository string) string {
	return "git@github.com:" + repository + ".git"
}
func syncReportClonePath(root, repository string) (string, error) {
	return worktrees.CanonicalRepositoryPathForURL(root, repository, syncReportCloneURL(repository))
}
