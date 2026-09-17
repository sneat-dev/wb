package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/wbhome"
)

type secureHookRootHandle struct {
	path      string
	directory *os.File
}

// projectsRootContextKey carries the projects root through the secure Git
// helper handoffs. A helper is a separate process that must authorize — and,
// for the hook runtime root, create — the same directory the hook itself will
// write to. With no root it would resolve the machine default instead, and on a
// filesystem-enforcing platform the sandbox would then deny the hook's write.
type projectsRootContextKey struct{}

// withProjectsRoot records the projects root a secure Git handoff belongs to.
// An empty root leaves the context untouched, preserving the legacy fallback to
// the process environment and then the default root.
func withProjectsRoot(ctx context.Context, projectsRoot string) context.Context {
	root := strings.TrimSpace(projectsRoot)
	if root == "" {
		return ctx
	}
	return context.WithValue(ctx, projectsRootContextKey{}, root)
}

// projectsRootFromContext returns the projects root recorded by
// withProjectsRoot, or "" when the caller did not record one.
func projectsRootFromContext(ctx context.Context) string {
	root, _ := ctx.Value(projectsRootContextKey{}).(string)
	return strings.TrimSpace(root)
}

// secureHelperEnvironment is the environment for a secure Git helper child:
// console.Env() plus an explicit WB_PROJECTS_ROOT whenever the caller's context
// carries one. An ambient value is replaced rather than duplicated, so the
// child cannot pick up a different root than the caller is operating on.
func secureHelperEnvironment(ctx context.Context) []string {
	environment := console.Env()
	root := projectsRootFromContext(ctx)
	if root == "" {
		return environment
	}
	return environmentWithValue(environment, wbhome.EnvOverride, root)
}

// helperProjectsRoot is the projects root a secure helper child reads back from
// the environment its parent set. It is deliberately read from the environment
// rather than derived from a repository path: task 2 adds a host level to the
// canonical clone path, and any parent-directory walk would silently break.
func helperProjectsRoot() string {
	return strings.TrimSpace(os.Getenv(wbhome.EnvOverride))
}

func environmentWithValue(environment []string, name, value string) []string {
	prefix := name + "="
	filtered := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered, prefix+value)
}

// appendSecureHookExecutionCapabilityRoots authorizes the hook runtime root a
// hook process resolving projectsRoot would write to, so a sandboxed Git
// handoff cannot be denied that write. projectsRoot must be the real root the
// hook will use; "" falls back to the process environment and then the default
// root, which is only correct when the caller has no other root.
func appendSecureHookExecutionCapabilityRoots(repoPath, projectsRoot string, roots []gitFilesystemCapabilityRoot) ([]gitFilesystemCapabilityRoot, []secureHookRootHandle, error) {
	layout, err := hooks.ResolveExecutionLayout(repoPath, projectsRoot)
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
		roots = append(roots, gitFilesystemCapabilityRoot{path: cachePath, directory: cacheRoot})
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
