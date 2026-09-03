package streams

import (
	"path/filepath"
	"strings"
)

// canonicalPath is the shared clone of one owner/repository below a projects
// root. WB's fleet layout is <projects-root>/<owner>/<repository>, and the
// preflight checks read that clone because the stream worktrees do not exist
// yet when they run — refusing to create them is the point.
func canonicalPath(projectsRoot, repository string) string {
	owner, name, found := strings.Cut(repository, "/")
	if !found {
		return filepath.Join(projectsRoot, repository)
	}
	return filepath.Join(projectsRoot, owner, name)
}
