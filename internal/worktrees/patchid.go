package worktrees

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/sneat-dev/wb/internal/console"
)

// commitPatchIDs returns the stable patch-id ("git patch-id --stable") for
// every non-merge commit in revRange (a "<base>..<tip>" Git revision range),
// keyed by patch-id and mapping to the commit SHA(s) that produced it (a
// patch-id can legitimately repeat, e.g. an empty commit). --stable is
// required: the default patch-id algorithm is not guaranteed to produce the
// same id for the same patch across Git versions, which would make this
// comparison silently flaky depending on which Git built the two sides.
func commitPatchIDs(ctx context.Context, repository, revRange string) (map[string][]string, error) {
	logCommand := exec.CommandContext(ctx, "git", "-C", repository, "log", "-p", "--no-color", "--no-merges", "--reverse", revRange)
	logCommand.Env = console.Env()
	logOutput, err := logCommand.Output()
	if err != nil {
		return nil, fmt.Errorf("list patches for %s in %s: %w", revRange, repository, describeExitError(err))
	}
	patchIDCommand := exec.CommandContext(ctx, "git", "-C", repository, "patch-id", "--stable")
	patchIDCommand.Env = console.Env()
	patchIDCommand.Stdin = bytes.NewReader(logOutput)
	patchIDOutput, err := patchIDCommand.Output()
	if err != nil {
		return nil, fmt.Errorf("compute patch-ids for %s in %s: %w", revRange, repository, describeExitError(err))
	}
	ids := make(map[string][]string)
	for _, line := range strings.Split(strings.TrimSpace(string(patchIDOutput)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		ids[fields[0]] = append(ids[fields[0]], fields[1])
	}
	return ids, nil
}

// describeExitError adds a command's captured stderr to its error, matching
// this package's other git helpers (see git/gitWithExtraFiles in
// worktrees.go): exec.ExitError alone never carries the process's own
// diagnostic text.
func describeExitError(err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(bytes.TrimSpace(exitErr.Stderr)) > 0 {
		return fmt.Errorf("%w: %s", err, bytes.TrimSpace(exitErr.Stderr))
	}
	return err
}

// commitsShareEveryPatchIDByRebase proves that every non-merge commit's
// patch in "<sealedBase>..sealedHead" also appears, by stable patch-id,
// somewhere in "<sealedBase>..currentHead" — the one fact a rebase (or an
// equivalent history rewrite that replays the same changes as new commits)
// preserves even though it discards the original commits' own SHAs and
// parent chain, which a plain `git merge-base --is-ancestor` check cannot
// see past. sealedBase is computed as the merge-base of sealedHead and
// currentHead: for an ordinary "rebase onto the moved target" (the target
// branch only ever advances, it is not itself rewritten), that merge-base is
// exactly the commit the branch originally diverged from, whether or not
// sealedHead is still reachable from currentHead directly.
//
// An empty sealed range (sealedHead already equals sealedBase — nothing was
// ever committed past the shared base) proves nothing and is deliberately
// rejected: there would be no patches to require, and authorizing cleanup on
// an empty proof would accept a currentHead with no real relationship to
// the finalized work at all.
func commitsShareEveryPatchIDByRebase(ctx context.Context, repository, sealedHead, currentHead string) (bool, error) {
	sealedBase, err := git(ctx, repository, "merge-base", sealedHead, currentHead)
	if err != nil {
		return false, fmt.Errorf("find the common ancestor of %s and %s: %w", sealedHead, currentHead, err)
	}
	sealed, err := commitPatchIDs(ctx, repository, sealedBase+".."+sealedHead)
	if err != nil {
		return false, err
	}
	if len(sealed) == 0 {
		return false, nil
	}
	landed, err := commitPatchIDs(ctx, repository, sealedBase+".."+currentHead)
	if err != nil {
		return false, err
	}
	for id := range sealed {
		if _, ok := landed[id]; !ok {
			return false, nil
		}
	}
	return true, nil
}
