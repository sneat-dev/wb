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
	"slices"
	"sort"
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
		if _, err := node.Unlink(ctx, consumerDir, packageName); err != nil {
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
		if _, cleanupErr := node.Unlink(ctx, consumerDir, packageName); cleanupErr != nil {
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
func (node ExecNode) Unlink(ctx context.Context, consumerDir, packageName string) (string, error) {
	target := filepath.Join(consumerDir, "node_modules", filepath.FromSlash(packageName))
	marker := linkAppliedMarkerPath(consumerDir, packageName)
	stage := ""
	if contents, err := os.ReadFile(marker); err == nil {
		var validateErr error
		stage, validateErr = validateStagedLinkPath(consumerDir, target, strings.TrimSpace(string(contents)))
		if validateErr != nil {
			return "", validateErr
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read the applied-link marker for %s: %w", packageName, err)
	}

	info, err := os.Lstat(target)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		actual, readErr := os.Readlink(target)
		if readErr != nil {
			return "", fmt.Errorf("read the active link at %s: %w", target, readErr)
		}
		actualPath := actual
		if !filepath.IsAbs(actualPath) {
			actualPath = filepath.Join(filepath.Dir(target), actualPath)
		}
		if stage != "" && filepath.Clean(actualPath) != stage {
			symlinkBackup := target + linkSymlinkBackupSuffix
			if original, backupErr := os.ReadFile(symlinkBackup); backupErr == nil && strings.TrimSpace(string(original)) == actual {
				if _, statErr := os.Stat(target); statErr != nil {
					return "", fmt.Errorf("restored the original link for %s but it is dangling — %s no longer resolves; re-install to recover the published package: %w", packageName, actual, statErr)
				}
				if err := os.Remove(symlinkBackup); err != nil {
					return "", fmt.Errorf("clear the link record for %s: %w", packageName, err)
				}
				return "", clearStagedLink(stage, marker)
			}
			// The package manager itself may already have replaced the WB
			// stage with a published copy — a governed `pnpm install` mid
			// stream is the common trigger. That is not the dangerous case
			// the backups exist to guard: the filesystem already holds a
			// real, resolvable package, so refusing here only leaves a dead
			// record that blocks `wb pr land`'s live-local-link preflight
			// forever. Confirm it really is a published copy before ever
			// clearing quietly.
			if name, version, ok := resolvePublishedPackage(actualPath, packageName); ok {
				if err := clearSupersededLink(stage, marker, target); err != nil {
					return "", err
				}
				return fmt.Sprintf("link superseded by published %s@%s; record cleared", name, version), nil
			}
			if fileExists(symlinkBackup) || fileExists(target+linkBackupSuffix) {
				return "", fmt.Errorf("%s no longer points to the WB-staged output; preserve its backups and inspect before undo", target)
			}
			return "", clearStagedLink(stage, marker)
		}
		if err := os.Remove(target); err != nil {
			return "", fmt.Errorf("remove the link at %s: %w", target, err)
		}
	} else if err == nil && stage != "" {
		if name, version, ok := resolvePublishedPackage(target, packageName); ok {
			if err := clearSupersededLink(stage, marker, target); err != nil {
				return "", err
			}
			return fmt.Sprintf("link superseded by published %s@%s; record cleared", name, version), nil
		}
		if fileExists(target+linkSymlinkBackupSuffix) || fileExists(target+linkBackupSuffix) {
			return "", fmt.Errorf("%s is no longer the WB-created symlink; preserve its backups and inspect before undo", target)
		}
		return "", clearStagedLink(stage, marker)
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect %s: %w", target, err)
	}

	// A recorded symlink target is restored first: on pnpm this is the normal
	// case, and it is the one that used to be lost entirely.
	symlinkBackup := target + linkSymlinkBackupSuffix
	if contents, readErr := os.ReadFile(symlinkBackup); readErr == nil {
		original := strings.TrimSpace(string(contents))
		if original == "" {
			return "", fmt.Errorf("the recorded link target for %s is empty; restore it by re-installing", packageName)
		}
		if err := os.Symlink(original, target); err != nil {
			return "", fmt.Errorf("restore the original link for %s: %w", packageName, err)
		}
		if _, statErr := os.Stat(target); statErr != nil {
			return "", fmt.Errorf("restored the original link for %s but it is dangling — %s no longer resolves; re-install to recover the published package: %w", packageName, original, statErr)
		}
		if err := os.Remove(symlinkBackup); err != nil {
			return "", fmt.Errorf("clear the link record for %s: %w", packageName, err)
		}
		return "", clearStagedLink(stage, marker)
	} else if !os.IsNotExist(readErr) {
		return "", fmt.Errorf("read the link record for %s: %w", packageName, readErr)
	}
	directoryBackup := target + linkBackupSuffix
	if fileExists(directoryBackup) {
		if err := os.Rename(directoryBackup, target); err != nil {
			return "", fmt.Errorf("restore the installed %s: %w", packageName, err)
		}
	}
	return "", clearStagedLink(stage, marker)
}

// wbStageSuffix names the directory Link stages a built package into beside
// the consumer's installed peer context. A resolved path still carrying it is
// another WB stage, never a published copy — see nodeLinkStagePath.
const wbStageSuffix = ".wb-locallink-stage"

// resolvePublishedPackage reports the package name and version recorded in
// package.json at resolvedPath, when resolvedPath is not itself a WB stage.
// It covers both shapes a package manager leaves once it has genuinely
// replaced the link: a pnpm virtual-store path
// (node_modules/.pnpm/<name>@<version>/node_modules/<name>) and a plain
// installed directory. A miss — no package.json, an unreadable or malformed
// one, an empty version, or resolvedPath still being a WB stage — returns
// ok=false so the caller keeps refusing rather than guessing.
func resolvePublishedPackage(resolvedPath, packageName string) (name, version string, ok bool) {
	if strings.HasSuffix(filepath.Base(resolvedPath), wbStageSuffix) {
		return "", "", false
	}
	data, err := os.ReadFile(filepath.Join(resolvedPath, "package.json"))
	if err != nil {
		return "", "", false
	}
	var manifest struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", "", false
	}
	version = strings.TrimSpace(manifest.Version)
	if version == "" {
		return "", "", false
	}
	name = strings.TrimSpace(manifest.Name)
	if name == "" {
		name = packageName
	}
	return name, version, true
}

type siblingStage struct {
	name  string
	stage string
}

type siblingEdge struct {
	from string
	to   string
}

// LinkSiblings makes staged packages resolve their declared runtime siblings
// to the corresponding staged identities. The edges live inside the untracked
// stage directories, so removing any package stage removes its edges too and
// leaves the installed pnpm topology available for exact undo.
func (node ExecNode) LinkSiblings(ctx context.Context, consumerDir string, packageNames []string) error {
	stages := make(map[string]siblingStage, len(packageNames))
	for _, packageName := range packageNames {
		if _, already := stages[packageName]; already {
			continue
		}
		target := filepath.Join(consumerDir, "node_modules", filepath.FromSlash(packageName))
		marker := linkAppliedMarkerPath(consumerDir, packageName)
		contents, err := os.ReadFile(marker)
		if err != nil {
			return fmt.Errorf("read staged sibling marker for %s: %w", packageName, err)
		}
		stage, err := validateStagedLinkPath(consumerDir, target, strings.TrimSpace(string(contents)))
		if err != nil {
			return fmt.Errorf("validate staged sibling %s: %w", packageName, err)
		}
		stages[packageName] = siblingStage{name: packageName, stage: stage}
	}

	var edges []siblingEdge
	for _, current := range stages {
		manifest := filepath.Join(current.stage, "package.json")
		contents, err := os.ReadFile(manifest)
		if err != nil {
			return fmt.Errorf("read staged package manifest for %s: %w", current.name, err)
		}
		var parsed struct {
			Dependencies         map[string]string `json:"dependencies"`
			PeerDependencies     map[string]string `json:"peerDependencies"`
			OptionalDependencies map[string]string `json:"optionalDependencies"`
		}
		if err := json.Unmarshal(contents, &parsed); err != nil {
			return fmt.Errorf("parse staged package manifest for %s: %w", current.name, err)
		}
		for dependency := range parsed.Dependencies {
			if _, staged := stages[dependency]; staged {
				edges = append(edges, siblingEdge{from: current.name, to: dependency})
			}
		}
		for dependency := range parsed.PeerDependencies {
			if _, staged := stages[dependency]; staged {
				edges = append(edges, siblingEdge{from: current.name, to: dependency})
			}
		}
		for dependency := range parsed.OptionalDependencies {
			if _, staged := stages[dependency]; staged {
				edges = append(edges, siblingEdge{from: current.name, to: dependency})
			}
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].from != edges[j].from {
			return edges[i].from < edges[j].from
		}
		return edges[i].to < edges[j].to
	})

	// Preflight every edge before creating one. This makes a partial failure
	// retryable without leaving a half-reconciled sibling graph.
	for _, edge := range edges {
		from, to := stages[edge.from], stages[edge.to]
		link := filepath.Join(from.stage, "node_modules", filepath.FromSlash(edge.to))
		if info, err := os.Lstat(link); err == nil {
			if info.Mode()&os.ModeSymlink == 0 {
				return fmt.Errorf("refuse to replace existing staged sibling path %s", link)
			}
			actual, readErr := os.Readlink(link)
			if readErr != nil {
				return fmt.Errorf("read existing staged sibling path %s: %w", link, readErr)
			}
			if !samePath(filepath.Dir(link), actual, to.stage) {
				return fmt.Errorf("staged sibling path %s points to %q, want %s", link, actual, to.stage)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect staged sibling path %s: %w", link, err)
		}
	}

	var created []string
	for _, edge := range edges {
		from, to := stages[edge.from], stages[edge.to]
		link := filepath.Join(from.stage, "node_modules", filepath.FromSlash(edge.to))
		if fileExists(link) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			removeSiblingEdges(created)
			return fmt.Errorf("create staged sibling parent for %s: %w", edge.to, err)
		}
		relative, err := filepath.Rel(filepath.Dir(link), to.stage)
		if err != nil {
			removeSiblingEdges(created)
			return fmt.Errorf("resolve staged sibling %s from %s: %w", edge.to, edge.from, err)
		}
		if err := os.Symlink(relative, link); err != nil {
			removeSiblingEdges(created)
			return fmt.Errorf("link staged sibling %s into %s: %w", edge.to, edge.from, err)
		}
		created = append(created, link)
	}
	if err := node.verifyRuntimeGraph(ctx, consumerDir, packageNames); err != nil {
		return err
	}
	return nil
}

