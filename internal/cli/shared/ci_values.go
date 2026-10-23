package shared

import (
	"regexp"
	"strings"
	"time"
)

const (
	DefaultCIWaitSlice = 8 * time.Minute
)

var ExactGitObjectID = regexp.MustCompile(`^[0-9a-fA-F]{40}([0-9a-fA-F]{24})?$`)
var ciWaitShellSafeArg = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func ShellQuoteArg(value string) string {
	if ciWaitShellSafeArg.MatchString(value) {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
