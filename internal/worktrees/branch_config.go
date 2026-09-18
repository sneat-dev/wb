package worktrees

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sneat-dev/wb/internal/gitremote"
	"github.com/sneat-dev/wb/internal/repopath"
	"github.com/sneat-dev/wb/internal/wbhome"
	"gopkg.in/yaml.v3"
)

const (
	branchConfigVersion = 1
	maxBranchConfigSize = 64 << 10
)

// Store modes select where a new checkout physically lands. The store mode is
// machine-local user policy; a repository-tracked file may not select it.
const (
	// StoreModeCentral places every checkout of one task together below the
	// projects-root store: <root>/.worktrees/<task>/<host>/<org>/<repo>.
	StoreModeCentral = "central"
	// StoreModeRepositoryLocal places the checkout inside its own canonical
	// clone: <canonical>/.worktrees/<task>.
	StoreModeRepositoryLocal = "repository-local"
)

// branchConfigFile is layered user then repository. A nil prefix leaves the
// lower layer intact; an explicitly empty value deliberately disables it.
type branchConfigFile struct {
	Version   int `yaml:"version"`
	Worktrees struct {
		BranchPrefix *string `yaml:"branch_prefix"`
		// Store selects the machine-local store mode. It is intentionally a
		// user-only setting: a repository-tracked .wb/worktrees.yaml may not
		// select or override it. When unset, StoreModeCentral applies.
		Store *string `yaml:"store"`
		// Root is intentionally a user-only setting. In central mode it
		// selects the exact store directory containing
		// <task>/<host>/<org>/<repository> checkouts; when unset the
		// projects-root store <root>/.worktrees is used. It is not meaningful
		// in repository-local mode, which has no configurable store root.
		Root *string `yaml:"root"`
	} `yaml:"worktrees"`
}

// worktreePlacement is the physical placement selected for one canonical
// repository. WB_HOME remains the authority for claims, locks, and receipts;
// placement only answers where Git writes the linked checkout.
type worktreePlacement struct {
	Root  string
	Local bool
	// Relative is the canonical clone's root-relative address
	// (<host>/<org>/<repository>, or the legacy <org>/<repository> for a clone
	// with no literal host level). It is what puts the host level into the
	// central store path without the caller having to know the host.
	Relative string
}

// WorktreePlacement is the public, resolved physical placement for one
// canonical repository. It deliberately does not expose WB_HOME: that is
// lifecycle authority, not a worktree checkout location.
type WorktreePlacement struct {
	Root            string
	RepositoryLocal bool
	// relative carries the canonical clone's root-relative address from the
	// resolver to Path. It stays unexported because a placement is only ever
	// produced by ResolveUserWorktreePlacement / ResolveWorktreePlacement.
	relative string
}

// Path returns the one physical checkout path for task and repository. In
// central mode the path embeds the canonical clone's literal host level, which
// the resolver took from the clone's on-disk placement or, for a clone still at
// the legacy two-level path, from its origin remote. A clone whose origin names
// no forge keeps the two-level <org>/<repository> suffix so no checkout has to
// move to stay addressable.
func (placement WorktreePlacement) Path(task, repository string) (string, error) {
	address, err := splitRepositoryAddress(repository)
	if err != nil {
		return "", err
	}
	if !validSafeSegment(task) {
		return "", fmt.Errorf("invalid worktree task %q", task)
	}
	if placement.RepositoryLocal {
		return filepath.Join(placement.Root, task), nil
	}
	relative := strings.Trim(strings.TrimSpace(placement.relative), "/")
	if relative == "" {
		relative = address.Relative()
	}
	if !validCloneRelative(relative) {
		return "", fmt.Errorf("canonical clone address %q is not a safe relative path", relative)
	}
	return filepath.Join(placement.Root, task, filepath.FromSlash(relative)), nil
}

// validCloneRelative reports whether a root-relative canonical clone address is
// safe to join below a store root: either {org}/{repository}, or
// {host}/{org}/{repository} with a literal forge hostname first.
func validCloneRelative(relative string) bool {
	parts := strings.Split(relative, "/")
	if len(parts) == 3 {
		if !repopath.IsForgeHost(parts[0]) {
			return false
		}
		parts = parts[1:]
	}
	if len(parts) != 2 {
		return false
	}
	return validSafeSegment(parts[0]) && validRepositorySegment(parts[1])
}

