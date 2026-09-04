package worktrees

import (
	"os"
	"path/filepath"
	"strings"
)

// Orphan enumeration reads Git's own worktree registry, which is the only
// source that sees all three layout generations at once. It therefore cannot
// see the one checkout that most needs explaining: one Git no longer registers.
//
// `git worktree remove` deletes a worktree's registration even when it fails
// partway through deleting the working tree, so a failure can leave a checkout
// on disk that no registry mentions. WB now finishes that removal itself and
// keeps a durable record able to resume it, so residue should be transient —
// but a kill in the window between the two, or a checkout left by an earlier
// release, still produces one, and until now no command would have said so.
//
// Detection here is read-only and deliberately has no removal path of its own.
// Removal already has an owner: the cleanup backlog record naming that exact
// path, applied by `wb worktree cleanup`. A second way to delete a checkout,
// for a state that is meant to be rare, is how rarely-exercised code goes
// wrong.

// OrphanResidue is a checkout under WB's own worktrees roots that no canonical
// clone registers.
type OrphanResidue struct {
	Path          string   `json:"path"`
	WorktreesRoot string   `json:"worktrees_root"`
	Task          string   `json:"task"`
	Repository    string   `json:"repository"`
	Layout        string   `json:"layout"`
	CanonicalDir  string   `json:"canonical_dir,omitempty"`
	Evidence      []string `json:"evidence,omitempty"`
	Remedy        string   `json:"remedy"`
}

// residueSweep walks WB's own worktrees roots for checkouts missing from
// registered, the set of every path the canonical clones list. WB owns these
// roots, which is what makes walking them legitimate here even though the rest
// of the sweep deliberately discovers through Git.
func residueSweep(projectsRoot string, roots map[string]string, registered map[string]bool) []OrphanResidue {
	found := make([]OrphanResidue, 0)
	for root, layout := range roots {
		tasks, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		if layout == LayoutLocal {
			owner, repository, coordinatesErr := canonicalCoordinates(projectsRoot, filepath.Dir(root))
			if coordinatesErr != nil {
				continue
			}
			for _, task := range tasks {
				if !task.IsDir() || strings.HasPrefix(task.Name(), ".") {
					continue
				}
				candidate := inspectResidue(projectsRoot, root, layout, task.Name(), owner+"/"+repository,
					filepath.Join(root, task.Name()), registered)
				if candidate != nil {
					found = append(found, *candidate)
				}
			}
			continue
		}
		for _, task := range tasks {
			if !task.IsDir() || strings.HasPrefix(task.Name(), ".") {
				continue
			}
			taskPath := filepath.Join(root, task.Name())
			owners, err := os.ReadDir(taskPath)
			if err != nil {
				continue
			}
			for _, owner := range owners {
				if !owner.IsDir() || strings.HasPrefix(owner.Name(), ".") {
					continue
				}
				ownerPath := filepath.Join(taskPath, owner.Name())
				repositories, err := os.ReadDir(ownerPath)
				if err != nil {
					continue
				}
				for _, repository := range repositories {
					if !repository.IsDir() || strings.HasPrefix(repository.Name(), ".") {
						continue
					}
					candidate := inspectResidue(
						projectsRoot, root, layout, task.Name(),
						owner.Name()+"/"+repository.Name(),
						filepath.Join(ownerPath, repository.Name()),
						registered,
					)
					if candidate != nil {
						found = append(found, *candidate)
					}
				}
			}
		}
	}
	return found
}

// inspectResidue reports a path only when it is recognizably a repository
// checkout: it carries Git metadata, or a canonical clone exists for the
// owner/repository its path claims. A task's own working directories — notes,
// evidence, scripts — satisfy neither and are nobody's residue.
func inspectResidue(projectsRoot, root, layout, task, repository, path string, registered map[string]bool) *OrphanResidue {
	if registered[filepath.Clean(path)] {
		return nil
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil && registered[filepath.Clean(resolved)] {
		return nil
	}
	canonical := filepath.Join(projectsRoot, repository)
	canonicalExists := false
	if info, statErr := os.Stat(filepath.Join(canonical, ".git")); statErr == nil && info.IsDir() {
		canonicalExists = true
	}
	metadata := hasGitMetadata(path)
	if !metadata && !canonicalExists {
		return nil
	}
	residue := &OrphanResidue{
		Path: path, WorktreesRoot: root, Task: task, Repository: repository, Layout: layout,
		Evidence: []string{"no canonical clone registers this path"},
	}
	if canonicalExists {
		residue.CanonicalDir = canonical
		residue.Evidence = append(residue.Evidence, "canonical clone "+canonical+" does not list it")
		residue.Remedy = "wb worktree cleanup " + task + " --apply finishes a removal WB interrupted; if it reports nothing, this checkout predates WB's recovery journal and needs a look before it is removed"
	} else {
		residue.Evidence = append(residue.Evidence, "no canonical clone at "+canonical)
		residue.Remedy = "inspect " + path + " and remove it by hand once its work is confirmed on origin"
	}
	if metadata {
		if gitDir, readErr := os.ReadFile(filepath.Join(path, ".git")); readErr == nil {
			if target, found := strings.CutPrefix(strings.TrimSpace(string(gitDir)), "gitdir: "); found {
				if _, statErr := os.Stat(target); statErr != nil {
					residue.Evidence = append(residue.Evidence, "its .git file points at "+target+", which no longer exists")
				}
			}
		}
	} else {
		residue.Evidence = append(residue.Evidence, "it carries no Git metadata of its own")
	}
	return residue
}
