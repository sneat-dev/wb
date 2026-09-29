//go:build darwin || linux

package worktrees

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// retireEmptyUnscopedLocalStages applies the exact root-level artifact plan
// emitted by listCanonicalLocalLayout. These directories have no task
// namespace or lock to route through Cleanup, so gc owns their one safe
// terminal transition: descriptor-verified removal of an unchanged, empty,
// collision-resistant retired stage. Active, non-empty, symlinked, renamed,
// or otherwise reclassified paths remain untouched.
func retireEmptyUnscopedLocalStages(artifacts []LifecycleArtifact) {
	retireEmptyUnscopedLocalStagesWithHooks(artifacts, nil, nil)
}

func retireEmptyUnscopedLocalStagesAfterAuthorization(artifacts []LifecycleArtifact, afterAuthorization func()) {
	retireEmptyUnscopedLocalStagesWithHooks(artifacts, afterAuthorization, nil)
}

func retireEmptyUnscopedLocalStagesWithHooks(artifacts []LifecycleArtifact, afterAuthorization func(), beforeRemoval func(string)) {
	retireEmptyUnscopedLocalStagesWithBoundaryHooks(artifacts, afterAuthorization, beforeRemoval, gcRetiredStageHooks{})
}

type gcRetiredStageHooks struct {
	afterRootOpen   func(*os.File)
	afterStageOpen  func(*os.File)
	afterStageMatch func(*os.File)
	afterMove       func(string)
	afterIsolation  func(string, *os.File)
	beforeLinkStat  func(*os.File)
}

