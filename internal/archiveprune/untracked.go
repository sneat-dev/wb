package archiveprune

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
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/wbhome"
	"golang.org/x/sys/unix"
)

const untrackedMaxDepth = 128

var errUntrackedPlanDrift = errors.New("untracked plan drift")

// cleanAuthorizedUntracked applies the one narrow exception to archive
// cleanup's normal no-untracked-files rule. It is intentionally a separate
// path from os.RemoveAll: every name is relative to a descriptor opened on
// the clone root; links are refused rather than followed; and the plan is
// reread after the durable intent receipt exists but before any deletion.
func cleanAuthorizedUntracked(ctx context.Context, projectsRoot string, repo discover.Repo, planned Result, options Options) Result {
	receipt := archiveCleanReceipt{
		SchemaVersion: 1,
		Phase:         "planned",
		Repository:    planned.Repository,
		ClonePath:     planned.Path,
		CreatedAt:     time.Now().UTC(),
		Untracked:     planned.Untracked,
	}
	receiptPath, err := writeArchiveCleanReceipt(projectsRoot, receipt)
	if err != nil {
		planned.Error = fmt.Sprintf("record untracked deletion receipt: %v", err)
		return planned
	}
	planned.ReceiptPath = receiptPath
	if options.beforeUntrackedRevalidation != nil {
		options.beforeUntrackedRevalidation()
	}

	if err := deleteExactUntracked(ctx, repo.Path, planned.Untracked); err != nil {
		if errors.Is(err, errUntrackedPlanDrift) {
			planned.Reason = err.Error()
			return planned
		}
		planned.Error = fmt.Sprintf("delete explicitly authorized untracked paths: %v", err)
		return planned
	}

	receipt.Phase = "untracked_deleted"
	if err := overwriteArchiveCleanReceipt(receiptPath, receipt); err != nil {
		planned.Error = fmt.Sprintf("finalize untracked deletion receipt: %v", err)
		return planned
	}

	current := Evaluate(ctx, projectsRoot, repo)
	current.ReceiptPath = receiptPath
	current.Untracked = planned.Untracked
	if !current.Eligible {
		current.Reason = fmt.Sprintf("explicitly deleted itemized untracked paths; clone is no longer eligible: %s", current.Reason)
		return current
	}
	if err := os.RemoveAll(repo.Path); err != nil {
		current.Error = err.Error()
		return current
	}
	current.Applied = true
	current.Reason = fmt.Sprintf("archived and clean; explicitly deleted %s under --delete-untracked", plural(len(planned.Untracked), "untracked path"))
	return current
}

