//go:build darwin

package checkoutmarker

import (
	"os"
	"strings"
)

// normalizeExcludeParent recognizes only macOS's fixed system aliases. All
// remaining components are still opened without following symlinks.
func normalizeExcludeParent(path string) string {
	return normalizeExcludeParentWithReadlink(path, os.Readlink)
}

func normalizeExcludeParentWithReadlink(path string, readlink func(string) (string, error)) string {
	for _, alias := range []struct{ source, target, link string }{
		{"/var", "/private/var", "private/var"},
		{"/tmp", "/private/tmp", "private/tmp"},
	} {
		if path != alias.source && !strings.HasPrefix(path, alias.source+"/") {
			continue
		}
		actual, err := readlink(alias.source)
		if err != nil || (actual != alias.link && actual != alias.target) {
			return path
		}
		return alias.target + strings.TrimPrefix(path, alias.source)
	}
	return path
}