// splitCloneRelative splits a root-relative clone address into its
// slash-separated parent path and repository segment:
// "github.com/acme/app" becomes ("github.com/acme", "app") and a legacy
// "acme/app" becomes ("acme", "app").
func splitCloneRelative(relative string) (parent, repository string) {
	relative = strings.Trim(strings.TrimSpace(relative), "/")
	index := strings.LastIndex(relative, "/")
	if index < 0 {
		return "", relative
	}
	return relative[:index], relative[index+1:]
}

// canonicalRelativeAddress returns the root-relative canonical clone address the
// central store path for one canonical clone must carry. Both paths are resolved
// first, because a projects root reached through a symlinked ancestor (macOS's
// /var) would otherwise make the relative computation meaningless.
func canonicalRelativeAddress(ctx context.Context, projectsRoot, canonicalPath string) (string, error) {
	address, err := canonicalStoreAddress(ctx, projectsRoot, canonicalPath)
	if err != nil {
		return "", err
	}
	return address.Relative(), nil
}

// canonicalStoreAddress returns the address the central store path for one
// canonical clone must carry. The org and repository come from the clone's
// on-disk placement. The host is that placement's own literal host level when it
// has one; a clone that still sits at the legacy <root>/{owner}/{repository}
// path takes the literal forge hostname named by its origin remote instead, so
// its checkouts land below the forge without the clone having to move.
//
// Preferring the on-disk host level when it exists keeps an already-hosted clone
// addressable even if its origin is later rewritten.
func canonicalStoreAddress(ctx context.Context, projectsRoot, canonicalPath string) (repopath.Address, error) {
	address, resolvedCanonical, err := canonicalPathAddress(projectsRoot, canonicalPath)
	if err != nil {
		return repopath.Address{}, err
	}
	if address.Host == "" {
		address.Host = canonicalStoreHost(ctx, resolvedCanonical)
	}
	return address, nil
}

// canonicalPathAddress returns the address a canonical clone's on-disk
// placement spells, with no origin fallback, plus the resolved path it was
// derived from. A legacy clone therefore has an empty host.
func canonicalPathAddress(projectsRoot, canonicalPath string) (repopath.Address, string, error) {
	resolvedRoot, err := resolvePlacementPath(filepath.Clean(projectsRoot))
	if err != nil {
		return repopath.Address{}, "", err
	}
	resolvedCanonical, err := resolvePlacementPath(filepath.Clean(canonicalPath))
	if err != nil {
		return repopath.Address{}, "", err
	}
	host, owner, name, err := canonicalCoordinates(resolvedRoot, resolvedCanonical)
	if err != nil {
		return repopath.Address{}, "", err
	}
	return repopath.Address{Host: host, Org: owner, Repo: name}, resolvedCanonical, nil
}

// canonicalStoreHost returns the literal forge hostname a canonical clone's
// origin remote names, or "" when the remote is local or names no usable forge.
// The configured remote.origin.url is preferred over the expanded one: a
// url.<local>.insteadOf alias is a transport detail, and the configured URL is
// the spelling that names the forge.
func canonicalStoreHost(ctx context.Context, canonicalPath string) string {
	raw, err := git(ctx, canonicalPath, "config", "--get", "remote.origin.url")
	if err != nil || strings.TrimSpace(raw) == "" {
		raw, err = git(ctx, canonicalPath, "remote", "get-url", "origin")
		if err != nil {
			return ""
		}
	}
	parsed, err := gitremote.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	host := parsed.Identity.Host()
	if host == "" || !repopath.IsForgeHost(host) {
		return ""
	}
	return host
}

// ResolveWorktreePlacement resolves user placement policy against an exact
// canonical base revision. It opens no destination and performs no mutation.
func ResolveWorktreePlacement(ctx context.Context, projectsRoot, canonicalPath, baseRevision string) (WorktreePlacement, error) {
	canonical, err := openCanonicalRepository(canonicalPath)
	if err != nil {
		return WorktreePlacement{}, err
	}
	defer canonical.close()
	placement, err := configuredWorktreePlacement(ctx, projectsRoot, canonical, baseRevision)
	if err != nil {
		return WorktreePlacement{}, err
	}
	return WorktreePlacement{Root: placement.Root, RepositoryLocal: placement.Local, relative: placement.Relative}, nil
}

