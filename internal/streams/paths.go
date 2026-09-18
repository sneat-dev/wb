package streams

import (
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/repopath"
)

// canonicalPath is the shared clone of one owner/repository below a projects
// root. Canonical clones live at <projects-root>/{host}/{owner}/{repository},
// with the legacy <projects-root>/{owner}/{repository} placement still read so
// a fleet that has not adopted the host level keeps working; the preflight
// checks read that clone because the stream worktrees do not exist yet when
// they run — refusing to create them is the point.
//
// The resolver prefers a clone that actually exists, host level first, so the
// answer follows the machine's real placement instead of a predicted one. A
// coordinate it cannot resolve — a bare name with no owner — keeps the
// historical flat join.
func canonicalPath(projectsRoot, repository string) string {
	parts := strings.Split(strings.Trim(repository, "/"), "/")
	switch len(parts) {
	case 2:
		if address, err := repopath.Locate(projectsRoot, parts[0], parts[1]); err == nil {
			return address.Path(projectsRoot)
		}
	case 3:
		if address, err := repopath.ParseRelative(strings.Join(parts, "/")); err == nil {
			return address.Path(projectsRoot)
		}
	}
	return filepath.Join(projectsRoot, filepath.FromSlash(repository))
}
