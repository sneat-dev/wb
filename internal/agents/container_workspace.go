package agents

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// This file owns the only host paths a contained execution may ever see. The
// input is one closed ContainerWorkspace, never a repository or profile
// extension: a trusted admission layer resolves the exact worktree and git
// common directory and hands them over here. Everything else is refused.
//
// The plan is deliberately literal. The worktree is bind-mounted read-write at
// its own absolute path (so a linked worktree's .git pointer still resolves),
// the canonical git directory is bind-mounted read-only at its own absolute
// path, and an optional scoped input directory is bind-mounted read-only at the
// fixed /wb-input target. Host package-manager caches are never mounted: every
// dependency location is either an empty container-owned tmpfs mask or a fixed
// /tmp directory created by the unprivileged worker itself.

const (
	// containerInputTarget is the only non-host destination a workspace may
	// add. It is fixed so a caller cannot rename or widen it.
	containerInputTarget = "/wb-input"
	// containerNodeModulesName is masked at the worktree root unconditionally
	// and at every existing nested package root.
	containerNodeModulesName = "node_modules"
	// containerNXCacheName and containerPNPMStoreName are build caches a host
	// toolchain must never reuse. Existing ones are masked.
	containerNXCacheName     = ".nx"
	containerPNPMStoreName   = ".pnpm-store"
	containerGitEntryName    = ".git"
	maxContainerWorkspaceDir = 4096
	maxContainerMaskCount    = 256
	containerTmpfsMode       = "1777"
)

// ContainerWorkspace is the complete and closed set of host paths a contained
// execution may mount. It is trusted admission data produced from WB's
// canonical/worktree metadata; it is not repository configuration, a profile
// field or a command-line option.
//
// WorktreeDir is the exact linked worktree and the only read-write host path.
// GitCommonDir is the canonical git directory that worktree's .git pointer
// names; it is read-only. InputDir is an optional directory already scoped to
// this run, mounted read-only at the fixed /wb-input target. Empty InputDir
// means no input mount.
type ContainerWorkspace struct {
	WorktreeDir  string `json:"worktree_dir"`
	GitCommonDir string `json:"git_common_dir"`
	InputDir     string `json:"input_dir,omitempty"`
}

// SnapshotExecutionWorkspace copies a workspace by value. Admission snapshots
// the trusted paths into the durable record, so a later mutation of the source
// value cannot rewrite what a run was required to mount.
func SnapshotExecutionWorkspace(workspace *ContainerWorkspace) *ContainerWorkspace {
	if workspace == nil {
		return nil
	}
	snapshot := *workspace
	return &snapshot
}

// containerMountKind is the only two mount kinds a contained execution may
// contain: a host bind and a container-owned tmpfs mask.
type containerMountKind string

const (
	containerMountBind  containerMountKind = "bind"
	containerMountTmpfs containerMountKind = "tmpfs"
)

// containerMount is one planned mount. Source is empty for tmpfs. Writable is
// the exact read-write flag the inspect verifier will require.
type containerMount struct {
	kind     containerMountKind
	source   string
	target   string
	writable bool
}

// dockerArgument renders one fixed --mount value. Paths were already validated
// against Docker's comma/quoted value syntax, so no escaping is performed here
// and none would be accepted from a caller.
func (mount containerMount) dockerArgument() string {
	if mount.kind == containerMountTmpfs {
		return "type=tmpfs,target=" + mount.target + ",tmpfs-mode=" + containerTmpfsMode
	}
	argument := "type=bind,source=" + mount.source + ",target=" + mount.target
	if !mount.writable {
		argument += ",readonly"
	}
	return argument
}

// containerWorkspacePlan is the validated, normalized workspace held by the
// runtime. It carries no caller-supplied option beyond the three exact paths.
type containerWorkspacePlan struct {
	worktreeDir string
	mounts      []containerMount
}

