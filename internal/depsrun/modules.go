package depsrun

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

func DiscoverModules(root string) []string {
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "vendor", "node_modules", "testdata":
				return filepath.SkipDir
			}
			if path != root && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() == "go.mod" {
			dirs = append(dirs, filepath.Dir(path))
		}
		return nil
	})
	sort.Strings(dirs)
	return dirs
}
