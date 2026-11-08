package locallink

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/streams"
)

// GoWorkProvenance says what Git knows about a worktree's go.work.
//
// It decides two things that must never guess: whether a go.work is a WB
// local link or repository content, and what `--undo` may do to it. A file
// in HEAD is tracked content — WB restores it, never deletes it.
type GoWorkProvenance string

const (
	// GoWorkUntracked is absent from HEAD: a link WB wrote, or a hand-written
	// workspace.
	GoWorkUntracked GoWorkProvenance = "untracked"
	// GoWorkModified is in HEAD but differs from it: a link written over a
	// committed workspace, or an uncommitted edit.
	GoWorkModified GoWorkProvenance = "modified"
	// GoWorkCommitted is in HEAD and identical to it: repository content.
	GoWorkCommitted GoWorkProvenance = "committed"
)

// headFiles is the Git surface for comparing a worktree path with HEAD. It is
// a value so tests can make each Git call fail without a broken toolchain.
type headFiles struct {
	exists    func(worktree, path string) (bool, error)
	unchanged func(worktree, path string) (bool, error)
	restore   func(worktree, path string) error
}

var gitHeadFiles = headFiles{exists: gitPathExistsAtHEAD, unchanged: gitPathUnchangedFromHEAD, restore: gitRestoreFromHEAD}

// ClassifyGoWork reports the provenance of the go.work at worktree's root.
func ClassifyGoWork(worktree string) (GoWorkProvenance, error) {
	return gitHeadFiles.classify(worktree, goWorkFile)
}

func (files headFiles) classify(worktree, path string) (GoWorkProvenance, error) {
	tracked, err := files.exists(worktree, path)
	if err != nil {
		return "", fmt.Errorf("inspect tracked %s in %s: %w", path, worktree, err)
	}
	if !tracked {
		return GoWorkUntracked, nil
	}
	unchanged, err := files.unchanged(worktree, path)
	if err != nil {
		return "", fmt.Errorf("compare %s with HEAD in %s: %w", path, worktree, err)
	}
	if unchanged {
		return GoWorkCommitted, nil
	}
	return GoWorkModified, nil
}

// GoWorkRemedy names the step that clears a go.work signal.
//
// Only a link stream state recorded is cleared by `--undo`, because undo acts
// on records alone. Every other remedy leaves the decision to the operator
// and never names a command that deletes tracked content.
func GoWorkRemedy(worktree string, provenance GoWorkProvenance, recorded bool) string {
	if recorded {
		return fmt.Sprintf("wb deps propagate local --to %s --undo", worktree)
	}
	switch provenance {
	case GoWorkUntracked:
		return fmt.Sprintf(
			"no stream records %s/go.work, so WB has no link to reverse; inspect it, then commit it or remove it by hand: rm %s %s",
			worktree, filepath.Join(worktree, goWorkFile), filepath.Join(worktree, goWorkSum))
	case GoWorkModified:
		return fmt.Sprintf(
			"no stream records the change to the committed %s/go.work; commit it, or discard it with: git -C %s restore --source=HEAD --staged --worktree -- go.work",
			worktree, worktree)
	default:
		return fmt.Sprintf(
			"the committed %s/go.work names modules that are not tracked inside the repository; fix it in a commit", worktree)
	}
}

// RetirementGoWorkSignal is the file-based half of the `wb worktree end`
// guard. It reports a go.work with `use` entries unless Git shows it is
// committed and unchanged from HEAD: retiring a checkout loses nothing a
// commit already holds, while an untracked or modified workspace may be the
// only trace that the checkout builds against an unpublished library tree.
//
// recorded says stream state holds a Go link for the worktree, which makes
// `--undo` the remedy. It returns nil when there is nothing to report.
func RetirementGoWorkSignal(worktree string, recorded bool) (*LiveLink, error) {
	return retirementGoWorkSignalWith(worktree, recorded, gitHeadFiles)
}

func retirementGoWorkSignalWith(worktree string, recorded bool, files headFiles) (*LiveLink, error) {
	entries, err := GoWorkUseEntries(worktree)
	if err != nil || len(entries) == 0 {
		return nil, err
	}
	provenance, err := files.classify(worktree, goWorkFile)
	if err != nil {
		return nil, err
	}
	if provenance == GoWorkCommitted {
		return nil, nil
	}
	return &LiveLink{
		Source:     "go.work",
		Detail:     fmt.Sprintf("%s/go.work carries use entries: %s (%s)", worktree, strings.Join(entries, ", "), provenance),
		Sanctioned: GoWorkRemedy(worktree, provenance, recorded),
	}, nil
}

// RecordsGoWorkLink reports whether any recorded link is a Go workspace link.
func RecordsGoWorkLink(links []streams.StreamLink) bool {
	for _, link := range links {
		if link.Link.Mechanism == streams.MechanismGoWork {
			return true
		}
	}
	return false
}

// goWorkArtefacts are the paths a Go link record names. A record from before
// artefacts were recorded falls back to the pair linkGo has always written.
func goWorkArtefacts(link streams.Link) []string {
	if len(link.Artifacts) > 0 {
		return link.Artifacts
	}
	return []string{goWorkFile, goWorkSum}
}

// restoreLinkArtefacts reverses the artefacts one link record names.
//
// An artefact absent from HEAD is removed. An artefact in HEAD is tracked
// content: it is restored from HEAD when it differs and left alone when it
// does not — never deleted. A recorded path that escapes the worktree is
// refused rather than followed.
func (files headFiles) restoreLinkArtefacts(worktree string, artefacts []string) ([]string, error) {
	var notes []string
	for _, artefact := range artefacts {
		if !filepath.IsLocal(artefact) {
			return notes, fmt.Errorf("recorded link artefact %q is outside the consumer worktree %s", artefact, worktree)
		}
		provenance, err := files.classify(worktree, artefact)
		if err != nil {
			return notes, err
		}
		switch provenance {
		case GoWorkUntracked:
			if err := os.Remove(filepath.Join(worktree, artefact)); err != nil && !os.IsNotExist(err) {
				return notes, fmt.Errorf("remove %s: %w", artefact, err)
			}
		case GoWorkModified:
			if err := files.restore(worktree, artefact); err != nil {
				return notes, fmt.Errorf("restore tracked %s from HEAD in %s: %w", artefact, worktree, err)
			}
			notes = append(notes, fmt.Sprintf("restored tracked %s from HEAD", artefact))
		default:
			notes = append(notes, fmt.Sprintf("tracked %s is unchanged from HEAD; left as committed", artefact))
		}
	}
	return notes, nil
}

// gitRestoreFromHEAD is the `git checkout HEAD -- <path>` equivalent: the
// index and working tree both return to the committed content.
func gitRestoreFromHEAD(worktree, path string) error {
	command := exec.Command("git", "-C", worktree, "restore", "--source=HEAD", "--staged", "--worktree", "--", filepath.ToSlash(path))
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
