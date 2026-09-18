//go:build darwin || linux

package worktrees

import (
	"crypto/rand"
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
			if !directoryStillMatches(rootPath, root) {
				artifact.Eligible = false
				artifact.Reason = "canonical-local worktrees root changed before apply"
				return
			}
			fd, openErr := unix.Openat(int(root.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			if errors.Is(openErr, unix.ENOENT) {
				artifact.Applied = true
				artifact.Disposition = dispositionRetiredEmptyUnscopedLocalStage
				artifact.Reason = "empty retired canonical-local stage was already absent at apply"
				return
			}
			if openErr != nil {
				artifact.Eligible = false
				artifact.Reason = "open retired canonical-local stage without following links: " + openErr.Error()
				return
			}
			directory := os.NewFile(uintptr(fd), "wb-gc-retired-stage")
			if directory == nil {
				_ = unix.Close(fd)
				artifact.Eligible = false
				artifact.Reason = "wrap retired canonical-local stage descriptor"
				return
			}
			defer func() { _ = directory.Close() }()
			if !directoryStillMatches(artifact.Path, directory) {
				artifact.Eligible = false
				artifact.Reason = "retired canonical-local stage changed before apply"
				return
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
			retiredName, moved, moveErr := moveEmptyRetiredStageForGC(root, name, directory, afterAuthorization)
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
			if moved == nil {
				artifact.Eligible = false
				artifact.Reason = "isolate exact retired canonical-local stage: moved descriptor is unavailable"
				return
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

func moveEmptyRetiredStageForGC(root *os.File, name string, expected *os.File, afterAuthorization func()) (string, *os.File, error) {
	for attempt := 0; attempt < 16; attempt++ {
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			return "", nil, fmt.Errorf("generate isolated retired-stage name: %w", err)
		}
		retiredName := fmt.Sprintf(".wb-retired-stage-%x", token[:])
		moved, err := moveExpectedDirectoryNoReplace(root, name, root, retiredName, expected, afterAuthorization)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		return retiredName, moved, err
	}
	return "", nil, fmt.Errorf("isolate retired canonical-local stage without a name collision")
}
