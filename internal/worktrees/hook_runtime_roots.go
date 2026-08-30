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

func closeSecureHookRootHandles(handles []secureHookRootHandle) {
	for _, handle := range handles {
		if handle.directory != nil {
			_ = handle.directory.Close()
		}
	}
}
