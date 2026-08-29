package worktrees

// This file owns the explicit recovery boundary for non-empty retired stages.
// Cleanup intentionally remains fail-closed for an unclassified stage; this
// operation is the only path allowed to inspect and retire one.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/wbhome"
	"golang.org/x/sys/unix"
)

type RetiredStageRecoveryOptions struct {
	ProjectsRoot string
	Task         string
	Stage        string
	Apply        bool
}

type RetiredStageRecoveryResult struct {
	WorktreesRoot string `json:"worktrees_root"`
	Task          string `json:"task"`
	Path          string `json:"path"`
	Stage         string `json:"stage"`
	GitRepository string `json:"git_repository,omitempty"`
	GitDir        string `json:"git_dir,omitempty"`
	Branch        string `json:"branch,omitempty"`
	HeadSHA       string `json:"head_sha,omitempty"`
	RemoteURL     string `json:"remote_url,omitempty"`
	ContentDigest string `json:"content_digest"`
	FileCount     int    `json:"file_count"`
	ByteCount     int64  `json:"byte_count"`
	SymlinkCount  int    `json:"symlink_count,omitempty"`
	StageDevice   uint64 `json:"stage_device,omitempty"`
	StageInode    uint64 `json:"stage_inode,omitempty"`
	Durable       bool   `json:"durable"`
	Eligible      bool   `json:"eligible"`
	Applied       bool   `json:"applied"`
	ArchivePath   string `json:"archive_path,omitempty"`
	Disposition   string `json:"disposition"`
	Reason        string `json:"reason"`
}

type RetiredStageRecoveryOutcome struct {
	Apply       bool                         `json:"apply"`
	ReceiptPath string                       `json:"receipt_path,omitempty"`
	Results     []RetiredStageRecoveryResult `json:"results"`
}

type stageContentInventory struct {
	Digest    string
	Files     int
	Bytes     int64
	Symlinks  int
	Ambiguous bool
}

// RecoverRetiredStages inspects exact WB-retired stage directories without
// following symlinks. Apply archives a stage by descriptor-anchored rename
// only after a fresh inventory matches the plan. The archive and receipt live
// below WB_HOME/reports and are private to the local operator.
func RecoverRetiredStages(ctx context.Context, options RetiredStageRecoveryOptions) (RetiredStageRecoveryOutcome, error) {
	if options.Task == "" || !validSafeSegment(options.Task) {
		return RetiredStageRecoveryOutcome{}, fmt.Errorf("stage recovery requires one valid task name")
	}
	if options.Stage != "" && !isRetiredWorktreeStagingDirectory(options.Stage) {
		return RetiredStageRecoveryOutcome{}, fmt.Errorf("stage must be a .wb-retired-stage-* directory name")
	}
	resolution, err := wbhome.Resolve(options.ProjectsRoot)
	if err != nil {
		return RetiredStageRecoveryOutcome{}, err
	}
	seenRoots := map[string]bool{}
	results := make([]RetiredStageRecoveryResult, 0)
	for _, layout := range resolution.Read {
		root := filepath.Clean(layout.WorktreesRoot)
		if seenRoots[root] {
			continue
		}
		seenRoots[root] = true
		taskPath := filepath.Join(root, options.Task)
		entries, readErr := os.ReadDir(taskPath)
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return RetiredStageRecoveryOutcome{}, fmt.Errorf("read retired stages for %s: %w", options.Task, readErr)
		}
		for _, entry := range entries {
			if options.Stage != "" && entry.Name() != options.Stage {
				continue
			}
			if !isRetiredWorktreeStagingDirectory(entry.Name()) {
				continue
			}
			path := filepath.Join(taskPath, entry.Name())
			result := inspectRetiredStage(ctx, root, options.Task, path, entry.Name())
			if options.Apply && result.Eligible {
				applyRetiredStageRecovery(resolution.Write.Home, &result)
			}
			results = append(results, result)
		}
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].WorktreesRoot != results[j].WorktreesRoot {
			return results[i].WorktreesRoot < results[j].WorktreesRoot
		}
		return results[i].Path < results[j].Path
	})
	outcome := RetiredStageRecoveryOutcome{Apply: options.Apply, Results: results}
	if len(results) > 0 {
		outcome.ReceiptPath = retiredStageReceiptPath(resolution.Write.Home, results)
		if options.Apply {
			if err := writeRetiredStageReceipt(outcome.ReceiptPath, outcome); err != nil {
				return outcome, err
			}
		}
	} else if options.Stage != "" {
		// A completed rename removes the active stage. Re-reading the matching
		// receipt makes a repeated invocation an honest resumable no-op instead
		// of losing the only durable recovery pointer.
		outcome.ReceiptPath = findRetiredStageReceipt(resolution.Write.Home, options.Task, options.Stage)
	}
	return outcome, nil
}

