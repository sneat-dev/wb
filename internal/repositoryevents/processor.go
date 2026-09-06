package repositoryevents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/gitops"
)

type SyncProcessor struct{ ProjectsRoot string }

func (processor SyncProcessor) Process(ctx context.Context, event repositoryevent.Event) (string, error) {
	if err := event.Validate(); err != nil {
		return "", err
	}
	if event.Reason == repositoryevent.ReasonRepositoryRenamed {
		return "", errors.New("repository rename reconciliation is waiting for WB's shared guarded relocation operation")
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
	result := fleetsync.Sync(ctx, discover.Repo{Org: owner, Name: name, Path: path, CloneURL: "git@github.com:" + owner + "/" + name + ".git", Remote: true}, processor.ProjectsRoot, false, false)
	if result.Status == fleetsync.Failed {
		return "", result.Err
	}
	return result.Status.String(), nil
}

func repositoryParts(repository string) (string, string) {
	owner, name, _ := strings.Cut(strings.TrimPrefix(repository, "github.com/"), "/")
	return owner, name
}
