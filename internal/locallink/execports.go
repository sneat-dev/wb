package locallink

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/console"
)

const defaultCommandTimeout = 30 * time.Minute

// ExecGit implements Git with the installed Git.
type ExecGit struct {
	Timeout time.Duration
}

func (git ExecGit) run(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	return runBounded(ctx, git.Timeout, dir, env, "git", args...)
}

// ContentHash computes a tree identity over the working tree, including
// modified and untracked files, using a temporary index so the caller's own
// index is never touched.
//
// `git write-tree` against that index is the exact bytes Git would record for
// a commit of this tree, which is what makes the hash a real identity rather
// than a checksum of a file listing. Ignored paths — `node_modules`, `dist` —
// stay out, so a rebuild does not change the source identity.
func (git ExecGit) ContentHash(ctx context.Context, dir string) (string, bool, error) {
	index, err := os.CreateTemp("", "wb-locallink-index-*")
	if err != nil {
		return "", false, err
	}
	indexPath := index.Name()
	if err := index.Close(); err != nil {
		return "", false, err
	}
	// git read-tree refuses to populate an index file that already exists as
	// an empty regular file in some versions; removing it leaves only the
	// unique name reserved.
	if err := os.Remove(indexPath); err != nil {
		return "", false, err
	}
	defer func() { _ = os.Remove(indexPath) }()
	env := []string{"GIT_INDEX_FILE=" + indexPath}
	if _, err := git.run(ctx, dir, env, "read-tree", "HEAD"); err != nil {
		// A repository with no commit yet has no HEAD to read; an empty index
		// is the correct starting point rather than a failure.
		if _, emptyErr := git.run(ctx, dir, env, "read-tree", "--empty"); emptyErr != nil {
			return "", false, fmt.Errorf("prepare a temporary index for %s: %w", dir, emptyErr)
		}
	}
	if _, err := git.run(ctx, dir, env, "add", "-A", "."); err != nil {
		return "", false, fmt.Errorf("stage the working tree of %s into a temporary index: %w", dir, err)
	}
	tree, err := git.run(ctx, dir, env, "write-tree")
	if err != nil {
		return "", false, fmt.Errorf("write a tree for %s: %w", dir, err)
	}
	hash := strings.TrimSpace(tree)
	status, err := git.run(ctx, dir, nil, "status", "--porcelain")
	if err != nil {
		return "", false, fmt.Errorf("read status of %s: %w", dir, err)
	}
	return hash, strings.TrimSpace(status) != "", nil
}

// TrackedChanges lists tracked files that differ from HEAD. Untracked paths are
// deliberately excluded: a link creates untracked artefacts by design.
func (git ExecGit) TrackedChanges(ctx context.Context, dir string) ([]string, error) {
	output, err := git.run(ctx, dir, nil, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return nil, fmt.Errorf("read tracked changes in %s: %w", dir, err)
	}
	var paths []string
	for _, line := range strings.Split(output, "\n") {
		if len(line) <= 3 {
			continue
		}
		paths = append(paths, strings.TrimSpace(line[3:]))
	}
	return paths, nil
}

