package worktrees

import "os"

// closeIncompleteInheritedFiles releases a partially acquired descriptor set.
// Complete sets remain owned by the caller until its successful-path defers.
func closeIncompleteInheritedFiles(files ...*os.File) bool {
	for _, file := range files {
		if file == nil {
			for _, acquired := range files {
				if acquired != nil {
					_ = acquired.Close()
				}
			}
			return true
		}
	}
	return false
}
