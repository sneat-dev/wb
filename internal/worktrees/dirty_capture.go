package worktrees

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

// Dirty captures are deliberately bounded. A discard command is not allowed
// to turn an untrusted checkout into an unbounded private archive or memory
// allocation. The limits are per file and for the complete changed set.
const (
	maxDirtyCaptureFileBytes  int64 = 8 << 20
	maxDirtyCaptureTotalBytes int64 = 32 << 20
)

// DirtyWorktreeEvidence is the public, non-sensitive receipt for a dirty
// capture. It contains no path or source bytes; the exact bytes live below the
// private Work Log run directory.
type DirtyWorktreeEvidence struct {
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
	Files  int    `json:"files"`
}

type dirtyCaptureEntry struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Mode   uint32 `json:"mode"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256,omitempty"`
	Blob   string `json:"blob,omitempty"`
}

type dirtyCaptureManifest struct {
	Version int                   `json:"version"`
	Receipt DirtyWorktreeEvidence `json:"receipt"`
	Entries []dirtyCaptureEntry   `json:"entries"`
}

type dirtyCaptureMaterial struct {
	Manifest dirtyCaptureManifest
	Blobs    map[string][]byte
}

func dirtyWorktreeEvidence(ctx context.Context, worktree string) (DirtyWorktreeEvidence, error) {
	material, err := collectDirtyCapture(ctx, worktree)
	if err != nil {
		return DirtyWorktreeEvidence{}, err
	}
	return material.Manifest.Receipt, nil
}