// normalizeContainerWorkspace validates one trusted workspace and returns its
// exact mount plan. A nil workspace stays nil and plans no host mounts, which is
// what lifecycle-only probes use.
func normalizeContainerWorkspace(workspace *ContainerWorkspace, host *containerHost) (containerWorkspacePlan, error) {
	if workspace == nil {
		return containerWorkspacePlan{}, nil
	}
	home, err := host.homeDirectory()
	if err != nil {
		return containerWorkspacePlan{}, fmt.Errorf("resolve the owner home directory before validating the container workspace: %w", err)
	}
	worktree, err := validateContainerWorkspaceRoot("worktree directory", workspace.WorktreeDir, home)
	if err != nil {
		return containerWorkspacePlan{}, err
	}
	gitCommon, err := validateContainerWorkspaceRoot("git common directory", workspace.GitCommonDir, home)
	if err != nil {
		return containerWorkspacePlan{}, err
	}
	if containerPathsOverlap(worktree, gitCommon) {
		return containerWorkspacePlan{}, fmt.Errorf("container workspace worktree directory %s overlaps the git common directory %s; both roots must be exact and disjoint", worktree, gitCommon)
	}
	input := ""
	if workspace.InputDir != "" {
		input, err = validateContainerWorkspaceRoot("input directory", workspace.InputDir, home)
		if err != nil {
			return containerWorkspacePlan{}, err
		}
		if containerPathsOverlap(input, worktree) || containerPathsOverlap(input, gitCommon) {
			return containerWorkspacePlan{}, fmt.Errorf("container workspace input directory %s overlaps the worktree or git common directory; the scoped input must be a separate root", input)
		}
	}
	masks, err := containerWorkspaceMaskMounts(worktree)
	if err != nil {
		return containerWorkspacePlan{}, err
	}
	mounts := make([]containerMount, 0, 3+len(masks))
	mounts = append(mounts, containerMount{kind: containerMountBind, source: worktree, target: worktree, writable: true})
	mounts = append(mounts, containerMount{kind: containerMountBind, source: gitCommon, target: gitCommon, writable: false})
	if input != "" {
		mounts = append(mounts, containerMount{kind: containerMountBind, source: input, target: containerInputTarget, writable: false})
	}
	mounts = append(mounts, masks...)
	return containerWorkspacePlan{worktreeDir: worktree, mounts: mounts}, nil
}

// validateContainerWorkspaceRoot accepts one absolute, clean, real directory
// that is neither the filesystem root, the owner home, nor an ancestor of that
// home. A symlink anywhere in the path is refused rather than resolved, because
// resolving it would silently widen what the runtime later mounted.
func validateContainerWorkspaceRoot(label, raw, home string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" {
		return "", fmt.Errorf("the container workspace %s is empty", label)
	}
	if path != raw {
		return "", fmt.Errorf("the container workspace %s %q has leading or trailing whitespace", label, raw)
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("the container workspace %s %q is not absolute", label, path)
	}
	clean := filepath.Clean(path)
	if clean != path {
		return "", fmt.Errorf("the container workspace %s %q is not a clean path; use %q", label, path, clean)
	}
	if err := validateContainerMountPath(label, path); err != nil {
		return "", err
	}
	if forbiddenContainerWorkspaceRoot(path, home) {
		return "", fmt.Errorf("the container workspace %s %q is a forbidden whole-host root", label, path)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect the container workspace %s %q: %w", label, path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("the container workspace %s %q is a symlink; refusing to follow it", label, path)
	}
	if info.Mode()&os.ModeSocket != 0 {
		return "", fmt.Errorf("the container workspace %s %q is a socket", label, path)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("the container workspace %s %q is not a directory", label, path)
	}
	// Lstat only proves the last component. Resolving the whole path catches an
	// earlier symlink component, which would make the mounted source differ
	// from the path the plan records.
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve the container workspace %s %q: %w", label, path, err)
	}
	if resolved != clean {
		return "", fmt.Errorf("the container workspace %s %q contains a symlink; it resolves to %q", label, path, resolved)
	}
	return clean, nil
}

// validateContainerMountPath refuses the bytes Docker's comma/quoted --mount
// value syntax cannot represent unambiguously, plus every control byte.
func validateContainerMountPath(label, path string) error {
	for _, character := range path {
		switch {
		case character == 0:
			return fmt.Errorf("the container workspace %s contains a NUL byte", label)
		case unicode.IsControl(character):
			return fmt.Errorf("the container workspace %s contains a control character", label)
		case character == ',' || character == '"' || character == '\\' || character == ':':
			return fmt.Errorf("the container workspace %s contains the unsafe character %q", label, character)
		}
	}
	return nil
}

