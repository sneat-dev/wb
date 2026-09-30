package worktrees

import (
	"os"

	"github.com/sneat-dev/wb/internal/worktreesecure"
)

// closeIncompleteInheritedFiles releases a partially acquired descriptor set.
// Complete sets remain owned by the caller until its successful-path defers.
func closeIncompleteInheritedFiles(files ...*os.File) bool {
	return worktreesecure.CloseIncompleteFiles(files...)
}
