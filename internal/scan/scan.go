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

// HasGoOrTS reports whether root contains at least one Go or TypeScript source
// file, skipping vendored and VCS directories. It stops at the first match.
func HasGoOrTS(root string) (bool, error) {
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
		switch filepath.Ext(d.Name()) {
		case ".go", ".ts", ".tsx":
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found, err
}