func inspectRetiredStage(ctx context.Context, root, task, path, name string) RetiredStageRecoveryResult {
	result := RetiredStageRecoveryResult{WorktreesRoot: root, Task: task, Path: path, Stage: name,
		Disposition: "unclassified_retired_stage", Reason: "stage is preserved until this explicit audited recovery runs"}
	info, err := os.Lstat(path)
	if err != nil {
		result.Reason = "cannot inspect retired stage: " + err.Error()
		return result
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		result.Reason = "retired stage is a symlink or not a no-follow directory; ambiguous evidence is preserved"
		return result
	}
	directory, err := openAbsoluteDirectoryNoFollow(path, false)
	if err != nil {
		result.Reason = "cannot open retired stage without following links: " + err.Error()
		return result
	}
	defer directory.Close()
	var identity unix.Stat_t
	if err := unix.Fstat(int(directory.Fd()), &identity); err != nil {
		result.Reason = "cannot inspect retired stage identity: " + err.Error()
		return result
	}
	result.StageDevice, result.StageInode = uint64(identity.Dev), uint64(identity.Ino)
	inventory, err := inventoryStage(path)
	if err != nil {
		result.Reason = "cannot inventory retired stage without following links: " + err.Error()
		return result
	}
	result.ContentDigest, result.FileCount, result.ByteCount, result.SymlinkCount = inventory.Digest, inventory.Files, inventory.Bytes, inventory.Symlinks
	result.GitRepository, result.GitDir, result.Branch, result.HeadSHA, result.RemoteURL = inspectStageGit(ctx, path)
	result.Durable = stageContentIsDurable(ctx, path, result.HeadSHA, inventory.Ambiguous)
	result.Eligible = true
	if result.Durable {
		result.Disposition = "archive_proven_durable_stage"
		result.Reason = "Git commit and clean content are durable; the exact stage will be privately archived before retirement"
	} else {
		result.Disposition = "archive_recoverable_stage"
		result.Reason = "content is not proven durable; the exact stage will be privately archived before retirement"
	}
	return result
}

func inspectStageGit(ctx context.Context, path string) (repository, gitDir, branch, head, remote string) {
	inside, err := git(ctx, path, "rev-parse", "--is-inside-work-tree")
	if err != nil || inside != "true" {
		return "", "", "", "", ""
	}
	repository, _ = git(ctx, path, "rev-parse", "--show-toplevel")
	gitDir, _ = git(ctx, path, "rev-parse", "--git-common-dir")
	branch, _ = git(ctx, path, "branch", "--show-current")
	head, _ = git(ctx, path, "rev-parse", "HEAD")
	remote, _ = git(ctx, path, "remote", "get-url", "origin")
	return
}

func stageContentIsDurable(ctx context.Context, path, head string, ambiguous bool) bool {
	if ambiguous || head == "" {
		return false
	}
	if _, err := git(ctx, path, "cat-file", "-e", head+"^{commit}"); err != nil {
		return false
	}
	status, err := git(ctx, path, "status", "--porcelain", "--untracked-files=all")
	return err == nil && status == ""
}

func inventoryStage(path string) (stageContentInventory, error) {
	hash := sha256.New()
	var inventory stageContentInventory
	err := filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == path {
			return nil
		}
		relative, err := filepath.Rel(path, current)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%d\x00", filepath.ToSlash(relative), mode.String(), info.Size())
		switch {
		case mode&os.ModeSymlink != 0:
			target, err := os.Readlink(current)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(hash, "%s\x00", target)
			inventory.Files++
			inventory.Symlinks++
			inventory.Ambiguous = true
		case mode.IsRegular():
			contents, err := os.ReadFile(current)
			if err != nil {
				return err
			}
			_, _ = hash.Write(contents)
			inventory.Files++
			inventory.Bytes += int64(len(contents))
		case mode.IsDir():
			// Directory names and modes are already part of the manifest.
		default:
			inventory.Ambiguous = true
		}
		return nil
	})
	if err != nil {
		return stageContentInventory{}, err
	}
	inventory.Digest = hex.EncodeToString(hash.Sum(nil))
	return inventory, nil
}

func retiredStageArchivePath(home string, result RetiredStageRecoveryResult) string {
	return filepath.Join(home, "reports", "worktree-stage-recovery", result.Task,
		result.Stage+"-"+result.ContentDigest[:16])
}

