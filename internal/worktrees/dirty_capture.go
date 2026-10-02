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

	"github.com/sneat-dev/wb/internal/unixcompat"
	"github.com/sneat-dev/wb/internal/worktreeproof"
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
type DirtyWorktreeEvidence = worktreeproof.DirtyWorktreeEvidence
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
	return collectDirtyCaptureWithBoundary(ctx, worktree, nil)
}

// dirtyCaptureBoundary observes real filesystem boundaries for deterministic
// replacement and error tests. A production capture passes nil.
type dirtyCaptureBoundary func(stage string, root *os.Root, file *os.File)

func observeDirtyCaptureBoundary(boundary dirtyCaptureBoundary, stage string, root *os.Root, file *os.File) {
	if boundary != nil {
		boundary(stage, root, file)
	}
}

func collectDirtyCaptureWithBoundary(ctx context.Context, worktree string, boundary dirtyCaptureBoundary) (dirtyCaptureMaterial, error) {
	sourceInfo, err := os.Lstat(worktree)
	if err != nil {
		return dirtyCaptureMaterial{}, fmt.Errorf("inspect dirty worktree root: %w", err)
	}
	// The worktree root itself must be the directory opened below. Relative
	// dirty paths may still resolve through safe links inside that root.
	if !sourceInfo.IsDir() {
		return dirtyCaptureMaterial{}, fmt.Errorf("refusing non-directory dirty worktree root %s", worktree)
	}
	observeDirtyCaptureBoundary(boundary, "root-lstat", nil, nil)
	root, err := os.OpenRoot(worktree)
	if err != nil {
		return dirtyCaptureMaterial{}, fmt.Errorf("open dirty worktree root: %w", err)
	}
	defer func() { _ = root.Close() }()
	observeDirtyCaptureBoundary(boundary, "root-open", root, nil)
	heldInfo, err := root.Stat(".")
	if err != nil {
		return dirtyCaptureMaterial{}, fmt.Errorf("inspect held dirty worktree root: %w", err)
	}
	if !os.SameFile(sourceInfo, heldInfo) {
		return dirtyCaptureMaterial{}, fmt.Errorf("dirty worktree root changed before path inspection")
	}
	paths, err := dirtyCapturePaths(ctx, worktree)
	if err != nil {
		return dirtyCaptureMaterial{}, err
	}
	observeDirtyCaptureBoundary(boundary, "paths", root, nil)
	if err := confirmDirtyCaptureRoot(worktree, sourceInfo); err != nil {
		return dirtyCaptureMaterial{}, err
	}
	entries := make([]dirtyCaptureEntry, 0, len(paths))
	blobs := make(map[string][]byte)
	var total int64
	for _, path := range paths {
		entry, blob, err := readDirtyCaptureEntry(root, path, total, boundary)
		if err != nil {
			return dirtyCaptureMaterial{}, err
		}
		total += entry.Bytes
		if blob != nil {
			blobs[entry.Blob] = blob
		}
		entries = append(entries, entry)
		observeDirtyCaptureBoundary(boundary, "entry", root, nil)
	}
	if err := confirmDirtyCaptureRoot(worktree, sourceInfo); err != nil {
		return dirtyCaptureMaterial{}, err
	}
	if len(entries) == 0 {
		return dirtyCaptureMaterial{Manifest: dirtyCaptureManifest{Version: 1, Receipt: DirtyWorktreeEvidence{SHA256: dirtyCaptureDigest(nil), Files: 0}, Entries: []dirtyCaptureEntry{}}, Blobs: blobs}, nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	receipt := DirtyWorktreeEvidence{Bytes: total, Files: len(entries)}
	receipt.SHA256 = dirtyCaptureDigest(entries)
	return dirtyCaptureMaterial{Manifest: dirtyCaptureManifest{Version: 1, Receipt: receipt, Entries: entries}, Blobs: blobs}, nil
}

func confirmDirtyCaptureRoot(worktree string, expected os.FileInfo) error {
	current, err := os.Lstat(worktree)
	if err != nil {
		return fmt.Errorf("recheck dirty worktree root: %w", err)
	}
	if !current.IsDir() || !os.SameFile(expected, current) {
		return fmt.Errorf("dirty worktree root changed during capture")
	}
	return nil
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
	return parseDirtyCapturePaths(tracked, untracked)
}

// parseDirtyCapturePaths validates and deduplicates NUL-delimited Git path records.
func parseDirtyCapturePaths(tracked, untracked string) ([]string, error) {
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

func readDirtyCaptureEntry(root *os.Root, path string, total int64, boundary dirtyCaptureBoundary) (dirtyCaptureEntry, []byte, error) {
	info, err := root.Lstat(filepath.FromSlash(path))
	if errors.Is(err, os.ErrNotExist) {
		return dirtyCaptureEntry{Path: path, Kind: "deleted"}, nil, nil
	}
	if err != nil {
		return dirtyCaptureEntry{}, nil, fmt.Errorf("inspect dirty path %s: %w", path, err)
	}
	observeDirtyCaptureBoundary(boundary, "leaf-lstat", root, nil)
	entry := dirtyCaptureEntry{Path: path, Mode: uint32(info.Mode().Perm())}
	switch {
	case info.Mode().IsRegular():
		if info.Size() < 0 || info.Size() > maxDirtyCaptureFileBytes || total > maxDirtyCaptureTotalBytes-info.Size() {
			return dirtyCaptureEntry{}, nil, fmt.Errorf("refusing dirty capture for %s: size exceeds bounded %d-byte retention", path, maxDirtyCaptureTotalBytes)
		}
		content, err := readDirtyCaptureRegular(root, filepath.FromSlash(path), info, boundary)
		if err != nil {
			return dirtyCaptureEntry{}, nil, fmt.Errorf("read dirty path %s: %w", path, err)
		}
		entry.Kind, entry.Bytes = "file", int64(len(content))
		sum := sha256.Sum256(content)
		entry.SHA256 = hex.EncodeToString(sum[:])
		entry.Blob = dirtyCaptureBlobName(len(content), entry.SHA256)
		return entry, content, nil
	case info.Mode()&os.ModeSymlink != 0:
		target, err := root.Readlink(filepath.FromSlash(path))
		if err != nil {
			return dirtyCaptureEntry{}, nil, fmt.Errorf("read dirty symlink %s: %w", path, err)
		}
		observeDirtyCaptureBoundary(boundary, "readlink", root, nil)
		current, err := root.Lstat(filepath.FromSlash(path))
		if err != nil || current.Mode()&os.ModeSymlink == 0 || !os.SameFile(info, current) {
			return dirtyCaptureEntry{}, nil, fmt.Errorf("dirty symlink changed while being captured: %s: %v", path, err)
		}
		observeDirtyCaptureBoundary(boundary, "symlink-stat-after", root, nil)
		// Device/inode identity can be reused after an unlink. Check the actual
		// link bytes too, so a replacement cannot validate an earlier target.
		verifiedTarget, err := root.Readlink(filepath.FromSlash(path))
		if err != nil || verifiedTarget != target {
			return dirtyCaptureEntry{}, nil, fmt.Errorf("dirty symlink changed while being captured: %s: %v", path, err)
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

func readDirtyCaptureRegular(root *os.Root, path string, initial os.FileInfo, boundary dirtyCaptureBoundary) ([]byte, error) {
	file, err := root.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	observeDirtyCaptureBoundary(boundary, "file-open", root, file)
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(initial, opened) || opened.Size() != initial.Size() {
		return nil, fmt.Errorf("dirty file changed before being captured")
	}
	observeDirtyCaptureBoundary(boundary, "file-stat-before", root, file)
	content, err := io.ReadAll(io.LimitReader(file, maxDirtyCaptureFileBytes+1))
	if err != nil {
		return nil, err
	}
	observeDirtyCaptureBoundary(boundary, "file-read", root, file)
	current, err := file.Stat()
	if err != nil {
		return nil, err
	}
	observeDirtyCaptureBoundary(boundary, "file-stat-after", root, file)
	pathInfo, err := root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !current.Mode().IsRegular() || !os.SameFile(initial, current) || !os.SameFile(initial, pathInfo) || !pathInfo.Mode().IsRegular() || current.Mode().Perm() != initial.Mode().Perm() || pathInfo.Mode().Perm() != initial.Mode().Perm() || current.Size() != initial.Size() || pathInfo.Size() != initial.Size() || !current.ModTime().Equal(initial.ModTime()) || !pathInfo.ModTime().Equal(initial.ModTime()) || int64(len(content)) != initial.Size() || int64(len(content)) > maxDirtyCaptureFileBytes {
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
	// This private manifest contains only strings, integers, and slices of
	// those fixed structs, with no custom marshalers or fallible JSON values.
	encoded, _ := json.MarshalIndent(material.Manifest, "", "  ")
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