// planUntracked expands the Git porcelain roots into an exact manifest. It
// refuses any link, absolute path, parent escape, special file, or directory
// component replaced while it is being descended. The manifest carries inode,
// mode and regular-file content hash privately so revalidation can reject a
// changed same-size file too; reports expose only the useful path/kind/size.
func planUntracked(root string, reported []string) ([]UntrackedEntry, error) {
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open clone root: %w", err)
	}
	rootFile := os.NewFile(uintptr(rootFD), root)
	if rootFile == nil {
		_ = unix.Close(rootFD)
		return nil, fmt.Errorf("wrap clone root descriptor")
	}
	defer func() { _ = rootFile.Close() }()

	roots, err := untrackedRoots(reported)
	if err != nil {
		return nil, err
	}
	var entries []UntrackedEntry
	for _, relative := range roots {
		if err := collectPathAt(rootFile, root, relative, &entries, 0); err != nil {
			return nil, err
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func untrackedRoots(reported []string) ([]string, error) {
	set := make(map[string]bool, len(reported))
	for _, raw := range reported {
		relative, err := safeRelativePath(raw)
		if err != nil {
			return nil, err
		}
		set[relative] = true
	}
	roots := make([]string, 0, len(set))
	for path := range set {
		contained := false
		for other := range set {
			if other != path && strings.HasPrefix(path, other+"/") {
				contained = true
				break
			}
		}
		if !contained {
			roots = append(roots, path)
		}
	}
	sort.Strings(roots)
	return roots, nil
}

func safeRelativePath(raw string) (string, error) {
	path := strings.TrimSuffix(strings.TrimSpace(raw), "/")
	if path == "" || path == "." || filepath.IsAbs(path) {
		return "", fmt.Errorf("untracked path %q is not a relative clone path", raw)
	}
	clean := filepath.Clean(path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("untracked path %q escapes the clone", raw)
	}
	return filepath.ToSlash(clean), nil
}

func collectPathAt(root *os.File, rootPath, relative string, entries *[]UntrackedEntry, depth int) error {
	if depth > untrackedMaxDepth {
		return fmt.Errorf("untracked path %s nests deeper than %d directories", relative, untrackedMaxDepth)
	}
	parent, name, err := openParentAt(root, rootPath, relative)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	return collectEntryAt(parent, rootPath, relative, name, entries, depth)
}

func collectEntryAt(parent *os.File, rootPath, relative, name string, entries *[]UntrackedEntry, depth int) error {
	var stat unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("inspect untracked path %s: %w", relative, err)
	}
	if stat.Mode&unix.S_IFMT == unix.S_IFLNK {
		return fmt.Errorf("untracked path %s is a symlink; refusing to follow or delete it", relative)
	}
	entry := entryFromStat(relative, stat)
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		hash, err := hashFileAt(parent, relative, name, stat)
		if err != nil {
			return err
		}
		entry.hash = hash
		*entries = append(*entries, entry)
		return nil
	case unix.S_IFDIR:
		*entries = append(*entries, entry)
		child, err := openDirectoryAt(parent, relative, name, stat)
		if err != nil {
			return err
		}
		defer func() { _ = child.Close() }()
		names, err := directoryNames(child, relative)
		if err != nil {
			return err
		}
		for _, childName := range names {
			childRelative := relative + "/" + childName
			if err := collectEntryAt(child, rootPath, childRelative, childName, entries, depth+1); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("untracked path %s is not a regular file or directory", relative)
	}
}

func entryFromStat(path string, stat unix.Stat_t) UntrackedEntry {
	kind := "file"
	if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
		kind = "directory"
	}
	return UntrackedEntry{Path: path, Kind: kind, Size: stat.Size, device: uint64(stat.Dev), inode: uint64(stat.Ino), mode: uint32(stat.Mode)}
}

func openParentAt(root *os.File, rootPath, relative string) (*os.File, string, error) {
	parts := strings.Split(relative, "/")
	parentFD, err := unix.Openat(int(root.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, "", fmt.Errorf("open clone root for %s: %w", relative, err)
	}
	parent := os.NewFile(uintptr(parentFD), rootPath)
	if parent == nil {
		_ = unix.Close(parentFD)
		return nil, "", fmt.Errorf("wrap clone root for %s", relative)
	}
	for _, part := range parts[:len(parts)-1] {
		var stat unix.Stat_t
		if err := unix.Fstatat(int(parent.Fd()), part, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			_ = parent.Close()
			return nil, "", fmt.Errorf("inspect untracked parent %s: %w", relative, err)
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
			_ = parent.Close()
			return nil, "", fmt.Errorf("untracked parent of %s is not a directory", relative)
		}
		child, err := openDirectoryAt(parent, rootPath, part, stat)
		_ = parent.Close()
		if err != nil {
			return nil, "", err
		}
		parent = child
	}
	return parent, parts[len(parts)-1], nil
}

func openDirectoryAt(parent *os.File, path, name string, expected unix.Stat_t) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open untracked directory %s: %w", path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("wrap untracked directory %s", path)
	}
	var actual unix.Stat_t
	if err := unix.Fstat(fd, &actual); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect opened untracked directory %s: %w", path, err)
	}
	if actual.Dev != expected.Dev || actual.Ino != expected.Ino {
		_ = file.Close()
		return nil, fmt.Errorf("untracked directory %s was replaced while planning", path)
	}
	return file, nil
}