func retireEmptyUnscopedLocalStagesWithBoundaryHooks(artifacts []LifecycleArtifact, afterAuthorization func(), beforeRemoval func(string), hooks gcRetiredStageHooks) {
	for index := range artifacts {
		artifact := &artifacts[index]
		if artifact.Kind != lifecycleArtifactKindStage || artifact.State != "quarantined" ||
			artifact.Disposition != dispositionEmptyUnscopedLocalRetiredStage || !artifact.Eligible || artifact.Applied {
			continue
		}
		rootPath := filepath.Clean(artifact.WorktreesRoot)
		name := filepath.Base(artifact.Path)
		if filepath.Dir(filepath.Clean(artifact.Path)) != rootPath || !isRetiredWorktreeStagingDirectory(name) {
			artifact.Eligible = false
			artifact.Reason = "retired stage lost its exact canonical-local identity before apply"
			continue
		}
		root, err := openAbsoluteDirectoryNoFollow(rootPath, false)
		if err != nil {
			artifact.Eligible = false
			artifact.Reason = "open canonical-local worktrees root without following links: " + err.Error()
			continue
		}
		func() {
			defer func() { _ = root.Close() }()
			if hooks.afterRootOpen != nil {
				hooks.afterRootOpen(root)
			}
			if !directoryStillMatches(rootPath, root) {
				artifact.Eligible = false
				artifact.Reason = "canonical-local worktrees root changed before apply"
				return
			}
			directory, openErr := openDirectoryAtNoFollow(int(root.Fd()), name, "wb-gc-retired-stage",
				"open retired canonical-local stage without following links", "")
			if errors.Is(openErr, unix.ENOENT) {
				artifact.Applied = true
				artifact.Disposition = dispositionRetiredEmptyUnscopedLocalStage
				artifact.Reason = "empty retired canonical-local stage was already absent at apply"
				return
			}
			if openErr != nil {
				artifact.Eligible = false
				artifact.Reason = openErr.Error()
				return
			}
			defer func() { _ = directory.Close() }()
			if hooks.afterStageOpen != nil {
				hooks.afterStageOpen(directory)
			}
			if !directoryStillMatches(artifact.Path, directory) {
				artifact.Eligible = false
				artifact.Reason = "retired canonical-local stage changed before apply"
				return
			}
			if hooks.afterStageMatch != nil {
				hooks.afterStageMatch(directory)
			}
			empty, emptyErr := directoryEmpty(directory)
			if emptyErr != nil || !empty {
				artifact.Eligible = false
				if emptyErr != nil {
					artifact.Reason = "reinspect retired canonical-local stage: " + emptyErr.Error()
				} else {
					artifact.Reason = "retired canonical-local stage became non-empty; preserved for audited recovery"
				}
				return
			}
			retiredName, moved, moveErr := moveEmptyRetiredStageForGC(root, name, directory, afterAuthorization, hooks.afterMove)
			if moved != nil {
				defer func() { _ = moved.Close() }()
			}
			if moveErr != nil {
				artifact.Eligible = false
				artifact.Reason = "isolate exact retired canonical-local stage before removal: " + moveErr.Error()
				if moved != nil && directoryEntryStillMatches(root, retiredName, directory) {
					artifact.ArchivePath = filepath.Join(rootPath, retiredName)
				}
				return
			}
			if hooks.afterIsolation != nil {
				hooks.afterIsolation(retiredName, moved)
			}
			empty, emptyErr = directoryEmpty(moved)
			if emptyErr != nil || !empty || !directoryEntryStillMatches(root, retiredName, moved) {
				artifact.Eligible = false
				artifact.ArchivePath = filepath.Join(rootPath, retiredName)
				if emptyErr != nil {
					artifact.Reason = "reinspect isolated retired canonical-local stage: " + emptyErr.Error()
				} else if !empty {
					artifact.Reason = "isolated retired canonical-local stage became non-empty; preserved for audited recovery"
				} else {
					artifact.Reason = "isolated retired canonical-local stage changed before removal"
				}
				return
			}
			if hooks.beforeLinkStat != nil {
				hooks.beforeLinkStat(moved)
			}
			var linked unix.Stat_t
			if statErr := unix.Fstat(int(moved.Fd()), &linked); statErr != nil {
				artifact.Eligible = false
				artifact.ArchivePath = filepath.Join(rootPath, retiredName)
				artifact.Reason = "inspect isolated retired canonical-local stage link count: " + statErr.Error()
				return
			}
			if beforeRemoval != nil {
				beforeRemoval(retiredName)
			}
			if unlinkErr := unix.Unlinkat(int(root.Fd()), retiredName, unix.AT_REMOVEDIR); unlinkErr != nil {
				artifact.Eligible = false
				artifact.ArchivePath = filepath.Join(rootPath, retiredName)
				artifact.Reason = "remove isolated empty retired canonical-local stage: " + unlinkErr.Error()
				return
			}
			if !directoryDescriptorWasRemovedAt(moved, filepath.Join(rootPath, retiredName), uint64(linked.Nlink)) {
				artifact.Eligible = false
				artifact.Reason = "isolated retired canonical-local stage moved before final removal; exact stage was preserved"
				return
			}
			artifact.Applied = true
			artifact.Disposition = dispositionRetiredEmptyUnscopedLocalStage
			artifact.Reason = "descriptor-verified empty retired canonical-local stage removed by gc --apply"
		}()
	}
}

func moveEmptyRetiredStageForGC(root *os.File, name string, expected *os.File, afterAuthorization func(), afterMove ...func(string)) (string, *os.File, error) {
	return moveEmptyRetiredStageForGCWith(root, name, expected, afterAuthorization, afterMove, randomHexToken, moveExpectedDirectoryNoReplace)
}

func moveEmptyRetiredStageForGCWith(root *os.File, name string, expected *os.File, afterAuthorization func(), afterMove []func(string), token func(int) string,
	move func(*os.File, string, *os.File, string, *os.File, func(), ...func()) (*os.File, error),
) (string, *os.File, error) {
	for attempt := 0; attempt < 16; attempt++ {
		retiredName := ".wb-retired-stage-" + token(16)
		var movedHook func()
		if len(afterMove) > 0 && afterMove[0] != nil {
			movedHook = func() { afterMove[0](retiredName) }
		}
		moved, err := move(root, name, root, retiredName, expected, afterAuthorization, movedHook)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		return retiredName, moved, err
	}
	return "", nil, fmt.Errorf("isolate retired canonical-local stage without a name collision")
}
