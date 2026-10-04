package orchestrate

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// mergeAcknowledgementDocument keeps this writer bound to the existing audit
// schemas. Marshal errors remain meaningful: these records contain time.Time.
type mergeAcknowledgementDocument interface {
	WorktreeMergeReceiptCollisionAcknowledgement |
		WorktreeMergePreparedRebatch |
		WorktreeMergeLandedFailureAcknowledgement |
		WorktreeMergeConflictCandidateAdvance |
		WorktreeMergeLegacyValidationFailureIdentity |
		WorktreeMergeLegacyConflictIdentity |
		WorktreeMergeMissingCleanupAcknowledgement |
		WorktreeMergeValidationFailureSupersession |
		WorktreeMergeSelfSupersessionCorrection
}

// persistMergeAcknowledgement stages the complete audit bytes before atomic
// publication. Each caller retains its own replace or no-replace policy.
func persistMergeAcknowledgement[T mergeAcknowledgementDocument](path, tempPattern string, document T, publish func(string, string, *filewrite.Injector) error, inj *filewrite.Injector) error {
	contents, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := filewrite.CreateTemp(filepath.Dir(path), tempPattern, inj)
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := filewrite.ChmodFile(temporary, 0o600, temporaryPath, inj); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := filewrite.Write(temporary, contents, temporaryPath, inj); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := filewrite.Sync(temporary, temporaryPath, inj); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := filewrite.Close(temporary, temporaryPath, inj); err != nil {
		return err
	}
	return publish(temporaryPath, path, inj)
}

// A conflict advance publishes its immutable bridge before syncing the parent.
// On a later open or sync error the published bridge remains available; its
// caller retains the original recovery/error policy. Production binds os.Open.
func publishConflictAcknowledgement(temporaryPath, path string, inj *filewrite.Injector, openDirectory func(string) (*os.File, error)) error {
	if err := filewrite.LinkPath(temporaryPath, path, inj); err != nil {
		return err
	}
	directory, err := openDirectory(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return filewrite.SyncDir(directory, inj)
}
