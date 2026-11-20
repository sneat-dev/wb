//go:build !unix

package hooks

import (
	"fmt"
	"os"
)

// actAsWBForHookTemplates has no stand-in here: the built-in hook templates
// are /bin/sh scripts, which template tests only run on Unix. A call is
// refused loudly, so the test binary never re-runs its own suite as "wb".
func actAsWBForHookTemplates(args []string) (int, bool) {
	if len(args) >= 2 && (args[0] == "hooks" || args[0] == "run" || args[0] == "--projects-root") {
		_, _ = fmt.Fprintln(os.Stderr, "hooks test binary: no wb stand-in on this platform")
		return 2, true
	}
	return 0, false
}
