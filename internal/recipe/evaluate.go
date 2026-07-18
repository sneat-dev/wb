package recipe

import (
	"fmt"

	"github.com/sneat-dev/wb/internal/gitops"
)

// Evaluate previews what r would do to repoPath without mutating anything.
//
// For a template-section recipe, this fetches and inspects the target file
// at origin/<default-branch> — matching what Land will actually work from
// (a fresh worktree off the default branch), not any local uncommitted
// state. For a command recipe, DryRunCommand (if set) runs directly against
// repoPath's current local working tree, mirroring how a linter is
// normally invoked.
func Evaluate(r Recipe, repoPath string) (Preview, error) {
	switch r.Type {
	case KindTemplateSection:
		tmpl, err := r.loadTemplate()
		if err != nil {
			return Preview{}, err
		}
		if err := gitops.Fetch(repoPath); err != nil {
			return Preview{}, err
		}
		def, err := gitops.DefaultBranch(repoPath)
		if err != nil {
			return Preview{}, err
		}
		content, ok, err := gitops.ShowFile(repoPath, "origin/"+def, r.Target)
		if err != nil {
			return Preview{}, err
		}
		if !ok {
			return Preview{Summary: "no " + r.Target, Changed: false}, nil
		}
		action := planTemplateSection(content, tmpl, r.blockRe())
		return Preview{Summary: action.String(), Changed: action != ActionNoop}, nil
	case KindCommand:
		return previewCommand(r, repoPath)
	default:
		return Preview{}, fmt.Errorf("unknown recipe type %q", r.Type)
	}
}