// ResolveUserWorktreePlacement resolves only the machine-local placement
// setting. It never fetches and never mutates, so callers that merely display a
// planned path cannot change a checkout. It reads the canonical clone's
// configured origin remote — a local, read-only Git query — when the clone has
// no on-disk host level, because that is where the central store's literal host
// level comes from. Mutating lifecycle operations must use
// ResolveWorktreePlacement instead, which additionally refuses any
// repository-tracked attempt to select the mode.
func ResolveUserWorktreePlacement(projectsRoot, canonicalPath string) (WorktreePlacement, error) {
	if !filepath.IsAbs(canonicalPath) {
		return WorktreePlacement{}, fmt.Errorf("canonical repository path must be absolute: %s", canonicalPath)
	}
	canonicalPath = filepath.Clean(canonicalPath)
	policy, err := resolveUserStorePolicy(projectsRoot)
	if err != nil {
		return WorktreePlacement{}, err
	}
	return policy.placement(context.Background(), projectsRoot, canonicalPath)
}

// userStorePolicy is the machine-local store mode and, in central mode, the
// store root it selected. It deliberately excludes the canonical clone's own
// address so a caller can snapshot the user-only policy before it prepares any
// destination, then apply that one snapshot to every repository.
type userStorePolicy struct {
	RepositoryLocal bool
	CentralRoot     string
}

// resolveUserStorePolicy reads the machine-local store mode and, in central
// mode, its root. A configured root is resolved (symlinks and ~) at snapshot
// time, so a later filesystem swap of that ancestor is detected by comparing
// against the snapshot rather than silently adopted.
func resolveUserStorePolicy(projectsRoot string) (userStorePolicy, error) {
	global, found, globalPath, err := configuredUserWorktreesConfig()
	if err != nil {
		return userStorePolicy{}, err
	}
	mode := StoreModeCentral
	if found && global.Worktrees.Store != nil {
		mode = *global.Worktrees.Store
	}
	if mode == StoreModeRepositoryLocal {
		if found && global.Worktrees.Root != nil {
			return userStorePolicy{}, fmt.Errorf("worktrees config %s selects store mode %q and also sets root; a repository-local checkout has no configurable store root", globalPath, StoreModeRepositoryLocal)
		}
		return userStorePolicy{RepositoryLocal: true}, nil
	}
	if found && global.Worktrees.Root != nil {
		root, resolveErr := resolveSharedWorktreesRoot(*global.Worktrees.Root)
		if resolveErr != nil {
			return userStorePolicy{}, fmt.Errorf("worktrees config %s root: %w", globalPath, resolveErr)
		}
		return userStorePolicy{CentralRoot: root}, nil
	}
	root, err := wbhome.StoreRoot(projectsRoot)
	if err != nil {
		return userStorePolicy{}, fmt.Errorf("resolve central worktree store for %s: %w", projectsRoot, err)
	}
	return userStorePolicy{CentralRoot: root}, nil
}

// placement applies one snapshotted user policy to a canonical clone. The
// repository-local path is the canonical clone itself, so only the central
// store needs the clone's root-relative address — which is where its literal
// host level comes from.
func (policy userStorePolicy) placement(ctx context.Context, projectsRoot, canonicalPath string) (WorktreePlacement, error) {
	if policy.RepositoryLocal {
		return WorktreePlacement{
			Root:            filepath.Join(canonicalPath, ".worktrees"),
			RepositoryLocal: true,
		}, nil
	}
	relative, err := canonicalRelativeAddress(ctx, projectsRoot, canonicalPath)
	if err != nil {
		return WorktreePlacement{}, err
	}
	return WorktreePlacement{Root: policy.CentralRoot, relative: relative}, nil
}

// branchNamingOptions carries only precedence inputs. Agent provenance is
// deliberately absent: Work Logs, not branch spelling, own that data.
type branchNamingOptions struct {
	Task              string
	ExactBranch       string
	ExactBranchChosen bool
	CLIPrefix         string
	CLIPrefixChosen   bool
	Canonical         *canonicalRepository
	BaseRevision      string
	Base              string
}