func hashFileAt(parent *os.File, relative, name string, expected unix.Stat_t) (string, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", fmt.Errorf("open untracked file %s: %w", relative, err)
	}
	file := os.NewFile(uintptr(fd), relative)
	if file == nil {
		_ = unix.Close(fd)
		return "", fmt.Errorf("wrap untracked file %s", relative)
	}
	defer func() { _ = file.Close() }()
	var actual unix.Stat_t
	if err := unix.Fstat(fd, &actual); err != nil {
		return "", fmt.Errorf("inspect opened untracked file %s: %w", relative, err)
	}
	if actual.Dev != expected.Dev || actual.Ino != expected.Ino || actual.Size != expected.Size {
		return "", fmt.Errorf("untracked file %s was replaced while planning", relative)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash untracked file %s: %w", relative, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func directoryNames(directory *os.File, path string) ([]string, error) {
	fd, err := unix.Openat(int(directory.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("list untracked directory %s: %w", path, err)
	}
	listing := os.NewFile(uintptr(fd), path)
	if listing == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("wrap listing descriptor for %s", path)
	}
	defer func() { _ = listing.Close() }()
	names, err := listing.Readdirnames(-1)
	if err != nil {
		return nil, fmt.Errorf("list untracked directory %s: %w", path, err)
	}
	sort.Strings(names)
	return names, nil
}

func deleteExactUntracked(ctx context.Context, root string, planned []UntrackedEntry) error {
	if len(planned) == 0 {
		return fmt.Errorf("%w: empty plan", errUntrackedPlanDrift)
	}
	_ = ctx // gitops.Status is intentionally non-interactive and has no context parameter.
	status, err := gitops.Status(root)
	if err != nil {
		return fmt.Errorf("%w: cannot reread git status: %v", errUntrackedPlanDrift, err)
	}
	current, err := planUntracked(root, status.Untracked)
	if err != nil {
		return fmt.Errorf("%w: cannot reread paths: %v", errUntrackedPlanDrift, err)
	}
	if !samePlan(planned, current) {
		return fmt.Errorf("%w: paths, descriptors, or file content changed after the dry-run itemization", errUntrackedPlanDrift)
	}

	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open clone root for deletion: %w", err)
	}
	rootFile := os.NewFile(uintptr(rootFD), root)
	if rootFile == nil {
		_ = unix.Close(rootFD)
		return fmt.Errorf("wrap clone root deletion descriptor")
	}
	defer func() { _ = rootFile.Close() }()

	manifest := make(map[string]UntrackedEntry, len(planned))
	for _, entry := range planned {
		manifest[entry.Path] = entry
	}
	for _, rootPath := range planRoots(planned) {
		if err := removeExactPathAt(rootFile, root, rootPath, manifest, 0); err != nil {
			return err
		}
	}
	return nil
}

func planRoots(entries []UntrackedEntry) []string {
	set := make(map[string]bool, len(entries))
	for _, entry := range entries {
		set[entry.Path] = true
	}
	var roots []string
	for path := range set {
		parent := filepath.ToSlash(filepath.Dir(path))
		if parent == "." || !set[parent] {
			roots = append(roots, path)
		}
	}
	sort.Strings(roots)
	return roots
}

func samePlan(want, got []UntrackedEntry) bool {
	if len(want) != len(got) {
		return false
	}
	for index := range want {
		if want[index].Path != got[index].Path || want[index].Kind != got[index].Kind || want[index].Size != got[index].Size || want[index].device != got[index].device || want[index].inode != got[index].inode || want[index].mode != got[index].mode || want[index].hash != got[index].hash {
			return false
		}
	}
	return true
}