type runtimeGraphProbe struct {
	Visited    int                    `json:"visited"`
	Mismatches []runtimeGraphMismatch `json:"mismatches"`
	Error      string                 `json:"error"`
}

type runtimeGraphMismatch struct {
	From         string `json:"from"`
	Dependency   string `json:"dependency"`
	Resolver     string `json:"resolver"`
	ResolvedRoot string `json:"resolved_root"`
	ExpectedRoot string `json:"expected_root"`
}

// verifyRuntimeGraph asks Node's own CommonJS and ESM resolvers to walk the
// consumer's installed runtime graph. Checking only the application root and
// packages staged by WB misses pnpm peer contexts: an already-published package
// can keep resolving a singleton such as @angular/core or @sneat/core to its
// published physical package while the application resolves WB's staged one.
// Bundlers follow that physical edge and emit two token identities.
func (node ExecNode) verifyRuntimeGraph(ctx context.Context, consumerDir string, packageNames []string) error {
	linked, err := json.Marshal(dedupe(packageNames))
	if err != nil {
		return fmt.Errorf("encode linked npm identities for runtime graph verification: %w", err)
	}
	if _, err := exec.LookPath("node"); err != nil {
		return fmt.Errorf("node is required to verify the linked npm runtime graph: %w", err)
	}
	timeout := node.Timeout
	if timeout <= 0 {
		timeout = defaultCommandTimeout
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(bounded, "node", "--experimental-import-meta-resolve", "--input-type=module", "-")
	command.Dir = consumerDir
	command.Env = append(console.Env(), "WB_LINKED_PACKAGES="+string(linked))
	command.Stdin = strings.NewReader(nodeRuntimeGraphProbeScript)
	output, runErr := command.CombinedOutput()
	if runErr != nil {
		if bounded.Err() != nil && ctx.Err() == nil {
			return fmt.Errorf("verify linked npm runtime graph timed out after %s: %s", timeout, strings.TrimSpace(string(output)))
		}
		return fmt.Errorf("verify linked npm runtime graph with node: %w: %s", runErr, strings.TrimSpace(string(output)))
	}
	var probe runtimeGraphProbe
	if err := json.Unmarshal(output, &probe); err != nil {
		return fmt.Errorf("parse linked npm runtime graph result: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if probe.Error != "" {
		return fmt.Errorf("verify linked npm runtime graph: %s", probe.Error)
	}
	if len(probe.Mismatches) == 0 {
		return nil
	}
	type mismatchSummary struct {
		mismatch  runtimeGraphMismatch
		resolvers []string
	}
	byEdge := make(map[string]*mismatchSummary, len(probe.Mismatches))
	for _, mismatch := range probe.Mismatches {
		key := strings.Join([]string{mismatch.From, mismatch.Dependency, mismatch.ResolvedRoot, mismatch.ExpectedRoot}, "\x00")
		summary := byEdge[key]
		if summary == nil {
			summary = &mismatchSummary{mismatch: mismatch}
			byEdge[key] = summary
		}
		if !slices.Contains(summary.resolvers, mismatch.Resolver) {
			summary.resolvers = append(summary.resolvers, mismatch.Resolver)
		}
	}
	keys := make([]string, 0, len(byEdge))
	for key := range byEdge {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	const maximumReportedRuntimeSplits = 8
	details := make([]string, 0, min(len(keys), maximumReportedRuntimeSplits)+1)
	for _, key := range keys[:min(len(keys), maximumReportedRuntimeSplits)] {
		summary := byEdge[key]
		sort.Strings(summary.resolvers)
		details = append(details, fmt.Sprintf("%s resolves %s through %s to %s, want %s",
			summary.mismatch.From, summary.mismatch.Dependency, strings.Join(summary.resolvers, "+"),
			summary.mismatch.ResolvedRoot, summary.mismatch.ExpectedRoot))
	}
	if remaining := len(keys) - len(details); remaining > 0 {
		details = append(details, fmt.Sprintf("and %d more split edges", remaining))
	}
	return fmt.Errorf("linked npm runtime graph contains split package identities after checking %d installed packages: %s",
		probe.Visited, strings.Join(details, "; "))
}

const nodeRuntimeGraphProbeScript = `
import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";
import { fileURLToPath, pathToFileURL } from "node:url";

const result = { visited: 0, mismatches: [], error: "" };
try {
  const linked = new Set(JSON.parse(process.env.WB_LINKED_PACKAGES || "[]"));
  const workspace = fs.realpathSync.native(process.cwd());
  const rootManifest = path.join(workspace, "package.json");
  const expected = new Map();

  function manifestAt(manifest) {
    return JSON.parse(fs.readFileSync(manifest, "utf8"));
  }
  function packageRoot(entry, packageName) {
    if (!entry || entry.startsWith("node:")) return "";
    let current = entry.startsWith("file:") ? fileURLToPath(entry) : entry;
    current = fs.realpathSync.native(current);
    if (!fs.statSync(current).isDirectory()) current = path.dirname(current);
    for (;;) {
      const manifest = path.join(current, "package.json");
      if (fs.existsSync(manifest)) {
        try {
          if (manifestAt(manifest).name === packageName) return fs.realpathSync.native(current);
        } catch {}
      }
      const parent = path.dirname(current);
      if (parent === current) return "";
      current = parent;
    }
  }
  function resolveFrom(manifest, packageName) {
    const resolutions = [];
    const requireFrom = createRequire(manifest);
	for (const specifier of [packageName, packageName + "/package.json"]) {
	  try {
		const root = packageRoot(requireFrom.resolve(specifier), packageName);
		if (root) { resolutions.push({ resolver: "createRequire.resolve", root }); break; }
	  } catch {}
	}
	for (const specifier of [packageName, packageName + "/package.json"]) {
	  try {
		const root = packageRoot(import.meta.resolve(specifier, pathToFileURL(manifest).href), packageName);
		if (root) { resolutions.push({ resolver: "import.meta.resolve", root }); break; }
	  } catch {}
	}
    return resolutions.filter((item) => item.root);
  }
  function displayPath(value) {
    const relative = path.relative(workspace, value);
    return relative && !relative.startsWith("..") ? relative : value;
  }

  for (const packageName of linked) {
    const roots = resolveFrom(rootManifest, packageName);
    if (roots.length === 0) throw new Error("consumer root cannot resolve linked package " + packageName);
    expected.set(packageName, roots[0].root);
    for (const resolution of roots) {
      if (resolution.root !== roots[0].root) {
        result.mismatches.push({
          from: "consumer root", dependency: packageName, resolver: resolution.resolver,
          resolved_root: displayPath(resolution.root), expected_root: displayPath(roots[0].root),
        });
      }
    }
  }

  const queue = [rootManifest];
  const visited = new Set();
  while (queue.length > 0) {
    const manifest = fs.realpathSync.native(queue.shift());
    if (visited.has(manifest)) continue;
    visited.add(manifest);
    result.visited += 1;
    const pkg = manifestAt(manifest);
    const label = pkg.name ? pkg.name + (pkg.version ? "@" + pkg.version : "") : displayPath(path.dirname(manifest));
    const dependencies = {
      ...(pkg.dependencies || {}),
      ...(pkg.optionalDependencies || {}),
      ...(pkg.peerDependencies || {}),
    };
    for (const packageName of Object.keys(dependencies).sort()) {
      const resolutions = resolveFrom(manifest, packageName);
      if (linked.has(packageName)) {
        const want = expected.get(packageName);
        for (const resolution of resolutions) {
          if (resolution.root !== want) {
            result.mismatches.push({
              from: label, dependency: packageName, resolver: resolution.resolver,
              resolved_root: displayPath(resolution.root), expected_root: displayPath(want),
            });
          }
        }
      }
      if (resolutions.length > 0) {
        const dependencyManifest = path.join(resolutions[0].root, "package.json");
        if (fs.existsSync(dependencyManifest)) queue.push(dependencyManifest);
      }
    }
  }
} catch (error) {
  result.error = error instanceof Error ? error.message : String(error);
}
process.stdout.write(JSON.stringify(result));
`

func samePath(base, actual, want string) bool {
	if !filepath.IsAbs(actual) {
		actual = filepath.Join(base, actual)
	}
	return filepath.Clean(actual) == filepath.Clean(want)
}

func removeSiblingEdges(paths []string) {
	for _, path := range paths {
		_ = os.Remove(path)
	}
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

// clearSupersededLink clears everything Link ever wrote for this package —
// the stage, the marker, and any recovery backup — without touching target
// itself. The backups exist to let a genuinely dangerous state be inspected
// before undo; once the package manager has already replaced the link with
// a published copy, they name a restore that would only overwrite what is
// correctly installed, so they are stale bookkeeping, not evidence to keep.
func clearSupersededLink(stage, marker, target string) error {
	for _, backup := range []string{target + linkSymlinkBackupSuffix, target + linkBackupSuffix} {
		if err := os.Remove(backup); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return clearStagedLink(stage, marker)
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