func deriveBranchName(ctx context.Context, options branchNamingOptions) (string, error) {
	if options.ExactBranchChosen && options.CLIPrefixChosen {
		return "", fmt.Errorf("--branch and --branch-prefix cannot be used together")
	}
	if options.ExactBranchChosen {
		if options.ExactBranch == "" {
			return "", fmt.Errorf("--branch must not be empty when explicitly provided")
		}
		return validateDerivedBranch(options.ExactBranch, options.Base)
	}
	prefix := ""
	if options.CLIPrefixChosen {
		prefix = options.CLIPrefix
		if prefix != "" && !strings.HasSuffix(prefix, "/") {
			return "", fmt.Errorf("branch prefix %q must end with /", prefix)
		}
		if prefix != "" && !validBranch(context.Background(), prefix+"probe") {
			return "", fmt.Errorf("invalid branch prefix %q", prefix)
		}
	} else {
		configured, err := configuredBranchPrefix(ctx, options.Canonical, options.BaseRevision)
		if err != nil {
			return "", err
		}
		prefix = configured
	}
	return validateDerivedBranch(prefix+options.Task, options.Base)
}

func validateDerivedBranch(branch, base string) (string, error) {
	branch = strings.TrimSpace(branch)
	if !validBranch(context.Background(), branch) {
		return "", fmt.Errorf("invalid feature branch %q", branch)
	}
	if branch == base {
		return "", fmt.Errorf("feature branch must differ from base branch %q", base)
	}
	return branch, nil
}

// configuredBranchPrefix reads the repository layer from the exact fetched
// target-base object. A clean canonical checkout may intentionally be on a
// different branch, so its filesystem is never policy authority here.
func configuredBranchPrefix(ctx context.Context, canonical *canonicalRepository, baseRevision string) (string, error) {
	globalPath, err := defaultWorktreesConfigPath()
	if err != nil {
		return "", err
	}
	prefix := ""
	if config, found, err := loadBranchConfigFile(globalPath); err != nil {
		return "", err
	} else if found && config.Worktrees.BranchPrefix != nil {
		prefix = *config.Worktrees.BranchPrefix
	}
	if canonical == nil || !isGitObjectID(baseRevision) {
		return "", fmt.Errorf("branch policy requires a fetched canonical target-base revision")
	}
	contents, found, err := repositoryBranchConfigAt(ctx, canonical, baseRevision)
	if err != nil {
		return "", err
	}
	if !found {
		return prefix, nil
	}
	config, _, err := parseBranchConfig(".wb/worktrees.yaml at "+baseRevision, contents)
	if err != nil {
		return "", err
	}
	// A repository may spell branches, not place checkouts. Reject its attempt
	// to select the store mode or root here too, so no caller that reads
	// repository policy — branch naming included — can pass one through.
	if violation := repositoryPlacementPolicy(config, baseRevision, globalPath); violation != nil {
		return "", violation
	}
	if config.Worktrees.BranchPrefix != nil {
		prefix = *config.Worktrees.BranchPrefix
	}
	return prefix, nil
}

// repositoryPlacementPolicy reports a repository-tracked attempt to select the
// machine-local store mode or store root. The error names the exact user-only
// configuration path that may select them, because .wb/worktrees.yaml is a
// user-local filename as well and a bare filename would not tell them apart.
func repositoryPlacementPolicy(config branchConfigFile, baseRevision, userConfigPath string) error {
	if config.Worktrees.Store != nil {
		return fmt.Errorf("repository worktrees policy at %s must not set worktrees.store; the store mode is machine-local user policy, set it in %s", baseRevision, userConfigPath)
	}
	if config.Worktrees.Root != nil {
		return fmt.Errorf("repository worktrees policy at %s must not set worktrees.root; the store root is machine-local user policy, set it in %s", baseRevision, userConfigPath)
	}
	return nil
}