func removeExactPathAt(root *os.File, rootPath, relative string, manifest map[string]UntrackedEntry, depth int) error {
	if depth > untrackedMaxDepth {
		return fmt.Errorf("%w: untracked path %s nests too deeply", errUntrackedPlanDrift, relative)
	}
	parent, name, err := openParentAt(root, rootPath, relative)
	if err != nil {
		return fmt.Errorf("%w: %v", errUntrackedPlanDrift, err)
	}
	defer func() { _ = parent.Close() }()
	expected, ok := manifest[relative]
	if !ok {
		return fmt.Errorf("%w: %s was not in the authorised manifest", errUntrackedPlanDrift, relative)
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("%w: inspect %s: %v", errUntrackedPlanDrift, relative, err)
	}
	actual := entryFromStat(relative, stat)
	if actual.device != expected.device || actual.inode != expected.inode || actual.mode != expected.mode || actual.Size != expected.Size || actual.Kind != expected.Kind {
		return fmt.Errorf("%w: %s changed while deleting", errUntrackedPlanDrift, relative)
	}
	if actual.Kind == "file" {
		hash, err := hashFileAt(parent, relative, name, stat)
		if err != nil || hash != expected.hash {
			return fmt.Errorf("%w: %s changed while deleting", errUntrackedPlanDrift, relative)
		}
		if err := unix.Unlinkat(int(parent.Fd()), name, 0); err != nil {
			return fmt.Errorf("remove authorised untracked file %s: %w", relative, err)
		}
		return nil
	}

	child, err := openDirectoryAt(parent, relative, name, stat)
	if err != nil {
		return fmt.Errorf("%w: %v", errUntrackedPlanDrift, err)
	}
	names, err := directoryNames(child, relative)
	if err != nil {
		_ = child.Close()
		return err
	}
	for _, childName := range names {
		childRelative := relative + "/" + childName
		if _, ok := manifest[childRelative]; !ok {
			_ = child.Close()
			return fmt.Errorf("%w: additional path %s appeared while deleting", errUntrackedPlanDrift, childRelative)
		}
	}
	_ = child.Close()
	for _, childName := range names {
		if err := removeExactPathAt(root, rootPath, relative+"/"+childName, manifest, depth+1); err != nil {
			return err
		}
	}
	if err := unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR); err != nil {
		return fmt.Errorf("remove authorised untracked directory %s: %w", relative, err)
	}
	return nil
}

type archiveCleanReceipt struct {
	SchemaVersion int              `json:"schema_version"`
	Phase         string           `json:"phase"`
	Repository    string           `json:"repository"`
	ClonePath     string           `json:"clone_path"`
	CreatedAt     time.Time        `json:"created_at"`
	Untracked     []UntrackedEntry `json:"untracked"`
}

func writeArchiveCleanReceipt(projectsRoot string, receipt archiveCleanReceipt) (string, error) {
	home, err := wbhome.EnsureRoot(projectsRoot)
	if err != nil {
		return "", err
	}
	directory := filepath.Join(home, "reports", "archive-clean")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create archive-clean receipt directory: %w", err)
	}
	name := fmt.Sprintf("%s-%s.json", receipt.CreatedAt.Format("20060102T150405.000000000Z"), strings.ReplaceAll(receipt.Repository, "/", "--"))
	path := filepath.Join(directory, name)
	if err := overwriteArchiveCleanReceipt(path, receipt); err != nil {
		return "", err
	}
	return path, nil
}

func overwriteArchiveCleanReceipt(path string, receipt archiveCleanReceipt) error {
	raw, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return fmt.Errorf("encode archive-clean receipt: %w", err)
	}
	raw = append(raw, '\n')
	directory := filepath.Dir(path)
	temp, err := os.CreateTemp(directory, ".archive-clean-receipt-*")
	if err != nil {
		return fmt.Errorf("create archive-clean receipt: %w", err)
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("protect archive-clean receipt: %w", err)
	}
	if _, err := temp.Write(raw); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write archive-clean receipt: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync archive-clean receipt: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close archive-clean receipt: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("publish archive-clean receipt: %w", err)
	}
	dir, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open archive-clean receipt directory: %w", err)
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync archive-clean receipt directory: %w", err)
	}
	return nil
}
