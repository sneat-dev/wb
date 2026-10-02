package worktrees

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/gitremote"
	"github.com/sneat-dev/wb/internal/repopath"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/worktreejournal"
	"github.com/sneat-dev/wb/internal/worktreelayout"
)

// Helpers kept for tests only: no production caller remains.

// ExpectedRemoteURL derives the remote URL a canonical clone path under
// projectsRoot corresponds to: <root>/github.com/dal-go/dalgo becomes
// https://github.com/dal-go/dalgo.
//
// It is pure path arithmetic. No WB configuration and no repository remote is
// read, so the answer exists before a clone does and cannot be changed by a
// rewritten origin. A path whose first level is not a literal forge hostname
// has no such remote and is refused rather than guessed.
func ExpectedRemoteURL(projectsRoot, canonicalPath string) (string, error) {
	root, err := absoluteProjectsRoot(projectsRoot)
	if err != nil {
		return "", err
	}
	return repopath.RemoteURLForLocalPath(root, canonicalPath)
}

func NewestChangedFileTime(ctx context.Context, worktree string) time.Time {
	return heartbeatPorts().NewestChangedFileTime(ctx, worktree)
}

// ParkedSessionWorkLogReference returns the exact active Work Log claim only
// when it is owned by source. Session parking uses this at its immutable
// snapshot boundary; it must never adopt another session's latest owner.
func ParkedSessionWorkLogReference(projectsRoot, worktree string, source session.Record) (string, error) {
	_, reference, err := inspectSessionMoveWorkLog(projectsRoot, worktree, source)
	return reference, err
}

func QuarantineManifestDigest(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:]), nil
}

func grantOwnerWriteAt(directory *os.File, path string) error {
	return grantOwnerWriteWithIO(directory, path, nativeResidueRemovalIO())
}

func mergeResultTree(ctx context.Context, repository, ours, theirs string) (string, bool, error) {
	return landingReceiptService().MergeResultTree(ctx, repository, ours, theirs)
}

func openJournalComponent(parentFD int, name string, create bool) (int, error) {
	return worktreejournal.OpenJournalComponent(parentFD, name, create)
}

// resolveCanonicalClone returns the canonical clone address for one repository
// coordinate.
//
// A host-qualified coordinate names its path directly. An unqualified
// {owner}/{name} coordinate resolves to an existing clone, preferring the
// literal host level and falling back to the legacy two-level placement. When
// neither exists the legacy path is predicted, because an unqualified
// coordinate carries no host: a host is knowable only from an existing clone or
// from a clone URL, and inventing one would place a repository on a forge
// nobody named.
func resolveCanonicalClone(projectsRoot string, address repopath.Address) (repopath.Address, error) {
	return worktreelayout.ResolveCanonicalClone(projectsRoot, address)
}

func sessionReceiveRepositoryFromRemote(remote string) (string, error) {
	parsed, err := gitremote.Parse(remote)
	if err != nil {
		return "", err
	}
	return parsed.Identity.Repository, nil
}

func writeJSONAtomic(path string, value any, mode os.FileMode) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return filewrite.WriteBytesAtomic(filepath.Dir(path), filepath.Base(path), append(content, '\n'), mode)
}