func retiredStageReceiptPath(home string, results []RetiredStageRecoveryResult) string {
	if len(results) == 0 {
		return ""
	}
	digest := sha256.New()
	for _, result := range results {
		_, _ = fmt.Fprintf(digest, "%s\x00%s\x00%s\x00", result.WorktreesRoot, result.Task, result.Stage)
		_, _ = fmt.Fprintf(digest, "%s\x00%d\x00%d\x00", result.ContentDigest, result.FileCount, result.ByteCount)
	}
	id := hex.EncodeToString(digest.Sum(nil))[:16]
	return filepath.Join(home, "reports", "worktree-stage-recovery", results[0].Task, "receipt-"+id+".json")
}

func applyRetiredStageRecovery(home string, result *RetiredStageRecoveryResult) {
	planned, err := inventoryStage(result.Path)
	if err != nil || planned.Digest != result.ContentDigest || planned.Files != result.FileCount || planned.Bytes != result.ByteCount || planned.Symlinks != result.SymlinkCount {
		result.Eligible = false
		result.Reason = "retired stage changed after inventory; ambiguous evidence was left untouched"
		return
	}
	task, err := acquireCleanupTaskAt(result.WorktreesRoot, result.Task)
	if err != nil {
		result.Eligible = false
		result.Reason = "cannot acquire task lock for audited stage recovery: " + err.Error()
		return
	}
	defer task.close()
	stage, err := openAbsoluteDirectoryNoFollow(result.Path, false)
	if err != nil {
		result.Eligible = false
		result.Reason = "cannot open retired stage without following links: " + err.Error()
		return
	}
	defer stage.Close()
	if !directoryStillMatches(result.Path, stage) {
		result.Eligible = false
		result.Reason = "retired stage path changed before recovery; ambiguous evidence was left untouched"
		return
	}
	var identity unix.Stat_t
	if err := unix.Fstat(int(stage.Fd()), &identity); err != nil || uint64(identity.Dev) != result.StageDevice || uint64(identity.Ino) != result.StageInode {
		result.Eligible = false
		result.Reason = "retired stage identity changed before recovery; ambiguous evidence was left untouched"
		return
	}
	archivePath := retiredStageArchivePath(home, *result)
	archiveParent, err := openAbsoluteDirectoryNoFollow(filepath.Dir(archivePath), true)
	if err != nil {
		result.Eligible = false
		result.Reason = "cannot open private recovery archive: " + err.Error()
		return
	}
	defer archiveParent.Close()
	if _, statErr := os.Lstat(archivePath); statErr == nil {
		result.Eligible = false
		result.Reason = "deterministic archive already exists while source remains; refusing ambiguous duplicate"
		return
	} else if !errors.Is(statErr, os.ErrNotExist) {
		result.Eligible = false
		result.Reason = "cannot inspect deterministic archive destination: " + statErr.Error()
		return
	}
	moved, err := moveExpectedDirectoryNoReplace(task.task, result.Stage, archiveParent, filepath.Base(archivePath), stage, nil)
	if err != nil {
		if moved != nil {
			_ = moved.Close()
		}
		result.Eligible = false
		result.Reason = "descriptor-safely archive retired stage: " + err.Error()
		return
	}
	_ = moved.Close()
	result.Applied = true
	result.ArchivePath = archivePath
	result.Disposition = "archived_retired_stage"
	result.Reason = "content preserved in a private deterministic recovery archive"
}

func writeRetiredStageReceipt(path string, outcome RetiredStageRecoveryOutcome) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create private stage recovery receipt directory: %w", err)
	}
	contents, err := json.MarshalIndent(outcome, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, contents, 0o600); err != nil {
		return fmt.Errorf("write private stage recovery receipt: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("publish private stage recovery receipt: %w", err)
	}
	return nil
}

func findRetiredStageReceipt(home, task, stage string) string {
	root := filepath.Join(home, "reports", "worktree-stage-recovery")
	var found string
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || !strings.HasPrefix(entry.Name(), "receipt-") || !strings.HasSuffix(entry.Name(), ".json") {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var receipt RetiredStageRecoveryOutcome
		if json.Unmarshal(contents, &receipt) != nil {
			return nil
		}
		for _, result := range receipt.Results {
			if result.Task == task && result.Stage == stage && result.Applied {
				found = path
				return filepath.SkipDir
			}
		}
		return nil
	})
	if found == "" {
		// A receipt may have been written by a prior process with a partial
		// result. The task-scoped receipt name is still a safe resumable pointer;
		// return it rather than pretending the archived evidence disappeared.
		if entries, err := os.ReadDir(filepath.Join(root, task)); err == nil {
			for _, entry := range entries {
				if !entry.IsDir() && strings.HasPrefix(entry.Name(), "receipt-") && strings.HasSuffix(entry.Name(), ".json") {
					return filepath.Join(root, task, entry.Name())
				}
			}
		}
	}
	return found
}
