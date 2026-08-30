package deps

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/mod/semver"
)

// npmAdapter updates dependency references across every package.json in a
// repository (dependencies, devDependencies, peerDependencies,
// optionalDependencies, including pnpm/npm workspace members) and, where
// present, the pnpm-workspace.yaml `overrides:`/`catalog:`/`catalogs:`
// blocks pnpm 11 reads instead of the legacy `pnpm.overrides` field in
// package.json. After writing an exact version it regenerates every affected
// lockfile so a frozen CI install does not fail with
// ERR_PNPM_LOCKFILE_CONFIG_MISMATCH, then verifies the regeneration was
// actually sufficient with a frozen probe before reporting success.
type npmAdapter struct{}

func (npmAdapter) inspect(ctx context.Context, repositoryDir, base string, target Target, options Options) ([]Decision, error) {
	output, _, err := runCommand(ctx, options.Timeout, options.Retry, repositoryDir, "git", "ls-tree", "-r", "--name-only", base)
	if err != nil {
		return nil, err
	}
	var decisions []Decision
	for _, name := range strings.Split(strings.TrimSpace(output), "\n") {
		if ignoredManifestPath(name) {
			continue
		}
		switch filepath.Base(name) {
		case "package.json":
			contents, _, err := runCommand(ctx, options.Timeout, options.Retry, repositoryDir, "git", "show", base+":"+name)
			if err != nil {
				return nil, err
			}
			for _, ref := range scanNpmPackageJSONRefs([]byte(contents)) {
				if ref.Key != target.Dependency {
					continue
				}
				decision, blocked := npmDecisionFor(name, ref.Field+"."+ref.Key, ref.Value, target, options.AllowDowngrade, "planned", "existing npm reference will be set to the exact target version")
				decisions = append(decisions, decision)
				if blocked {
					sortDecisions(decisions)
					return decisions, fmt.Errorf("%s: %s", name, decision.Reason)
				}
			}
		case "pnpm-workspace.yaml":
			contents, _, err := runCommand(ctx, options.Timeout, options.Retry, repositoryDir, "git", "show", base+":"+name)
			if err != nil {
				return nil, err
			}
			for _, ref := range scanPnpmWorkspaceRefs([]byte(contents)) {
				if ref.Key != target.Dependency {
					continue
				}
				decision, blocked := npmDecisionFor(name, workspaceSelector(ref), ref.Value, target, options.AllowDowngrade, "planned", "existing pnpm workspace override will be set to the exact target version")
				decisions = append(decisions, decision)
				if blocked {
					sortDecisions(decisions)
					return decisions, fmt.Errorf("%s: %s", name, decision.Reason)
				}
			}
		}
	}
	sortDecisions(decisions)
	return decisions, nil
}

