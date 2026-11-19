package orchestrate

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/runner"
	"slices"
	"strings"
)

func absorptionEvidenceCommits(r WorktreeMergeReceipt, proof *RetiredPrepareTreeProof) []string {
	if proof == nil {
		commits, _ := worktreeMergeTargetAbsorptionCommits(r)
		return commits
	}
	// The separately landed candidate's tree is proved; its commit is not an ancestor.
	commits := append(sourceSHAs(r.Sources), r.TargetSHA, proof.Landing)
	commits = append(commits, proof.SourceHeads...)
	commits = append(commits, proof.SourceLandings...)
	if proof.DiscardedSourceHead != "" {
		commits = append(commits, proof.DiscardedSourceHead)
	}
	slices.Sort(commits)
	return slices.Compact(commits)
}
func requireRetiredPrepareAncestor(ctx context.Context, run runner.Runner, objects, ancestor, descendant string) error {
	_, _, err := runCommand(ctx, run, 0, 0, objects, "git", "--no-replace-objects", "merge-base", "--is-ancestor", ancestor, descendant)
	return err
}

// The disposable bare clone omits canonical grafts. All object reads also ignore replacement refs.
// merge-tree may materialize tree or conflict objects; they remain in the disposable clone.
func verifyRetiredPrepareMergeTree(ctx context.Context, run runner.Runner, canonical, objects string, raw []byte, r WorktreeMergeReceipt, expected string) error {
	if _, _, err := runCommand(ctx, run, 0, 0, canonical, "git", "clone", "--bare", "--shared", "--", canonical, objects); err != nil {
		return err
	}
	hash, err := run.RunWithInput(ctx, objects, raw, "git", "--no-replace-objects", "hash-object", "-t", "commit", "--stdin")
	if err != nil {
		return err
	}
	if strings.TrimSpace(hash.Stdout) != r.Candidate.SHA {
		return errors.New("commit body does not match original candidate SHA")
	}
	left, right := retiredPrepareParents(r)
	tree, _, err := runCommand(ctx, run, 0, 0, objects, "git", "--no-replace-objects", "merge-tree", "--write-tree", left, right)
	if err != nil {
		return err
	}
	if strings.TrimSpace(tree) != expected {
		return errors.New("candidate is not the clean merge of the original source trees")
	}
	return nil
}
