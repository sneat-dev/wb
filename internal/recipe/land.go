package recipe

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/trakhimenok/workbench/wb/internal/gitops"
)

// Land applies r to repoPath and lands the result via git (direct push, or
// an auto-merge PR when the branch is protected or the local clone is
// dirty) — reusing gitops.Land. err is non-nil only for a hard failure
// (unreadable template, a git/gh command failing); "nothing changed" is a
// normal, successful Outcome with Changed=false.
func Land(r Recipe, repoPath, defaultBranch string) (gitops.Outcome, error) {
	opt := gitops.LandOptions{
		DefaultBranch: defaultBranch,
		CommitMessage: r.CommitMessage,
		PRBranch:      r.PRBranch,
		PRTitle:       r.PRTitle,
		PRBody:        r.PRBody,
	}
	switch r.Type {
	case KindTemplateSection:
		tmpl, err := r.loadTemplate()
		if err != nil {
			return gitops.Outcome{}, err
		}
		blockRe := r.blockRe()
		target := r.Target
		return gitops.Land(repoPath, opt, func(wt string) (bool, string, error) {
			path := filepath.Join(wt, target)
			raw, rerr := os.ReadFile(path)
			if rerr != nil {
				// Missing target is a skip, not an error — we don't fabricate one.
				return false, "no " + target, nil
			}
			updated, action := applyTemplateSection(string(raw), tmpl, blockRe)
			if action == ActionNoop {
				return false, "current", nil
			}
			if werr := os.WriteFile(path, []byte(updated), 0o644); werr != nil {
				return false, "", werr
			}
			return true, action.String(), nil
		})
	case KindCommand:
		return gitops.Land(repoPath, opt, commandMutator(r))
	default:
		return gitops.Outcome{}, fmt.Errorf("unknown recipe type %q", r.Type)
	}
}