// ExcludePath appends a pattern to the worktree's own exclude file.
//
// The path comes from `git rev-parse --git-path info/exclude`, which is the
// file Git will actually read for *this* worktree. WB never adds the pattern to
// a tracked `.gitignore`: that would be a tracked change, which a local link
// must never make.
func (git ExecGit) ExcludePath(ctx context.Context, dir, pattern string) error {
	path, err := git.excludeFile(ctx, dir)
	if err != nil {
		return err
	}
	existing, err := readLines(path)
	if err != nil {
		return err
	}
	for _, line := range existing {
		if strings.TrimSpace(line) == pattern {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	line := pattern + "\n"
	if len(existing) > 0 && strings.TrimSpace(existing[len(existing)-1]) != "" {
		line = pattern + "\n"
	}
	if _, err := file.WriteString(line); err != nil {
		return fmt.Errorf("append %q to %s: %w", pattern, path, err)
	}
	return nil
}

// ExcludedPatterns reads the worktree's own exclude file.
func (git ExecGit) ExcludedPatterns(ctx context.Context, dir string) ([]string, error) {
	path, err := git.excludeFile(ctx, dir)
	if err != nil {
		return nil, err
	}
	return readLines(path)
}

func (git ExecGit) excludeFile(ctx context.Context, dir string) (string, error) {
	output, err := git.run(ctx, dir, nil, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return "", fmt.Errorf("resolve the exclude file for %s: %w", dir, err)
	}
	path := strings.TrimSpace(output)
	if path == "" {
		return "", fmt.Errorf("git reported no exclude file for %s", dir)
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	return path, nil
}

func readLines(path string) ([]string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return strings.Split(string(contents), "\n"), nil
}

// ExecNode implements Node with the repository's own package manager.
//
// It never runs `pnpm link`. That command writes a `link:` entry into the
// consumer's `package.json`, which is a tracked file, and
// `npm-consumers-link-through-a-built-dist` requires that no tracked file
// changes. What it does instead is exactly what the package manager's link
// mechanism does to `node_modules` — replace the package directory with a
// symlink to a built package staged in the consumer's installed peer context —
// without the manifest edit or provider-side peer resolution.
type ExecNode struct {
	// CacheRoot holds built dists keyed by the library's content hash.
	CacheRoot string
	// ContentHash identifies the library tree the current build belongs to.
	ContentHash string
	Timeout     time.Duration
}

// FrozenInstall implements Node.
func (node ExecNode) FrozenInstall(ctx context.Context, dir string) error {
	manager, install := frozenInstallCommand(dir)
	if manager == "" {
		// No lockfile means there is no frozen baseline to prove. Passing
		// silently made the guarantee look satisfied when it was never
		// checked, so the skip is stated instead — the caller decides whether
		// an unlocked consumer is acceptable, and the report says which it
		// was.
		return &SkippedCheck{
			Check:  "frozen-install",
			Reason: "no pnpm-lock.yaml, yarn.lock or package-lock.json in " + dir + ", so there is no lockfile baseline to prove",
		}
	}
	if _, err := exec.LookPath(manager); err != nil {
		return fmt.Errorf("%s is required to prove a frozen install of %s: %w", manager, dir, err)
	}
	if _, err := runBounded(ctx, node.Timeout, dir, nil, manager, install...); err != nil {
		return err
	}
	return nil
}

func frozenInstallCommand(dir string) (string, []string) {
	switch {
	case fileExists(filepath.Join(dir, "pnpm-lock.yaml")):
		return "pnpm", []string{"install", "--frozen-lockfile"}
	case fileExists(filepath.Join(dir, "yarn.lock")):
		return "yarn", []string{"install", "--immutable"}
	case fileExists(filepath.Join(dir, "package-lock.json")):
		return "npm", []string{"ci"}
	default:
		return "", nil
	}
}

// Build implements Node, caching the built dist by the library's content hash
// so an iterative stream never verifies against a stale build.
func (node ExecNode) Build(ctx context.Context, libraryDir, packageDir string) (string, error) {
	if node.CacheRoot == "" || node.ContentHash == "" {
		return "", fmt.Errorf("a build cache root and library content hash are required")
	}
	cached := filepath.Join(node.CacheRoot, node.ContentHash, buildCacheKey(node.ContentHash, packageDir))
	marker := filepath.Join(cached, buildMarkerName)
	if fileExists(marker) {
		contents, err := os.ReadFile(marker)
		if err == nil {
			if dist := strings.TrimSpace(string(contents)); dist != "" && fileExists(dist) {
				return dist, nil
			}
		}
	}
	command, args, err := nodeBuildCommand(libraryDir, packageDir)
	if err != nil {
		return "", err
	}
	if _, err := exec.LookPath(command); err != nil {
		return "", fmt.Errorf("%s is required to build %s: %w", command, packageDir, err)
	}
	if _, err := runBounded(ctx, node.Timeout, libraryDir, nil, command, args...); err != nil {
		return "", err
	}
	dist, err := builtDist(libraryDir, packageDir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(cached, 0o755); err != nil {
		return "", fmt.Errorf("create the build cache directory: %w", err)
	}
	if err := os.WriteFile(marker, []byte(dist+"\n"), 0o644); err != nil {
		return "", fmt.Errorf("record the cached build: %w", err)
	}
	return dist, nil
}

func nodeBuildCommand(libraryDir, packageDir string) (string, []string, error) {
	manager := packageManager(libraryDir)
	projectPath := filepath.Join(packageDir, "project.json")
	if contents, err := os.ReadFile(projectPath); err == nil {
		var project struct {
			Name    string                     `json:"name"`
			Targets map[string]json.RawMessage `json:"targets"`
		}
		if err := json.Unmarshal(contents, &project); err != nil {
			return "", nil, fmt.Errorf("parse %s: %w", projectPath, err)
		}
		if _, hasBuild := project.Targets["build"]; hasBuild {
			if strings.TrimSpace(project.Name) == "" {
				return "", nil, fmt.Errorf("%s declares a build target without a project name", projectPath)
			}
			switch manager {
			case "pnpm":
				return manager, []string{"exec", "nx", "build", project.Name}, nil
			case "yarn":
				return manager, []string{"nx", "build", project.Name}, nil
			default:
				return manager, []string{"exec", "nx", "--", "build", project.Name}, nil
			}
		}
	} else if !os.IsNotExist(err) {
		return "", nil, fmt.Errorf("read %s: %w", projectPath, err)
	}
	manifest := filepath.Join(libraryDir, "package.json")
	contents, err := os.ReadFile(manifest)
	if err != nil {
		return "", nil, fmt.Errorf("read %s: %w", manifest, err)
	}
	var workspace struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(contents, &workspace); err != nil {
		return "", nil, fmt.Errorf("parse %s: %w", manifest, err)
	}
	if strings.TrimSpace(workspace.Scripts["build"]) == "" {
		return "", nil, fmt.Errorf("%s has neither an Nx project build target nor a workspace build script", packageDir)
	}
	return manager, []string{"run", "build"}, nil
}

// buildMarkerName records which dist a cached build produced.
const buildMarkerName = ".wb-built"

// buildCacheKey names one package's build inside one library content hash.
// Keying on the hash is what makes an iterative stream rebuild whenever the
// library tree moves; building once and reusing it would have consumers
// verifying against a stale dist and reporting false green.
func buildCacheKey(contentHash, packageDir string) string {
	key := sha256.Sum256([]byte(contentHash + "\x00" + packageDir))
	return hex.EncodeToString(key[:8])
}

// builtDist locates the built output of one package. It reads the package's own
// manifest first — a workspace names its output there — and falls back to the
// conventional dist directories, reporting rather than guessing when neither
// exists.
func builtDist(libraryDir, packageDir string) (string, error) {
	relative, err := filepath.Rel(libraryDir, packageDir)
	if err != nil {
		relative = filepath.Base(packageDir)
	}
	candidates := []string{
		filepath.Join(packageDir, "dist"),
		filepath.Join(libraryDir, "dist", relative),
		filepath.Join(libraryDir, "dist", filepath.Base(packageDir)),
	}
	for _, candidate := range candidates {
		if fileExists(filepath.Join(candidate, "package.json")) {
			return candidate, nil
		}
	}
	for _, candidate := range candidates {
		if fileExists(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("the build produced no output for %s; looked in %s", packageDir, strings.Join(candidates, ", "))
}

func packageManager(dir string) string {
	switch {
	case fileExists(filepath.Join(dir, "pnpm-lock.yaml")):
		return "pnpm"
	case fileExists(filepath.Join(dir, "yarn.lock")):
		return "yarn"
	default:
		return "npm"
	}
}

// linkBackupSuffix names the directory a real installed package is moved to, so
// Unlink restores exactly what was there rather than reinstalling.
const linkBackupSuffix = ".wb-locallink-backup"

// linkSymlinkBackupSuffix names the file recording where an existing SYMLINK
// pointed, so Unlink can re-create it exactly.
const linkSymlinkBackupSuffix = ".wb-locallink-symlink"

// linkAppliedMarkerSuffix proves that WB began applying this exact npm link.
// Intent-only records have no marker, so undo preserves the published package.
const linkAppliedMarkerSuffix = ".wb-locallink-applied"

func linkAppliedMarkerPath(consumerDir, packageName string) string {
	target := filepath.Join(consumerDir, "node_modules", filepath.FromSlash(packageName))
	return target + linkAppliedMarkerSuffix
}

// Link implements Node.
//
// Both shapes a package manager leaves in node_modules are preserved. pnpm's
// default isolated store makes node_modules/<pkg> a SYMLINK into .pnpm/…, and
// an earlier version simply deleted that symlink with no backup — so `--undo`
// left the consumer with no package at all until someone re-installed. npm's
// flat layout leaves a real directory, which is moved aside.
func (node ExecNode) Link(ctx context.Context, consumerDir, packageName, dist string) (result NodeLinkResult, returnedErr error) {
	target := filepath.Join(consumerDir, "node_modules", filepath.FromSlash(packageName))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return result, fmt.Errorf("create %s: %w", filepath.Dir(target), err)
	}
	marker := linkAppliedMarkerPath(consumerDir, packageName)
	if fileExists(marker) {
		if err := node.Unlink(ctx, consumerDir, packageName); err != nil {
			return result, fmt.Errorf("restore the prior local link before refreshing %s: %w", packageName, err)
		}
	}
	info, statErr := os.Lstat(target)
	if statErr != nil && !os.IsNotExist(statErr) {
		return result, fmt.Errorf("inspect %s: %w", target, statErr)
	}
	stage, err := nodeLinkStagePath(consumerDir, target, info)
	if err != nil {
		return result, err
	}
	symlinkBackup := target + linkSymlinkBackupSuffix
	directoryBackup := target + linkBackupSuffix
	for _, backup := range []string{symlinkBackup, directoryBackup} {
		if fileExists(backup) {
			return result, fmt.Errorf("refuse to replace unexpected local-link recovery artifact %s; inspect it or complete the prior undo", backup)
		}
	}
	if err := validateBuiltPackageSource(dist); err != nil {
		return result, fmt.Errorf("stage %s in the consumer's installed peer context: %w", packageName, err)
	}
	if err := os.Mkdir(stage, 0o755); err != nil {
		return result, fmt.Errorf("claim the staged package path %s without replacing existing data: %w", stage, err)
	}
	markerFile, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if cleanupErr := os.Remove(stage); cleanupErr != nil {
			return result, fmt.Errorf("record the pending link for %s: %w; preserve unclaimed stage %s: %v", packageName, err, stage, cleanupErr)
		}
		return result, fmt.Errorf("record the pending link for %s (run --undo if a prior attempt was interrupted): %w", packageName, err)
	}
	if _, err := markerFile.WriteString(stage + "\n"); err != nil {
		_ = markerFile.Close()
		_ = os.Remove(marker)
		_ = os.Remove(stage)
		return result, fmt.Errorf("record the staged link path for %s: %w", packageName, err)
	}
	if err := markerFile.Close(); err != nil {
		_ = os.Remove(marker)
		_ = os.Remove(stage)
		return result, fmt.Errorf("close the pending link marker for %s: %w", packageName, err)
	}
	defer func() {
		if returnedErr == nil {
			return
		}
		if cleanupErr := node.Unlink(ctx, consumerDir, packageName); cleanupErr != nil {
			returnedErr = fmt.Errorf("%w; restore the published package after the failed link: %v", returnedErr, cleanupErr)
		}
	}()
	if err := copyBuiltPackageContents(dist, stage); err != nil {
		return result, fmt.Errorf("stage %s in the consumer's installed peer context: %w", packageName, err)
	}
	info, err = os.Lstat(target)
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		// Record where it pointed before replacing it. Without this the
		// installed package is unrecoverable on every pnpm consumer.
		existing, readErr := os.Readlink(target)
		if readErr != nil {
			return result, fmt.Errorf("read the existing link at %s: %w", target, readErr)
		}
		backupFile, err := os.OpenFile(symlinkBackup, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return result, fmt.Errorf("claim the link recovery record %s without replacing existing data: %w", symlinkBackup, err)
		}
		if _, err := backupFile.WriteString(existing); err != nil {
			_ = backupFile.Close()
			_ = os.Remove(symlinkBackup)
			return result, fmt.Errorf("record the existing link target of %s: %w", packageName, err)
		}
		if err := backupFile.Close(); err != nil {
			_ = os.Remove(symlinkBackup)
			return result, fmt.Errorf("close the existing link target record of %s: %w", packageName, err)
		}
		if err := os.Remove(target); err != nil {
			return result, fmt.Errorf("replace the existing link at %s: %w", target, err)
		}
		result.Previous = filepath.ToSlash(filepath.Join("node_modules", filepath.FromSlash(packageName)+linkSymlinkBackupSuffix))
	case err == nil:
		if err := os.Rename(target, directoryBackup); err != nil {
			return result, fmt.Errorf("set aside the installed %s: %w", packageName, err)
		}
		result.Previous = filepath.ToSlash(filepath.Join("node_modules", filepath.FromSlash(packageName)+linkBackupSuffix))
	case !os.IsNotExist(err):
		return result, fmt.Errorf("inspect %s: %w", target, err)
	}
	if err := os.Symlink(stage, target); err != nil {
		return result, fmt.Errorf("link %s to staged package %s: %w", target, stage, err)
	}
	for _, artifact := range []string{target, marker, stage, target + linkSymlinkBackupSuffix, target + linkBackupSuffix} {
		if !fileExists(artifact) {
			continue
		}
		relative, err := filepath.Rel(consumerDir, artifact)
		if err != nil {
			return result, fmt.Errorf("record generated path %s: %w", artifact, err)
		}
		result.Artifacts = append(result.Artifacts, filepath.ToSlash(relative))
	}
	return result, nil
}

func nodeLinkStagePath(consumerDir, target string, info os.FileInfo) (string, error) {
	parent := filepath.Dir(target)
	if info != nil && info.Mode()&os.ModeSymlink != 0 {
		existing, err := os.Readlink(target)
		if err != nil {
			return "", fmt.Errorf("read the installed package link at %s: %w", target, err)
		}
		if !filepath.IsAbs(existing) {
			existing = filepath.Join(filepath.Dir(target), existing)
		}
		parent = filepath.Dir(filepath.Clean(existing))
	}
	resolvedConsumer, err := filepath.EvalSymlinks(consumerDir)
	if err != nil {
		return "", fmt.Errorf("resolve consumer npm workspace %s: %w", consumerDir, err)
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("resolve installed peer context %s: %w", parent, err)
	}
	relative, err := filepath.Rel(resolvedConsumer, resolvedParent)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("installed peer context %s resolves outside consumer npm workspace %s", parent, consumerDir)
	}
	return filepath.Join(parent, "."+filepath.Base(target)+".wb-locallink-stage"), nil
}

func copyBuiltPackage(source, destination string) error {
	if err := validateBuiltPackageSource(source); err != nil {
		return err
	}
	if err := os.Mkdir(destination, 0o755); err != nil {
		return err
	}
	return copyBuiltPackageContents(source, destination)
}

func validateBuiltPackageSource(source string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("built package source %s is not a real directory", source)
	}
	return nil
}

func copyBuiltPackageContents(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == source {
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("built package contains unsupported symlink %s", path)
		}
		if entry.IsDir() {
			return os.Mkdir(target, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("built package contains unsupported non-regular file %s", path)
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			_ = input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeErr := output.Close()
		_ = input.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

// Unlink implements Node, restoring whichever shape the link displaced.
func (node ExecNode) Unlink(ctx context.Context, consumerDir, packageName string) error {
	target := filepath.Join(consumerDir, "node_modules", filepath.FromSlash(packageName))
	marker := linkAppliedMarkerPath(consumerDir, packageName)
	stage := ""
	if contents, err := os.ReadFile(marker); err == nil {
		var validateErr error
		stage, validateErr = validateStagedLinkPath(consumerDir, target, strings.TrimSpace(string(contents)))
		if validateErr != nil {
			return validateErr
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read the applied-link marker for %s: %w", packageName, err)
	}

	info, err := os.Lstat(target)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		actual, readErr := os.Readlink(target)
		if readErr != nil {
			return fmt.Errorf("read the active link at %s: %w", target, readErr)
		}
		actualPath := actual
		if !filepath.IsAbs(actualPath) {
			actualPath = filepath.Join(filepath.Dir(target), actualPath)
		}
		if stage != "" && filepath.Clean(actualPath) != stage {
			symlinkBackup := target + linkSymlinkBackupSuffix
			if original, backupErr := os.ReadFile(symlinkBackup); backupErr == nil && strings.TrimSpace(string(original)) == actual {
				if _, statErr := os.Stat(target); statErr != nil {
					return fmt.Errorf("restored the original link for %s but it is dangling — %s no longer resolves; re-install to recover the published package: %w", packageName, actual, statErr)
				}
				if err := os.Remove(symlinkBackup); err != nil {
					return fmt.Errorf("clear the link record for %s: %w", packageName, err)
				}
				return clearStagedLink(stage, marker)
			}
			if fileExists(symlinkBackup) || fileExists(target+linkBackupSuffix) {
				return fmt.Errorf("%s no longer points to the WB-staged output; preserve its backups and inspect before undo", target)
			}
			return clearStagedLink(stage, marker)
		}
		if err := os.Remove(target); err != nil {
			return fmt.Errorf("remove the link at %s: %w", target, err)
		}
	} else if err == nil && stage != "" {
		if fileExists(target+linkSymlinkBackupSuffix) || fileExists(target+linkBackupSuffix) {
			return fmt.Errorf("%s is no longer the WB-created symlink; preserve its backups and inspect before undo", target)
		}
		return clearStagedLink(stage, marker)
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("inspect %s: %w", target, err)
	}

	// A recorded symlink target is restored first: on pnpm this is the normal
	// case, and it is the one that used to be lost entirely.
	symlinkBackup := target + linkSymlinkBackupSuffix
	if contents, readErr := os.ReadFile(symlinkBackup); readErr == nil {
		original := strings.TrimSpace(string(contents))
		if original == "" {
			return fmt.Errorf("the recorded link target for %s is empty; restore it by re-installing", packageName)
		}
		if err := os.Symlink(original, target); err != nil {
			return fmt.Errorf("restore the original link for %s: %w", packageName, err)
		}
		if _, statErr := os.Stat(target); statErr != nil {
			return fmt.Errorf("restored the original link for %s but it is dangling — %s no longer resolves; re-install to recover the published package: %w", packageName, original, statErr)
		}
		if err := os.Remove(symlinkBackup); err != nil {
			return fmt.Errorf("clear the link record for %s: %w", packageName, err)
		}
		return clearStagedLink(stage, marker)
	} else if !os.IsNotExist(readErr) {
		return fmt.Errorf("read the link record for %s: %w", packageName, readErr)
	}
	directoryBackup := target + linkBackupSuffix
	if fileExists(directoryBackup) {
		if err := os.Rename(directoryBackup, target); err != nil {
			return fmt.Errorf("restore the installed %s: %w", packageName, err)
		}
	}
	return clearStagedLink(stage, marker)
}

func validateStagedLinkPath(consumerDir, target, stage string) (string, error) {
	stage = filepath.Clean(stage)
	wantBase := "." + filepath.Base(target) + ".wb-locallink-stage"
	if !filepath.IsAbs(stage) || filepath.Base(stage) != wantBase {
		return "", fmt.Errorf("applied-link marker names invalid staged path %q", stage)
	}
	absoluteConsumer, err := filepath.Abs(consumerDir)
	if err != nil {
		return "", err
	}
	lexical, err := filepath.Rel(filepath.Join(absoluteConsumer, "node_modules"), stage)
	if err != nil || lexical == ".." || strings.HasPrefix(lexical, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("staged package %s is outside consumer npm workspace %s", stage, consumerDir)
	}
	resolvedConsumer, err := filepath.EvalSymlinks(consumerDir)
	if err != nil {
		return "", fmt.Errorf("resolve consumer npm workspace %s: %w", consumerDir, err)
	}
	resolvedParent, err := filepath.EvalSymlinks(filepath.Dir(stage))
	if err != nil {
		if os.IsNotExist(err) {
			return stage, nil
		}
		return "", fmt.Errorf("resolve staged package parent %s: %w", filepath.Dir(stage), err)
	}
	relative, err := filepath.Rel(resolvedConsumer, resolvedParent)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("staged package %s resolves outside consumer npm workspace %s", stage, consumerDir)
	}
	return stage, nil
}

func clearStagedLink(stage, marker string) error {
	if stage != "" {
		if err := os.RemoveAll(stage); err != nil {
			return err
		}
	}
	if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func runBounded(ctx context.Context, timeout time.Duration, dir string, env []string, name string, args ...string) (string, error) {
	if timeout <= 0 {
		timeout = defaultCommandTimeout
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(bounded, name, args...)
	command.Dir = dir
	command.Env = append(console.Env(), env...)
	output, err := command.CombinedOutput()
	if err != nil {
		if bounded.Err() != nil && ctx.Err() == nil {
			return string(output), fmt.Errorf("%s %s timed out after %s: %s", name, strings.Join(args, " "), timeout, strings.TrimSpace(string(output)))
		}
		return string(output), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}