func (npmAdapter) apply(ctx context.Context, worktree string, target Target, options Options) ([]Decision, error) {
	packageManifests, workspaceManifests, err := npmManifestFiles(worktree)
	if err != nil {
		return nil, err
	}

	type pendingPackageJSON struct {
		relative string
		contents []byte
		refs     []npmPackageJSONRef
	}
	type pendingWorkspace struct {
		relative string
		contents []byte
		refs     []pnpmWorkspaceRef
	}
	var decisions []Decision
	var pendingPackages []pendingPackageJSON
	var pendingWorkspaces []pendingWorkspace
	changedFiles := map[string]bool{}

	for _, relative := range packageManifests {
		contents, err := os.ReadFile(filepath.Join(worktree, filepath.FromSlash(relative)))
		if err != nil {
			return decisions, err
		}
		var matches []npmPackageJSONRef
		for _, ref := range scanNpmPackageJSONRefs(contents) {
			if ref.Key != target.Dependency {
				continue
			}
			decision, blocked := npmDecisionFor(relative, ref.Field+"."+ref.Key, ref.Value, target, options.AllowDowngrade, "updated", "npm tooling was not needed; the exact target version is a literal manifest edit")
			decisions = append(decisions, decision)
			if blocked {
				sortDecisions(decisions)
				return decisions, fmt.Errorf("%s: %s", relative, decision.Reason)
			}
			matches = append(matches, ref)
		}
		if len(matches) > 0 {
			pendingPackages = append(pendingPackages, pendingPackageJSON{relative: relative, contents: contents, refs: matches})
		}
	}
	for _, relative := range workspaceManifests {
		contents, err := os.ReadFile(filepath.Join(worktree, filepath.FromSlash(relative)))
		if err != nil {
			return decisions, err
		}
		var matches []pnpmWorkspaceRef
		for _, ref := range scanPnpmWorkspaceRefs(contents) {
			if ref.Key != target.Dependency {
				continue
			}
			decision, blocked := npmDecisionFor(relative, workspaceSelector(ref), ref.Value, target, options.AllowDowngrade, "updated", "pnpm workspace override set to the exact target version")
			decisions = append(decisions, decision)
			if blocked {
				sortDecisions(decisions)
				return decisions, fmt.Errorf("%s: %s", relative, decision.Reason)
			}
			matches = append(matches, ref)
		}
		if len(matches) > 0 {
			pendingWorkspaces = append(pendingWorkspaces, pendingWorkspace{relative: relative, contents: contents, refs: matches})
		}
	}

	// Nothing referenced the dependency at all: mirror the Go and GitHub
	// Actions adapters, which return an empty decision set rather than an
	// error when a repository simply does not use it.
	if len(pendingPackages) == 0 && len(pendingWorkspaces) == 0 {
		return decisions, nil
	}

	for _, pending := range pendingPackages {
		updated, applied, err := applyNpmPackageJSONOverride(pending.contents, target.Dependency, target.Version)
		if err != nil {
			return decisions, fmt.Errorf("%s: %w", pending.relative, err)
		}
		if len(applied) != len(pending.refs) {
			return decisions, fmt.Errorf("%s: expected to update %d reference(s), matched %d", pending.relative, len(pending.refs), len(applied))
		}
		if err := writeAtomic(filepath.Join(worktree, filepath.FromSlash(pending.relative)), updated, 0o644); err != nil {
			return decisions, err
		}
		changedFiles[pending.relative] = true
	}
	for _, pending := range pendingWorkspaces {
		updated, applied, err := applyPnpmWorkspaceOverride(pending.contents, target.Dependency, target.Version)
		if err != nil {
			return decisions, fmt.Errorf("%s: %w", pending.relative, err)
		}
		if len(applied) != len(pending.refs) {
			return decisions, fmt.Errorf("%s: expected to update %d reference(s), matched %d", pending.relative, len(pending.refs), len(applied))
		}
		if err := writeAtomic(filepath.Join(worktree, filepath.FromSlash(pending.relative)), updated, 0o644); err != nil {
			return decisions, err
		}
		changedFiles[pending.relative] = true
	}

	sortDecisions(decisions)
	lockfileDecisions, lockfileErr := regenerateAffectedLockfiles(ctx, worktree, changedFiles, target, options)
	decisions = append(decisions, lockfileDecisions...)
	return decisions, lockfileErr
}

// npmDecisionFor builds one Decision for an existing npm-ecosystem reference
// and reports whether the target is a blocked downgrade. changedAction and
// changedReason describe the outcome when the reference has to move, and
// differ between inspect (a plan, not yet written) and apply (already
// written by the time this decision is returned).
func npmDecisionFor(file, selector, before string, target Target, allowDowngrade bool, changedAction, changedReason string) (Decision, bool) {
	decision := Decision{
		Dependency: target.Dependency, Ecosystem: EcosystemNPM, File: file, Selector: selector, BeforeRef: before, BeforeVersion: before,
		TargetVersion: target.Version, ResolvedRef: target.Version, AfterRef: target.Version, AfterVersion: target.Version,
	}
	if comparableNpmDowngrade(before, target.Version) && !allowDowngrade {
		decision.Action = "blocked_downgrade"
		decision.Reason = fmt.Sprintf("target %s is lower than observed version %s; use --allow-downgrade", target.Version, before)
		return decision, true
	}
	if before == target.Version {
		decision.Action = "unchanged"
		decision.Reason = "existing reference already declares the exact target version"
		return decision, false
	}
	decision.Action = changedAction
	decision.Reason = changedReason
	return decision, false
}

func workspaceSelector(ref pnpmWorkspaceRef) string {
	if ref.Section == "catalogs" {
		return "catalogs." + ref.CatalogName + "." + ref.Key
	}
	return ref.Section + "." + ref.Key
}

