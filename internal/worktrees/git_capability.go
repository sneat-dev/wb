package worktrees

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// gitFilesystemCapability is the explicit ambient-write authority given to a
// stock Git child. Git itself still accepts paths, so its helper must first
// retain and validate the relevant descriptors, then execute under this OS
// policy. A swapped lexical path can make the Git operation fail, but cannot
// grant it authority to write the substitute outside these roots.
type gitFilesystemCapability struct {
	writeRoots []gitFilesystemCapabilityRoot
}

// gitFilesystemCapabilityRoot deliberately carries both the spelling that a
// macOS sandbox profile needs and the retained descriptor that Landlock needs.
// The spelling must never be treated as authority by a platform backend.
type gitFilesystemCapabilityRoot struct {
	path      string
	directory *os.File
}

func requireGitFilesystemCapability() error {
	if err := platformGitFilesystemCapabilityAvailable(); err != nil {
		return err
	}
	// Resolve the executable before Create/Cleanup has prepared any writable
	// WB state. On macOS this also avoids invoking the /usr/bin/git xcrun shim
	// inside the restricted child, where its global cache is not an authority
	// WB should grant to Git.
	_, err := trustedGitExecutable()
	return err
}

func newGitFilesystemCapability(writeRoots ...gitFilesystemCapabilityRoot) (gitFilesystemCapability, error) {
	unique := make(map[string]gitFilesystemCapabilityRoot, len(writeRoots))
	for _, root := range writeRoots {
		root.path = filepath.Clean(root.path)
		if root.directory == nil {
			return gitFilesystemCapability{}, fmt.Errorf("git capability root descriptor is unavailable for %q", root.path)
		}
		info, err := root.directory.Stat()
		if err != nil {
			return gitFilesystemCapability{}, fmt.Errorf("inspect git capability root %q: %w", root.path, err)
		}
		if !info.IsDir() {
			return gitFilesystemCapability{}, fmt.Errorf("git capability root is not a directory: %q", root.path)
		}
		if !filepath.IsAbs(root.path) {
			return gitFilesystemCapability{}, fmt.Errorf("git capability root must be absolute: %q", root.path)
		}
		unique[root.path] = root
	}
	roots := make([]gitFilesystemCapabilityRoot, 0, len(unique))
	for _, root := range unique {
		roots = append(roots, root)
	}
	sort.Slice(roots, func(left, right int) bool { return roots[left].path < roots[right].path })
	if len(roots) == 0 {
		return gitFilesystemCapability{}, fmt.Errorf("git capability requires at least one writable root")
	}
	return gitFilesystemCapability{writeRoots: roots}, nil
}

// runGitWithFilesystemCapability is called only from WB's short-lived helper
// children, after their final descriptor checks. Platform backends either
// execute Git under a deny-by-default write policy or return a dedicated
// capability error before Git starts; there is intentionally no lexical
// fallback.
func runGitWithFilesystemCapability(capability gitFilesystemCapability, executable string, args, environment []string) int {
	return runPlatformGitWithFilesystemCapability(capability, executable, args, environment)
}
