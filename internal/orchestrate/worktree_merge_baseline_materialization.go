package orchestrate

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
)

func configureWorktreeMergeBaselineRemote(ctx context.Context, candidateWorktree, snapshot string, timeout time.Duration, retry int) error {
	remote, _, err := runCommand(ctx, defaultRunner, timeout, retry, candidateWorktree, "git", "remote", "get-url", "origin")
	if err != nil {
		return fmt.Errorf("read candidate origin remote for target baseline: %w", err)
	}
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return errors.New("candidate origin remote for target baseline is empty")
	}
	if _, _, err := runCommand(ctx, defaultRunner, timeout, retry, snapshot, "git", "init", "--quiet"); err != nil {
		return fmt.Errorf("initialize target baseline Git context: %w", err)
	}
	if _, _, err := runCommand(ctx, defaultRunner, timeout, retry, snapshot, "git", "remote", "add", "origin", remote); err != nil {
		return fmt.Errorf("configure target baseline origin remote: %w", err)
	}
	return nil
}

func extractWorktreeMergeArchive(archivePath, destination string) error {
	return extractWorktreeMergeArchiveInjected(archivePath, destination, nil)
}

// extractWorktreeMergeArchiveInjected is extractWorktreeMergeArchive's test
// seam (task-9 PR-4): every production call site reaches it only through
// extractWorktreeMergeArchive, which always passes a nil
// *filewrite.Injector, so production behaviour is unchanged; a test passes
// its own Injector directly to reach a create/write/close failure branch
// on a tar.TypeReg entry deterministically. Each regular-file entry is
// created with a fixed name (from the archive) that is always fully
// overwritten (O_CREATE|O_TRUNC, never O_EXCL), so this uses
// CreateOrTruncatePath rather than CreateExclusivePath. The streaming copy
// from the tar reader goes through io.Copy(filewrite.Writer(file, path,
// inj), reader) (review-t9-pr6 N1): filewrite.Writer is an io.Writer
// adapter, not a []byte-at-a-time primitive, so it fits an arbitrary-size
// streaming source exactly as well as it fits a small in-memory payload,
// making io.Copy's write failures injectable without changing the chunks
// io.Copy writes or how it wraps their errors.
func extractWorktreeMergeArchiveInjected(archivePath, destination string, inj *filewrite.Injector) error {
	archive, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = archive.Close() }()
	reader := tar.NewReader(archive)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(header.Name)
		if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe archived path %q", header.Name)
		}
		path := filepath.Join(destination, name)
		switch header.Typeflag {
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
			// git archive emits PAX metadata before regular entries on some
			// platforms. The tar reader applies it to the following header.
			continue
		case tar.TypeDir:
			if err := os.MkdirAll(path, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			file, err := filewrite.CreateOrTruncatePath(path, os.FileMode(header.Mode), inj)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(filewrite.Writer(file, path, inj), reader)
			closeErr := filewrite.Close(file, path, inj)
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		case tar.TypeSymlink:
			if filepath.IsAbs(header.Linkname) {
				return fmt.Errorf("unsafe archived symlink %q -> %q", header.Name, header.Linkname)
			}
			linkTarget := filepath.Clean(filepath.Join(filepath.Dir(path), header.Linkname))
			relativeTarget, err := filepath.Rel(destination, linkTarget)
			if err != nil || relativeTarget == ".." || strings.HasPrefix(relativeTarget, ".."+string(filepath.Separator)) {
				return fmt.Errorf("unsafe archived symlink %q -> %q", header.Name, header.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(header.Linkname, path); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported archived entry %q", header.Name)
		}
	}
}