// npmManifestFiles walks a working tree and returns every package.json and
// pnpm-workspace.yaml path (relative, slash-separated, sorted), skipping
// node_modules and other paths that must never be treated as source. A repo
// can contain more than one independent pnpm workspace root (for example
// sneat-apps' landings/ subtree), so this is a full-tree walk rather than a
// single lookup at the repository root.
func npmManifestFiles(root string) (packageManifests, workspaceManifests []string, err error) {
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "vendor":
				if path != root {
					return filepath.SkipDir
				}
			}
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		relative = filepath.ToSlash(relative)
		if ignoredManifestPath(relative) {
			return nil
		}
		switch entry.Name() {
		case "package.json":
			packageManifests = append(packageManifests, relative)
		case "pnpm-workspace.yaml":
			workspaceManifests = append(workspaceManifests, relative)
		}
		return nil
	})
	sort.Strings(packageManifests)
	sort.Strings(workspaceManifests)
	return packageManifests, workspaceManifests, err
}

// npmLockfileKind identifies which tool owns a lockfile and therefore which
// command regenerates it.
type npmLockfileKind string

const (
	npmLockfilePnpm npmLockfileKind = "pnpm-lock.yaml"
	npmLockfileNpm  npmLockfileKind = "package-lock.json"
	npmLockfileYarn npmLockfileKind = "yarn.lock"
)

// npmLockfileDirectories walks a working tree for every lockfile and returns
// the directories that own one (relative, slash-separated, "" for the
// repository root), each with the lockfile kinds found there.
func npmLockfileDirectories(root string) (map[string][]npmLockfileKind, error) {
	directories := map[string][]npmLockfileKind{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "vendor":
				if path != root {
					return filepath.SkipDir
				}
			}
			return nil
		}
		var kind npmLockfileKind
		switch entry.Name() {
		case "pnpm-lock.yaml":
			kind = npmLockfilePnpm
		case "package-lock.json":
			kind = npmLockfileNpm
		case "yarn.lock":
			kind = npmLockfileYarn
		default:
			return nil
		}
		relativeDir, relErr := filepath.Rel(root, filepath.Dir(path))
		if relErr != nil {
			return relErr
		}
		relativeDir = filepath.ToSlash(relativeDir)
		if relativeDir == "." {
			relativeDir = ""
		}
		directories[relativeDir] = append(directories[relativeDir], kind)
		return nil
	})
	return directories, err
}

// npmLockfileDirForFile returns the lockfile-owning directory that governs
// relativeFile: the deepest directory in lockfileDirs that is relativeFile's
// own directory or an ancestor of it. A repository can own more than one
// independent lockfile scope (e.g. sneat-apps has both a root pnpm-lock.yaml
// and landings/pnpm-lock.yaml), so this always picks the most specific one
// rather than always regenerating the root lockfile.
func npmLockfileDirForFile(lockfileDirs []string, relativeFile string) (string, bool) {
	fileDir := filepath.ToSlash(filepath.Dir(relativeFile))
	if fileDir == "." {
		fileDir = ""
	}
	bestLength := -1
	var best string
	found := false
	for _, dir := range lockfileDirs {
		if dir != "" && dir != fileDir && !strings.HasPrefix(fileDir, dir+"/") {
			continue
		}
		if len(dir) > bestLength {
			bestLength, best, found = len(dir), dir, true
		}
	}
	return best, found
}