// configuredWorktreePlacement reads the machine-local placement setting. A
// repository is allowed to choose branch spelling, but it must never redirect
// filesystem writes on a developer's machine: a repository-tracked
// .wb/worktrees.yaml that sets worktrees.store or worktrees.root is rejected
// with an error naming the machine-local configuration path that may.
func configuredWorktreePlacement(ctx context.Context, projectsRoot string, canonical *canonicalRepository, baseRevision string) (worktreePlacement, error) {
	if canonical == nil || !isGitObjectID(baseRevision) {
		return worktreePlacement{}, fmt.Errorf("worktree placement requires a fetched canonical target-base revision")
	}
	userConfigPath, err := defaultWorktreesConfigPath()
	if err != nil {
		return worktreePlacement{}, err
	}
	contents, found, err := repositoryBranchConfigAt(ctx, canonical, baseRevision)
	if err != nil {
		return worktreePlacement{}, err
	}
	if found {
		config, _, parseErr := parseBranchConfig(".wb/worktrees.yaml at "+baseRevision, contents)
		if parseErr != nil {
			return worktreePlacement{}, parseErr
		}
		if violation := repositoryPlacementPolicy(config, baseRevision, userConfigPath); violation != nil {
			return worktreePlacement{}, violation
		}
	}
	placement, err := ResolveUserWorktreePlacement(projectsRoot, canonical.path)
	if err != nil {
		return worktreePlacement{}, err
	}
	return worktreePlacement{Root: placement.Root, Local: placement.RepositoryLocal, Relative: placement.relative}, nil
}

func configuredUserWorktreesConfig() (branchConfigFile, bool, string, error) {
	globalPath, err := defaultWorktreesConfigPath()
	if err != nil {
		return branchConfigFile{}, false, "", err
	}
	global, found, err := loadBranchConfigFile(globalPath)
	return global, found, globalPath, err
}

func appendConfiguredSharedWorktreesLayout(layouts []wbhome.Layout) ([]wbhome.Layout, error) {
	config, found, path, err := configuredUserWorktreesConfig()
	if err != nil {
		return nil, err
	}
	if !found || config.Worktrees.Root == nil {
		return layouts, nil
	}
	root, err := resolveSharedWorktreesRoot(*config.Worktrees.Root)
	if err != nil {
		return nil, fmt.Errorf("worktrees config %s root: %w", path, err)
	}
	for _, layout := range layouts {
		if filepath.Clean(layout.WorktreesRoot) == filepath.Clean(root) {
			return layouts, nil
		}
	}
	return append(layouts, wbhome.Layout{WorktreesRoot: root}), nil
}

func resolveSharedWorktreesRoot(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("must not be empty")
	}
	if value == "~" || strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home: %w", err)
		}
		if value == "~" {
			value = home
		} else {
			value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
		}
	}
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("must be an absolute path (or begin with ~/)")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	return resolvePlacementPath(filepath.Clean(absolute))
}

func resolvePlacementPath(path string) (string, error) {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return path, nil
	}
	resolvedParent, err := resolvePlacementPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedParent, filepath.Base(path)), nil
}

