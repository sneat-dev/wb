package wbexec

import (
	"strings"
)

func QuoteShellWord(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\n\"'\\$`*?[]{}();&|<>#~!") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
