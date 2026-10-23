package shared

import (
	"path/filepath"
	"strings"
)

func ShortPath(path string) string {
	trimmed := strings.TrimRight(filepath.Clean(path), string(filepath.Separator))
	owner, repository := filepath.Split(trimmed)
	owner = strings.TrimRight(owner, string(filepath.Separator))
	if base := filepath.Base(owner); base != "" && base != "." && base != string(filepath.Separator) {
		return base + "/" + repository
	}
	return repository
}