// regenerateAffectedLockfiles regenerates and verifies every lockfile scope
// that owns at least one file this apply() call changed. Skipping this step
// is the exact failure mode the fleet has hit before: an overrides or
// package.json edit lands, but the committed lockfile's recorded config
// snapshot no longer matches, and CI's frozen install fails with
// ERR_PNPM_LOCKFILE_CONFIG_MISMATCH instead of silently drifting.
func regenerateAffectedLockfiles(ctx context.Context, worktree string, changedFiles map[string]bool, target Target, options Options) ([]Decision, error) {
	if len(changedFiles) == 0 {
		return nil, nil
	}
	lockfilesByDir, err := npmLockfileDirectories(worktree)
	if err != nil {
		return nil, err
	}
	if len(lockfilesByDir) == 0 {
		return nil, nil
	}
	lockfileDirs := make([]string, 0, len(lockfilesByDir))
	for dir := range lockfilesByDir {
		lockfileDirs = append(lockfileDirs, dir)
	}
	sort.Strings(lockfileDirs)

	affected := map[string]bool{}
	for file := range changedFiles {
		if dir, ok := npmLockfileDirForFile(lockfileDirs, file); ok {
			affected[dir] = true
		}
	}
	affectedDirs := make([]string, 0, len(affected))
	for dir := range affected {
		affectedDirs = append(affectedDirs, dir)
	}
	sort.Strings(affectedDirs)

	var decisions []Decision
	var regenErrors []error
	for _, dir := range affectedDirs {
		absolute := filepath.Join(worktree, filepath.FromSlash(dir))
		for _, kind := range lockfilesByDir[dir] {
			lockfilePath := path.Join(dir, string(kind))
			decision := Decision{Dependency: target.Dependency, File: lockfilePath, TargetVersion: target.Version}
			switch kind {
			case npmLockfilePnpm:
				if _, _, err := runCommand(ctx, options.Timeout, options.Retry, absolute, "pnpm", "install", "--lockfile-only"); err != nil {
					decision.Action = "lockfile_regeneration_failed"
					decision.Reason = err.Error()
					decisions = append(decisions, decision)
					regenErrors = append(regenErrors, fmt.Errorf("regenerate %s: %w", lockfilePath, err))
					continue
				}
				if _, _, err := runCommand(ctx, options.Timeout, options.Retry, absolute, "pnpm", "install", "--frozen-lockfile", "--lockfile-only"); err != nil {
					decision.Action = "lockfile_verification_failed"
					decision.Reason = "regenerated lockfile did not pass a frozen-lockfile probe: " + err.Error()
					decisions = append(decisions, decision)
					regenErrors = append(regenErrors, fmt.Errorf("verify %s: %w", lockfilePath, err))
					continue
				}
				decision.Action = "lockfile_regenerated"
				decision.Reason = "pnpm install --lockfile-only regenerated the lockfile and a frozen-lockfile probe confirmed it is consistent"
			case npmLockfileNpm:
				if _, _, err := runCommand(ctx, options.Timeout, options.Retry, absolute, "npm", "install", "--package-lock-only"); err != nil {
					decision.Action = "lockfile_regeneration_failed"
					decision.Reason = err.Error()
					decisions = append(decisions, decision)
					regenErrors = append(regenErrors, fmt.Errorf("regenerate %s: %w", lockfilePath, err))
					continue
				}
				decision.Action = "lockfile_regenerated"
				decision.Reason = "npm install --package-lock-only regenerated the lockfile"
			case npmLockfileYarn:
				decision.Action = "lockfile_skipped"
				decision.Reason = "yarn.lock is not regenerated automatically; run `yarn install` in " + lockfilePath + " before merging"
			}
			decisions = append(decisions, decision)
		}
	}
	sortDecisions(decisions)
	return decisions, errors.Join(regenErrors...)
}

// universalSemverValid and universalSemverCompare accept both Go's
// "v"-prefixed module semver and npm's unprefixed semver by normalizing to
// the "v" form golang.org/x/mod/semver requires. deps bump only ever compares
// and merges exact published versions (never ranges) for either ecosystem,
// so this normalization is safe; the "v" is never written back to a manifest,
// only used transiently for comparison.
func universalSemverValid(version string) bool {
	return semver.IsValid(normalizeSemverPrefix(version))
}

func universalSemverCompare(a, b string) int {
	return semver.Compare(normalizeSemverPrefix(a), normalizeSemverPrefix(b))
}

func normalizeSemverPrefix(version string) string {
	if version == "" || strings.HasPrefix(version, "v") {
		return version
	}
	return "v" + version
}

// comparableNpmDowngrade reuses the shared exact-semver downgrade check with
// npm versions normalized to the "v"-prefixed form it expects. It reports
// false (never a blocked downgrade) for ranges, workspace/catalog protocol
// specifiers, and other non-exact values, exactly like comparableDowngrade
// already does for any invalid semver string.
func comparableNpmDowngrade(before, target string) bool {
	return comparableDowngrade(normalizeSemverPrefix(before), normalizeSemverPrefix(target))
}

// npmPackageNamePattern is npm's own package name shape: an optional
// "@scope/" prefix, then lowercase letters, digits, dots, hyphens, and
// underscores. It intentionally does not reach the registry.
var npmPackageNamePattern = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*$`)

// validateNpmPackageName performs the same light structural validation for a
// `deps bump npm --changed` event that ParseTarget already relies on being
// well-formed for `deps set npm`.
// ValidateNpmPackageName validates one exact npm package identity. Commands
// outside the dependency adapter (for example workflow-owned publication)
// must use this same validator before contacting a registry or GitHub.
func ValidateNpmPackageName(name string) error {
	if name == "" || len(name) > 214 {
		return fmt.Errorf("npm package name %q must be 1-214 characters", name)
	}
	if !npmPackageNamePattern.MatchString(name) {
		return fmt.Errorf("npm package name %q must be lowercase and optionally scoped as @scope/name", name)
	}
	return nil
}

func validateNpmPackageName(name string) error {
	return ValidateNpmPackageName(name)
}