func defaultWorktreesConfigPath() (string, error) {
	if configHome := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); configHome != "" {
		return filepath.Join(configHome, "wb", "worktrees.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "wb", "worktrees.yaml"), nil
}

func loadBranchConfigFile(path string) (branchConfigFile, bool, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if errors.Is(err, os.ErrNotExist) {
		return branchConfigFile{}, false, nil
	}
	if err != nil {
		return branchConfigFile{}, false, fmt.Errorf("resolve worktrees config %s: %w", path, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return branchConfigFile{}, false, fmt.Errorf("inspect worktrees config %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return branchConfigFile{}, false, fmt.Errorf("worktrees config %s must resolve to a regular file", path)
	}
	if info.Size() > maxBranchConfigSize {
		return branchConfigFile{}, false, fmt.Errorf("worktrees config %s exceeds %d-byte limit", path, maxBranchConfigSize)
	}
	contents, err := os.ReadFile(resolved)
	if err != nil {
		return branchConfigFile{}, false, fmt.Errorf("read worktrees config %s: %w", path, err)
	}
	return parseBranchConfig(path, contents)
}

func repositoryBranchConfigAt(ctx context.Context, canonical *canonicalRepository, revision string) ([]byte, bool, error) {
	// ls-tree has a typed empty result for an absent path. Never infer policy
	// absence from Git's human-facing stderr; its entry type also lets us reject
	// symlinks and submodules before bytes influence branch naming.
	// gitCanonicalPolicyBytes runs from retained canonical descriptors.
	// --full-tree makes this exact-tree query independent of a CWD pathspec
	// prefix.
	entryBytes, err := gitCanonicalPolicyBytes(ctx, canonical, "ls-tree", "--full-tree", revision, "--", ".wb/worktrees.yaml")
	if err != nil {
		return nil, false, fmt.Errorf("inspect repository worktrees policy at %s: %w", revision, err)
	}
	entry := strings.TrimSpace(string(entryBytes))
	if entry == "" {
		return nil, false, nil
	}
	metadata, path, found := strings.Cut(entry, "\t")
	fields := strings.Fields(metadata)
	if !found || path != ".wb/worktrees.yaml" || len(fields) != 3 || fields[1] != "blob" ||
		(fields[0] != "100644" && fields[0] != "100755") || !isGitObjectID(fields[2]) {
		return nil, false, fmt.Errorf("repository worktrees policy at %s must be a regular blob, not %q", revision, entry)
	}
	sizeBytes, err := gitCanonicalPolicyBytes(ctx, canonical, "cat-file", "-s", fields[2])
	if err != nil {
		return nil, false, fmt.Errorf("inspect repository worktrees policy blob size at %s: %w", revision, err)
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(sizeBytes)), 10, 64)
	if err != nil || size < 0 {
		return nil, false, fmt.Errorf("parse repository worktrees policy blob size at %s: %q", revision, strings.TrimSpace(string(sizeBytes)))
	}
	if size > maxBranchConfigSize {
		return nil, false, fmt.Errorf("repository worktrees policy blob at %s exceeds %d-byte limit", revision, maxBranchConfigSize)
	}
	contents, err := gitCanonicalPolicyBytes(ctx, canonical, "show", fields[2])
	if err != nil {
		return nil, false, fmt.Errorf("read repository worktrees policy blob at %s: %w", revision, err)
	}
	return contents, true, nil
}

func parseBranchConfig(path string, contents []byte) (branchConfigFile, bool, error) {
	if len(contents) > maxBranchConfigSize {
		return branchConfigFile{}, false, fmt.Errorf("worktrees config %s exceeds %d-byte limit", path, maxBranchConfigSize)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	var config branchConfigFile
	if err := decoder.Decode(&config); err != nil {
		return branchConfigFile{}, false, fmt.Errorf("parse worktrees config %s: %w", path, err)
	}
	if config.Version != branchConfigVersion {
		return branchConfigFile{}, false, fmt.Errorf("worktrees config %s has version %d; supported version is %d", path, config.Version, branchConfigVersion)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return branchConfigFile{}, false, fmt.Errorf("parse worktrees config %s: multiple YAML documents are not supported", path)
	} else if !errors.Is(err, io.EOF) {
		return branchConfigFile{}, false, fmt.Errorf("parse worktrees config %s: %w", path, err)
	}
	if config.Worktrees.BranchPrefix != nil {
		prefix := *config.Worktrees.BranchPrefix
		if strings.TrimSpace(prefix) != prefix {
			return branchConfigFile{}, false, fmt.Errorf("worktrees config %s branch_prefix must not have surrounding whitespace", path)
		}
		if prefix != "" && !strings.HasSuffix(prefix, "/") {
			return branchConfigFile{}, false, fmt.Errorf("worktrees config %s branch_prefix must end with /", path)
		}
		if prefix != "" && !validBranch(context.Background(), prefix+"probe") {
			return branchConfigFile{}, false, fmt.Errorf("worktrees config %s has invalid branch_prefix %q", path, prefix)
		}
	}
	if config.Worktrees.Store != nil {
		store := *config.Worktrees.Store
		if strings.TrimSpace(store) != store {
			return branchConfigFile{}, false, fmt.Errorf("worktrees config %s store must not have surrounding whitespace", path)
		}
		if store != StoreModeCentral && store != StoreModeRepositoryLocal {
			return branchConfigFile{}, false, fmt.Errorf("worktrees config %s store mode %q is unsupported; use %q or %q", path, store, StoreModeCentral, StoreModeRepositoryLocal)
		}
	}
	if config.Worktrees.Root != nil {
		if strings.TrimSpace(*config.Worktrees.Root) != *config.Worktrees.Root {
			return branchConfigFile{}, false, fmt.Errorf("worktrees config %s root must not have surrounding whitespace", path)
		}
		if *config.Worktrees.Root == "" {
			return branchConfigFile{}, false, fmt.Errorf("worktrees config %s root must not be empty", path)
		}
	}
	return config, true, nil
}