// forbiddenContainerWorkspaceRoot refuses the whole filesystem root, the whole
// owner home, and every ancestor of that home. Legitimate worktrees below the
// home (for example /Users/alex/projects/.worktrees/...) remain allowed.
func forbiddenContainerWorkspaceRoot(path, home string) bool {
	switch path {
	case "/", "/home", "/Users", "/root":
		return true
	}
	home = filepath.Clean(strings.TrimSpace(home))
	if home == "" || home == "." || home == "/" {
		return false
	}
	if containerPathContains(path, home) {
		// The whole home, or an ancestor of it, is always refused.
		return true
	}
	// The default projects tree, worktrees root and WB run store are the
	// parent containers this runtime must never mount. Exact descendants
	// (a real linked worktree, a real canonical .git) remain allowed.
	for _, containerRoot := range []string{
		filepath.Join(home, "projects"),
		filepath.Join(home, ".worktrees"),
		filepath.Join(home, ".wb"),
	} {
		if path == containerRoot {
			return true
		}
	}
	return false
}

// containerPathsOverlap reports whether either clean path contains the other.
func containerPathsOverlap(first, second string) bool {
	return containerPathContains(first, second) || containerPathContains(second, first)
}

func containerPathContains(parent, child string) bool {
	if parent == "" || child == "" {
		return false
	}
	relative, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

// containerWorkspaceMaskMounts returns the empty tmpfs masks for root
// node_modules (always), every existing nested node_modules, and any existing
// .nx/.pnpm-store cache directory. It never descends into node_modules, .git or
// an already masked cache, so package contents and git objects are not scanned.
func containerWorkspaceMaskMounts(worktree string) ([]containerMount, error) {
	targets := []string{filepath.Join(worktree, containerNodeModulesName)}
	if err := validateContainerMaskTarget(targets[0]); err != nil {
		return nil, err
	}
	seen := map[string]bool{targets[0]: true}
	directories := 0
	stack := []string{worktree}
	for len(stack) > 0 {
		directory := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		directories++
		if directories > maxContainerWorkspaceDir {
			return nil, fmt.Errorf("the container workspace worktree has more than %d directories to scan; refusing an unbounded dependency mask", maxContainerWorkspaceDir)
		}
		entries, err := os.ReadDir(directory)
		if err != nil {
			return nil, fmt.Errorf("scan the container workspace directory %s: %w", directory, err)
		}
		for _, entry := range entries {
			name := entry.Name()
			switch name {
			case containerGitEntryName:
				// Git metadata is either a file pointer or the git directory;
				// neither is package content and neither is traversed.
				continue
			case containerNodeModulesName:
				if directory == worktree {
					// The root mask is already planned and is never descended.
					continue
				}
				target := filepath.Join(directory, name)
				if err := validateContainerMaskTarget(target); err != nil {
					return nil, err
				}
				if !seen[target] {
					seen[target] = true
					targets = append(targets, target)
					if len(targets) > maxContainerMaskCount {
						return nil, fmt.Errorf("the container workspace worktree needs more than %d dependency masks", maxContainerMaskCount)
					}
				}
				continue
			case containerNXCacheName, containerPNPMStoreName:
				if entry.IsDir() {
					target := filepath.Join(directory, name)
					if err := validateContainerMaskTarget(target); err != nil {
						return nil, err
					}
					if !seen[target] {
						seen[target] = true
						targets = append(targets, target)
						if len(targets) > maxContainerMaskCount {
							return nil, fmt.Errorf("the container workspace worktree needs more than %d dependency masks", maxContainerMaskCount)
						}
					}
				}
				if entry.Type()&os.ModeSymlink != 0 {
					return nil, fmt.Errorf("the container workspace cache path %s is a symlink; refusing an ambiguous mask target", filepath.Join(directory, name))
				}
				continue
			}
			if entry.Type()&os.ModeSymlink != 0 {
				// A symlinked directory is not traversed: its real contents
				// may be outside the worktree.
				continue
			}
			if !entry.IsDir() {
				continue
			}
			stack = append(stack, filepath.Join(directory, name))
		}
	}
	mounts := make([]containerMount, 0, len(targets))
	for _, target := range targets {
		mounts = append(mounts, containerMount{kind: containerMountTmpfs, target: target, writable: true})
	}
	return mounts, nil
}

// validateContainerMaskTarget refuses a masked path that is a symlink or not a
// directory. A missing root node_modules is accepted: the mask creates an empty
// one rather than reusing anything from the host.
func validateContainerMaskTarget(target string) error {
	if err := validateContainerMountPath("dependency mask target", target); err != nil {
		return err
	}
	info, err := os.Lstat(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect the container workspace dependency mask %s: %w", target, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("the container workspace dependency mask %s is a symlink; refusing an ambiguous mount destination", target)
	}
	if !info.IsDir() {
		return fmt.Errorf("the container workspace dependency mask %s is not a directory", target)
	}
	return nil
}

// verifyContainerMounts is the exact allowlist replacement for the former
// blanket no-mount refusal. Every Docker-reported mount must match one planned
// mount's kind, source, destination and read-write flag; every planned mount
// must appear exactly once; and no bind, volume, tmpfs key or other mount may be
// extra. Host, home and socket sources therefore never match.
func verifyContainerMounts(document containerInspectDocument, expected []containerMount) error {
	remaining := append([]containerMount(nil), expected...)
	for _, mount := range document.Mounts {
		kind := containerMountKind(mount.Type)
		if kind != containerMountBind && kind != containerMountTmpfs {
			return fmt.Errorf("container %s has an unplanned %q mount at %s; only the planned bind and tmpfs allowlist is permitted", document.ID, mount.Type, mount.Destination)
		}
		matched := -1
		for index, planned := range remaining {
			if planned.kind != kind || planned.source != mount.Source || planned.target != mount.Destination || planned.writable != mount.RW {
				continue
			}
			matched = index
			break
		}
		if matched < 0 {
			return fmt.Errorf("container %s has an unplanned %s mount from %q at %q (rw=%t); it does not match any planned source, destination and read-write flag", document.ID, mount.Type, mount.Source, mount.Destination, mount.RW)
		}
		remaining = append(remaining[:matched], remaining[matched+1:]...)
	}
	if len(remaining) != 0 {
		missing := remaining[0]
		return fmt.Errorf("container %s is missing the planned %s mount from %q at %q", document.ID, missing.kind, missing.source, missing.target)
	}

	expectedBinds := map[string]containerMount{}
	for _, planned := range expected {
		if planned.kind == containerMountBind {
			expectedBinds[planned.source+"\x00"+planned.target] = planned
		}
	}
	seenBinds := map[string]bool{}
	for _, raw := range document.HostConfig.Binds {
		source, target, writable, err := parseContainerInspectBind(raw)
		if err != nil {
			return fmt.Errorf("container %s reports an unreadable bind %q: %w", document.ID, raw, err)
		}
		key := source + "\x00" + target
		planned, ok := expectedBinds[key]
		if !ok {
			return fmt.Errorf("container %s reports an unplanned bind from %q at %q", document.ID, source, target)
		}
		if planned.writable != writable {
			return fmt.Errorf("container %s reports bind %q at %q with rw=%t, not the planned rw=%t", document.ID, source, target, writable, planned.writable)
		}
		if seenBinds[key] {
			return fmt.Errorf("container %s reports the bind from %q at %q more than once", document.ID, source, target)
		}
		seenBinds[key] = true
	}
	if len(document.HostConfig.Volumes) != 0 {
		return fmt.Errorf("container %s has named volume mounts; only the planned bind and tmpfs allowlist is permitted", document.ID)
	}
	expectedTmpfs := map[string]bool{}
	for _, planned := range expected {
		if planned.kind == containerMountTmpfs {
			expectedTmpfs[planned.target] = true
		}
	}
	for target := range document.HostConfig.Tmpfs {
		if !expectedTmpfs[target] {
			return fmt.Errorf("container %s has an unplanned tmpfs at %q", document.ID, target)
		}
	}
	return nil
}

// parseContainerInspectBind reads Docker's source:target[:rw|ro] bind form. The
// runtime never accepts a source with a colon, so the three-part split is
// unambiguous.
func parseContainerInspectBind(raw string) (source, target string, writable bool, err error) {
	parts := strings.Split(raw, ":")
	switch len(parts) {
	case 2:
		return parts[0], parts[1], true, nil
	case 3:
		switch parts[2] {
		case "", "rw":
			return parts[0], parts[1], true, nil
		case "ro":
			return parts[0], parts[1], false, nil
		}
	}
	return "", "", false, fmt.Errorf("expected source:target[:rw|ro]")
}