// collectDirtyCapture reads only the paths Git identifies as changed. It does
// not stage, commit, invoke hooks, or mutate the checkout. All bytes are read
// only after both per-file and total size bounds have been checked.
func collectDirtyCapture(ctx context.Context, worktree string) (dirtyCaptureMaterial, error) {
	paths, err := dirtyCapturePaths(ctx, worktree)
	if err != nil {
		return dirtyCaptureMaterial{}, err
	}
	entries := make([]dirtyCaptureEntry, 0, len(paths))
	blobs := make(map[string][]byte)
	var total int64
	for _, path := range paths {
		entry, blob, err := readDirtyCaptureEntry(worktree, path, total)
		if err != nil {
			return dirtyCaptureMaterial{}, err
		}
		total += entry.Bytes
		if blob != nil {
			blobs[entry.Blob] = blob
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return dirtyCaptureMaterial{Manifest: dirtyCaptureManifest{Version: 1, Receipt: DirtyWorktreeEvidence{SHA256: dirtyCaptureDigest(nil), Files: 0}, Entries: []dirtyCaptureEntry{}}, Blobs: blobs}, nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	receipt := DirtyWorktreeEvidence{Bytes: total, Files: len(entries)}
	receipt.SHA256 = dirtyCaptureDigest(entries)
	return dirtyCaptureMaterial{Manifest: dirtyCaptureManifest{Version: 1, Receipt: receipt, Entries: entries}, Blobs: blobs}, nil
}

func dirtyCapturePaths(ctx context.Context, worktree string) ([]string, error) {
	tracked, err := git(ctx, worktree, "diff", "--name-only", "-z", "HEAD", "--")
	if err != nil {
		return nil, fmt.Errorf("inspect tracked dirty paths: %w", err)
	}
	untracked, err := git(ctx, worktree, "ls-files", "--others", "--exclude-standard", "-z", "--")
	if err != nil {
		return nil, fmt.Errorf("inspect untracked dirty paths: %w", err)
	}
	seen := make(map[string]struct{})
	paths := make([]string, 0)
	for _, output := range []string{tracked, untracked} {
		for _, raw := range strings.Split(output, "\x00") {
			if raw == "" {
				continue
			}
			path, err := dirtyCapturePath(raw)
			if err != nil {
				return nil, err
			}
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func dirtyCapturePath(path string) (string, error) {
	path = filepath.ToSlash(path)
	if path == "" || filepath.IsAbs(path) || path == "." || strings.HasPrefix(path, "../") || path == ".." || strings.Contains(path, "\x00") {
		return "", fmt.Errorf("refusing unsafe dirty path %q", path)
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if clean != path || clean == "." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("refusing non-canonical dirty path %q", path)
	}
	return path, nil
}

func readDirtyCaptureEntry(worktree, path string, total int64) (dirtyCaptureEntry, []byte, error) {
	full := filepath.Join(worktree, filepath.FromSlash(path))
	info, err := os.Lstat(full)
	if errors.Is(err, os.ErrNotExist) {
		return dirtyCaptureEntry{Path: path, Kind: "deleted"}, nil, nil
	}
	if err != nil {
		return dirtyCaptureEntry{}, nil, fmt.Errorf("inspect dirty path %s: %w", path, err)
	}
	entry := dirtyCaptureEntry{Path: path, Mode: uint32(info.Mode().Perm())}
	switch {
	case info.Mode().IsRegular():
		if info.Size() < 0 || info.Size() > maxDirtyCaptureFileBytes || total > maxDirtyCaptureTotalBytes-info.Size() {
			return dirtyCaptureEntry{}, nil, fmt.Errorf("refusing dirty capture for %s: size exceeds bounded %d-byte retention", path, maxDirtyCaptureTotalBytes)
		}
		content, err := readDirtyFileNoFollow(full, info.Size())
		if err != nil {
			return dirtyCaptureEntry{}, nil, fmt.Errorf("read dirty path %s: %w", path, err)
		}
		entry.Kind, entry.Bytes = "file", int64(len(content))
		sum := sha256.Sum256(content)
		entry.SHA256 = hex.EncodeToString(sum[:])
		entry.Blob = dirtyCaptureBlobName(len(content), entry.SHA256)
		return entry, content, nil
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(full)
		if err != nil {
			return dirtyCaptureEntry{}, nil, fmt.Errorf("read dirty symlink %s: %w", path, err)
		}
		content := []byte(target)
		if int64(len(content)) > maxDirtyCaptureFileBytes || total > maxDirtyCaptureTotalBytes-int64(len(content)) {
			return dirtyCaptureEntry{}, nil, fmt.Errorf("refusing dirty capture for %s: size exceeds bounded %d-byte retention", path, maxDirtyCaptureTotalBytes)
		}
		entry.Kind, entry.Bytes = "symlink", int64(len(content))
		sum := sha256.Sum256(content)
		entry.SHA256 = hex.EncodeToString(sum[:])
		entry.Blob = dirtyCaptureBlobName(len(content), entry.SHA256)
		return entry, content, nil
	default:
		return dirtyCaptureEntry{}, nil, fmt.Errorf("refusing unsupported dirty path type %s", path)
	}
}

func readDirtyFileNoFollow(path string, size int64) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), filepath.Base(path))
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("wrap dirty file")
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(io.LimitReader(file, maxDirtyCaptureFileBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) != size || int64(len(content)) > maxDirtyCaptureFileBytes {
		return nil, fmt.Errorf("dirty file changed while being captured")
	}
	return content, nil
}

func dirtyCaptureDigest(entries []dirtyCaptureEntry) string {
	hash := sha256.New()
	for _, entry := range entries {
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%d\x00%d\x00%s\x00", entry.Path, entry.Kind, entry.Mode, entry.Bytes, entry.SHA256)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func dirtyCaptureBlobName(size int, digest string) string {
	return fmt.Sprintf("%d-%s.bin", size, digest)
}

// materializeDirtyCapture publishes the already-read bytes through the same
// no-replace private-file primitive as other Work Log evidence. The manifest
// is written last, so a manifest is a durable receipt that every blob exists.
func materializeDirtyCapture(runDir *os.File, claimID string, material dirtyCaptureMaterial) (DirtyWorktreeEvidence, error) {
	if runDir == nil || !validClaimID(claimID) {
		return DirtyWorktreeEvidence{}, fmt.Errorf("dirty capture Work Log destination is invalid")
	}
	root, err := openPrivateChild(runDir, "dirty-discard", true)
	if err != nil {
		return DirtyWorktreeEvidence{}, err
	}
	defer func() { _ = root.Close() }()
	directory, err := openPrivateChild(root, claimID, true)
	if err != nil {
		return DirtyWorktreeEvidence{}, err
	}
	defer func() { _ = directory.Close() }()
	for name, content := range material.Blobs {
		if err := writeBytesImmutableAt(directory, name, content, 0o600, true); err != nil {
			return DirtyWorktreeEvidence{}, fmt.Errorf("write dirty capture blob: %w", err)
		}
	}
	encoded, err := json.MarshalIndent(material.Manifest, "", "  ")
	if err != nil {
		return DirtyWorktreeEvidence{}, err
	}
	encoded = append(encoded, '\n')
	if err := writeBytesImmutableAt(directory, "manifest.json", encoded, 0o600, true); err != nil {
		return DirtyWorktreeEvidence{}, fmt.Errorf("write dirty capture manifest: %w", err)
	}
	return material.Manifest.Receipt, nil
}

func captureAndPersistDirtyWorktree(ctx context.Context, home, worktree string, expected *DirtyWorktreeEvidence) (*DirtyWorktreeEvidence, error) {
	material, err := collectDirtyCapture(ctx, worktree)
	if err != nil {
		return nil, err
	}
	if expected != nil && !dirtyCaptureMatches(*expected, material.Manifest.Receipt) {
		return nil, dirtyCaptureChangedError(*expected, material.Manifest.Receipt)
	}
	projection, err := readWorkLogProjectionForClaim(home, worktree)
	if errors.Is(err, errWorkLogProjectionNotFound) {
		return nil, fmt.Errorf("cannot discard dirty worktree %s without a private Work Log claim", worktree)
	}
	if err != nil {
		return nil, err
	}
	runDir, _, err := openWorkLogRun(home, projection.EffortID, projection.RunID, false)
	if err != nil {
		return nil, fmt.Errorf("open private Work Log run for dirty capture: %w", err)
	}
	defer func() { _ = runDir.Close() }()
	receipt, err := materializeDirtyCapture(runDir, projection.ClaimID, material)
	if err != nil {
		return nil, err
	}
	return &receipt, nil
}

func dirtyCaptureMatches(expected, actual DirtyWorktreeEvidence) bool {
	return expected.SHA256 != "" && expected == actual
}

func dirtyCaptureChangedError(expected, actual DirtyWorktreeEvidence) error {
	return fmt.Errorf("dirty worktree bytes changed after evidence capture: expected sha256=%s bytes=%d files=%d, observed sha256=%s bytes=%d files=%d", expected.SHA256, expected.Bytes, expected.Files, actual.SHA256, actual.Bytes, actual.Files)
}
