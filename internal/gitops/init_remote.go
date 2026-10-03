package gitops

import "fmt"

// InitRemoteEvent records a completed mutation for a non-blocking CLI notice.
type InitRemoteEvent struct {
	Kind         InitRemoteEventKind
	Path, Branch string
}
type InitRemoteEventKind int

const (
	CreatedInitialCommit InitRemoteEventKind = iota
	Published
)

// InitRemote validates before publishing an unpublished branch.
// Notification failure cannot stop publication between mutation stages.
func InitRemote(path string, notify func(InitRemoteEvent)) error {
	return initRemoteWith(path, defaultInitRemoteOps(), notify)
}

type initRemoteOps struct {
	skipSync        func(string) (bool, error)
	originURL       func(string) (string, error)
	currentBranch   func(string) (string, error)
	hasCommits      func(string) (bool, error)
	commitEmpty     func(string, string) error
	pushSetUpstream func(string, string) error
}

func defaultInitRemoteOps() initRemoteOps {
	return initRemoteOps{
		skipSync: SkipSync, originURL: OriginURL,
		currentBranch: CurrentBranch, hasCommits: HasCommits,
		commitEmpty: CommitEmpty, pushSetUpstream: PushSetUpstream,
	}
}

// initRemoteWith validates before it mutates: a repo that fails any of the
// first three checks is left exactly as it was found, rather than carrying an
// empty commit created for a push that was never going to run.
func initRemoteWith(path string, ops initRemoteOps, notify func(InitRemoteEvent)) error {
	skip, err := ops.skipSync(path)
	if err != nil {
		return err
	}
	if skip {
		return fmt.Errorf("%s is marked %s, so wb sync would skip it anyway; run `wb repo ignore --unset %s` first",
			path, SkipSyncKey, path)
	}

	if _, err := ops.originURL(path); err != nil {
		return fmt.Errorf("%s has no origin remote to publish to: %w", path, err)
	}

	branch, err := ops.currentBranch(path)
	if err != nil {
		return err
	}
	if branch == "" {
		return fmt.Errorf("%s has a detached HEAD; check out a branch first", path)
	}

	hasCommits, err := ops.hasCommits(path)
	if err != nil {
		return err
	}
	if !hasCommits {
		if err := ops.commitEmpty(path, "Initial commit"); err != nil {
			return err
		}
		if notify != nil {
			notify(InitRemoteEvent{Kind: CreatedInitialCommit, Path: path, Branch: branch})
		}
	}

	if err := ops.pushSetUpstream(path, branch); err != nil {
		return err
	}
	if notify != nil {
		notify(InitRemoteEvent{Kind: Published, Path: path, Branch: branch})
	}
	return nil
}
