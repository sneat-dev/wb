package worktrees

import (
	"os"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/hooks"
)

type secureHookRootHandle struct {
	path      string
	directory *os.File
}

func appendSecureHookExecutionCapabilityRoots(repoPath string, roots []gitFilesystemCapabilityRoot) ([]gitFilesystemCapabilityRoot, []secureHookRootHandle, error) {
	layout, err := hooks.ResolveExecutionLayout(repoPath, "")
	if err != nil {
		return nil, nil, err
	}
	runtimeRoot, err := openAbsoluteDirectoryNoFollow(layout.Root, true)
	if err != nil {
		return nil, nil, err
	}
	handles := []secureHookRootHandle{{path: layout.Root, directory: runtimeRoot}}
	roots = append(roots, gitFilesystemCapabilityRoot{path: layout.Root, directory: runtimeRoot})

	// Hooks share the ambient Go caches rather than a wb-private copy, so a
	// sandboxed hook that compiles anything must be authorized to write them.
	// Without these roots the Go toolchain fails at "failed to initialize
	// build cache ... operation not permitted" before running a single check.
	for _, cachePath := range ambientGoCacheRoots() {
		cacheRoot, openErr := openAbsoluteDirectoryNoFollow(cachePath, true)
		if openErr != nil {
			continue
		}
		handles = append(handles, secureHookRootHandle{path: cachePath, directory: cacheRoot})
		roots = append(roots, gitFilesystemCapabilityRoot{path: cachePath, directory: cacheRoot, shared: true})
	}

	policy, err := hooks.LoadPolicy(repoPath, "")
	if err == nil && policy.Metrics.Enabled {
		metricsRootPath := filepath.Dir(policy.Metrics.Path)
		if metricsRootPath != "" && metricsRootPath != layout.Root {
			if metricsRoot, openErr := openAbsoluteDirectoryNoFollow(metricsRootPath, true); openErr == nil {
				handles = append(handles, secureHookRootHandle{path: metricsRootPath, directory: metricsRoot})
				roots = append(roots, gitFilesystemCapabilityRoot{path: metricsRootPath, directory: metricsRoot})
			}
		}
	}
	return roots, handles, nil
}

// ambientGoCacheRoots resolves the Go build and module caches the hook will
// actually use, honoring the environment exactly as the Go toolchain does and
// falling back to the toolchain's own defaults. Returned paths are absolute
// and deduplicated; unresolvable entries are omitted rather than guessed.
func ambientGoCacheRoots() []string {
	var roots []string
	add := func(path string) {
		if path == "" || !filepath.IsAbs(path) {
			return
		}
		for _, existing := range roots {
			if existing == path {
				return
			}
		}
		roots = append(roots, path)
	}

	goCache := os.Getenv("GOCACHE")
	if goCache == "" {
		if userCache, err := os.UserCacheDir(); err == nil {
			goCache = filepath.Join(userCache, "go-build")
		}
	}
	add(goCache)

	goPath := os.Getenv("GOPATH")
	if goPath == "" {
		if home, err := os.UserHomeDir(); err == nil {
			goPath = filepath.Join(home, "go")
		}
	}
	goModCache := os.Getenv("GOMODCACHE")
	if goModCache == "" && goPath != "" {
		goModCache = filepath.Join(goPath, "pkg", "mod")
	}
	add(goModCache)

	return roots
}

func closeSecureHookRootHandles(handles []secureHookRootHandle) {
	for _, handle := range handles {
		if handle.directory != nil {
			_ = handle.directory.Close()
		}
	}
}
