package repositoryevents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/worktrees"
)

type SyncProcessor struct {
	ProjectsRoot string
	relocate     func(context.Context, worktrees.RepositoryRelocateOptions) (worktrees.RepositoryRelocateResult, error)
	sync         func(context.Context, discover.Repo, string, bool, bool) fleetsync.Result
}

func (processor SyncProcessor) Process(ctx context.Context, event repositoryevent.Event) (string, error) {
	if err := event.Validate(); err != nil {
		return "", err
	}
	relocated := false
	if event.Reason == repositoryevent.ReasonRepositoryRenamed {
		oldOwner, oldName := repositoryParts(event.PreviousRepository)
		oldPath := filepath.Join(processor.ProjectsRoot, oldOwner, oldName)
		if _, err := os.Stat(oldPath); err == nil {
			relocate := processor.relocate
			if relocate == nil {
				relocate = worktrees.RelocateRepository
			}
			branch := strings.TrimPrefix(event.Ref, "refs/heads/")
			result, err := relocate(ctx, worktrees.RepositoryRelocateOptions{
				ProjectsRoot: processor.ProjectsRoot, SourceRepository: strings.TrimPrefix(event.PreviousRepository, "github.com/"),
				DestinationRepository: strings.TrimPrefix(event.Repository, "github.com/"),
				RemoteURL:             githubSSHURL(event.Repository), DefaultBranch: branch, Apply: true,
			})
			if err != nil {
				return "", fmt.Errorf("relocate renamed repository: %w", err)
			}
			if !result.Applied {
				return "", fmt.Errorf("relocate renamed repository: %s", result.Reason)
			}
			relocated = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	owner, name := repositoryParts(event.Repository)
	path := filepath.Join(processor.ProjectsRoot, owner, name)
	info, statErr := os.Stat(path)
	if statErr == nil {
		if !info.IsDir() {
			return "", errors.New("canonical repository path is not a directory")
		}
		gitInfo, err := os.Stat(filepath.Join(path, ".git"))
		if err != nil || !gitInfo.IsDir() {
			return "", errors.New("repository event target is not a canonical Git checkout")
		}
		tracking, err := gitops.Tracking(path)
		if err != nil {
			return "", err
		}
		branch := strings.TrimPrefix(event.Ref, "refs/heads/")
		if tracking.Branch != branch {
			return "canonical checkout is on " + tracking.Branch + "; skipped " + branch, nil
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	} else {
		path = ""
	}
	syncRepository := processor.sync
	if syncRepository == nil {
		syncRepository = fleetsync.Sync
	}
	result := syncRepository(ctx, discover.Repo{Org: owner, Name: name, Path: path, CloneURL: githubSSHURL(event.Repository), Remote: true}, processor.ProjectsRoot, false, false)
	if result.Status == fleetsync.Failed {
		return "", result.Err
	}
	if relocated {
		return "relocated; " + result.Status.String(), nil
	}
	return result.Status.String(), nil
}

func repositoryParts(repository string) (string, string) {
	owner, name, _ := strings.Cut(strings.TrimPrefix(repository, "github.com/"), "/")
	return owner, name
}

func githubSSHURL(repository string) string {
	return "git@github.com:" + strings.TrimPrefix(repository, "github.com/") + ".git"
}
