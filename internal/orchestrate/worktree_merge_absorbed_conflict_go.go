package orchestrate

import (
	"context"
	"errors"

	"github.com/sneat-dev/wb/internal/mergeack"
)

// proveGoDependencyUpgradePair observes both root files at both revisions before
// delegating the content proof. It accepts neither a lone module file nor a
// successful observation of only part of the pair.
func proveGoDependencyUpgradePair(ctx context.Context, gitRoot, sourceSHA, targetSHA string) (int, error) {
	sourceMod, sourceModPresent := gitFileContentsAtRevision(ctx, gitRoot, sourceSHA, "go.mod")
	targetMod, targetModPresent := gitFileContentsAtRevision(ctx, gitRoot, targetSHA, "go.mod")
	sourceSum, sourceSumPresent := gitFileContentsAtRevision(ctx, gitRoot, sourceSHA, "go.sum")
	targetSum, targetSumPresent := gitFileContentsAtRevision(ctx, gitRoot, targetSHA, "go.sum")
	if !sourceModPresent || !targetModPresent || !sourceSumPresent || !targetSumPresent {
		return 0, errors.New("go dependency upgrade proof requires go.mod and go.sum at both revisions")
	}
	return mergeack.ProveGoDependencyUpgrade(sourceMod, targetMod, sourceSum, targetSum)
}

func gitFileContentsAtRevision(ctx context.Context, gitRoot, revision, path string) (string, bool) {
	output, _, err := runCommand(ctx, defaultRunner, 0, 0, gitRoot, "git", "show", revision+":"+path)
	if err != nil {
		return "", false
	}
	return output, true
}
