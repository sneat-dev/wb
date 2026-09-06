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
	"github.com/sneat-dev/wb/internal/gitremote"
	"github.com/sneat-dev/wb/internal/worktrees"
)

type SyncProcessor struct {
	ProjectsRoot   string
	relocate       func(context.Context, worktrees.RepositoryRelocateOptions) (worktrees.RepositoryRelocateResult, error)
	sync           func(context.Context, discover.Repo, string, bool, bool) fleetsync.Result
	verifyOrigin   func(string, string) error
	recoverCleanup func(context.Context, worktrees.RepositoryTransferCleanupOptions) (worktrees.RepositoryTransferCleanupResult, error)
}

func (processor SyncProcessor) Process(ctx context.Context, event repositoryevent.Event, state ProcessState) (ProcessResult, error) {
	if err := event.Validate(); err != nil {
		return ProcessResult{}, err
	}
	if (state.CleanupReceipt == "") != (state.RecoveryCommand == "") {
		return ProcessResult{}, errors.New("repository transfer cleanup state is incomplete")
	}
	out := ProcessResult{}
	if state.CleanupReceipt != "" {
		recoverCleanup := processor.recoverCleanup
		if recoverCleanup == nil {
			recoverCleanup = worktrees.RecoverRepositoryTransferCleanup
		}
		cleanup, err := recoverCleanup(ctx, worktrees.RepositoryTransferCleanupOptions{ProjectsRoot: processor.ProjectsRoot, ReceiptPath: state.CleanupReceipt, Apply: true})
		if err != nil {
			return ProcessResult{CleanupStateSet: true, CleanupReceipt: state.CleanupReceipt, RecoveryCommand: state.RecoveryCommand}, fmt.Errorf("recover repository transfer cleanup: %w; recovery: %s", err, state.RecoveryCommand)
		}
		if !cleanup.Applied {
			return ProcessResult{CleanupStateSet: true, CleanupReceipt: state.CleanupReceipt, RecoveryCommand: state.RecoveryCommand}, fmt.Errorf("recover repository transfer cleanup: %s; recovery: %s", cleanup.Reason, state.RecoveryCommand)
		}
		out.CleanupStateSet = true
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
				OnCleanupPending: state.OnCleanupPending,
			})
			if err != nil {
				return out, fmt.Errorf("relocate renamed repository: %w", err)
			}
			if !result.Applied {
				return out, fmt.Errorf("relocate renamed repository: %s", result.Reason)
			}
			if result.CleanupPending {
				if result.ReplacementCleanupReceipt == "" || result.RecoveryCommand == "" {
					return out, errors.New("relocate renamed repository returned incomplete cleanup recovery state")
				}
				pending := ProcessResult{CleanupStateSet: true, CleanupReceipt: result.ReplacementCleanupReceipt, RecoveryCommand: result.RecoveryCommand}
				return pending, fmt.Errorf("relocate renamed repository: %s; recovery: %s", result.Reason, result.RecoveryCommand)
			}
			relocated = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return out, err
		}
	}
	owner, name := repositoryParts(event.Repository)
	path := filepath.Join(processor.ProjectsRoot, owner, name)
	info, statErr := os.Stat(path)
	if statErr == nil {
		if !info.IsDir() {
			return out, errors.New("canonical repository path is not a directory")
		}
		gitInfo, err := os.Stat(filepath.Join(path, ".git"))
		if err != nil || !gitInfo.IsDir() {
			return out, errors.New("repository event target is not a canonical Git checkout")
		}
		verifyOrigin := processor.verifyOrigin
		if verifyOrigin == nil {
			verifyOrigin = verifyGitHubOrigin
		}
		if err := verifyOrigin(path, event.Repository); err != nil {
			return out, err
		}
		tracking, err := gitops.Tracking(path)
		if err != nil {
			return out, err
		}
		branch := strings.TrimPrefix(event.Ref, "refs/heads/")
		if tracking.Branch != branch {
			out.Detail = "canonical checkout is on " + tracking.Branch + "; skipped " + branch
			return out, nil
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return out, statErr
	} else {
		path = ""
	}
	syncRepository := processor.sync
	if syncRepository == nil {
		syncRepository = fleetsync.Sync
	}
	result := syncRepository(ctx, discover.Repo{Org: owner, Name: name, Path: path, CloneURL: githubSSHURL(event.Repository), Remote: true}, processor.ProjectsRoot, false, false)
	if result.Status == fleetsync.Failed {
		return out, result.Err
	}
	if relocated {
		out.Detail = "relocated; " + result.Status.String()
		return out, nil
	}
	out.Detail = result.Status.String()
	return out, nil
}

func repositoryParts(repository string) (string, string) {
	owner, name, _ := strings.Cut(strings.TrimPrefix(repository, "github.com/"), "/")
	return owner, name
}

func githubSSHURL(repository string) string {
	return "git@github.com:" + strings.TrimPrefix(repository, "github.com/") + ".git"
}

func verifyGitHubOrigin(path, repository string) error {
	origin, err := gitops.OriginURL(path)
	if err != nil {
		return err
	}
	remote, err := gitremote.Parse(origin)
	expected := strings.TrimPrefix(repository, "github.com/")
	if err != nil || remote.Identity.Host() != "github.com" || !strings.EqualFold(remote.Identity.Repository, expected) {
		return fmt.Errorf("canonical checkout origin does not identify %s", repository)
	}
	return nil
}
