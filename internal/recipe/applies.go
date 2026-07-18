package recipe

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/scan"
)

var langExts = map[string][]string{
	"go": {".go"},
	"ts": {".ts", ".tsx"},
}

// AppliesTo evaluates r.AppliesIf against repoPath.
func (r Recipe) AppliesTo(repoPath string) (bool, error) {
	switch {
	case r.AppliesIf == "always":
		return true, nil
	case strings.HasPrefix(r.AppliesIf, "has_file:"):
		rel := strings.TrimPrefix(r.AppliesIf, "has_file:")
		return hasFile(repoPath, rel)
	case strings.HasPrefix(r.AppliesIf, "has_source:"):
		langs := strings.Split(strings.TrimPrefix(r.AppliesIf, "has_source:"), ",")
		return hasSource(repoPath, langs)
	default:
		return false, fmt.Errorf("unknown applies_if %q", r.AppliesIf)
	}
}

func hasFile(repoPath, rel string) (bool, error) {
	_, err := os.Stat(filepath.Join(repoPath, rel))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// hasSource reports whether repoPath contains source for any of langs
// (comma-separated in AppliesTo, i.e. OR semantics — "go,ts" matches a repo
// with either).
func hasSource(repoPath string, langs []string) (bool, error) {
	for _, lang := range langs {
		exts, ok := langExts[strings.TrimSpace(lang)]
		if !ok {
			return false, fmt.Errorf("unknown has_source language %q (want %q or %q)", lang, "go", "ts")
		}
		found, err := scan.HasExt(repoPath, exts...)
		if err != nil {
			return false, err
		}
		if found {
			return true, nil
		}
	}
	return false, nil
}
