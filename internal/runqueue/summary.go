package runqueue

import (
	"path/filepath"
	"strings"
)

// Summary names a program and its first non-flag verb without retaining full argv.
func Summary(args []string) string {
	if len(args) == 0 {
		return "unknown"
	}
	base := filepath.Base(args[0])
	for _, arg := range args[1:] {
		if !strings.HasPrefix(arg, "-") {
			return base + " " + arg
		}
	}
	return base
}
