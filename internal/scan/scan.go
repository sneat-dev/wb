// Package scan inspects a repository working tree.
package scan

import (
	"io/fs"
	"path/filepath"
)

// skipDirs are never descended into when looking for source files.
var skipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
}

// HasExt reports whether root contains at least one file whose extension
// (including the leading dot, e.g. ".go") is in exts, skipping vendored and
// VCS directories. Stops at the first match.
func HasExt(root string, exts ...string) (bool, error) {
	want := map[string]bool{}
	for _, e := range exts {
		want[e] = true
	}
	found := false
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are not source files; keep walking
		}
		if d.IsDir() {
			if path != root && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if want[filepath.Ext(d.Name())] {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found, err
}
