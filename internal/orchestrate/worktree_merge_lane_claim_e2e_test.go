//go:build e2e

package orchestrate

import (
	"context"
	"fmt"
	"github.com/sneat-dev/wb/internal/worktrees"
	"path/filepath"
	"testing"
)

//nolint:paralleltest // shared native journey scopes WB home through newEngineFixture t.Setenv
func TestE2EMergeLaneClaimSkipsAuthenticatedRebatchBeforeReplacement(t *testing.T) {
	var sources []WorktreeMergeSource
	var repository string
	mergeLaneRebatchJourney(t, func(fixture engineFixture, original, extra worktrees.CreateResult) string {
		var err error
		sources, repository, _, err = inspectWorktreeMergeSources(context.Background(), fixture.githubDir, []string{original.WorktreeDir, extra.WorktreeDir}, "main")
		if err != nil {
			t.Fatal(err)
		}
		target := ""
		for index := 0; index < 256; index++ {
			candidateTarget := fmt.Sprintf("rebatch-scan-%d", index)
			lane := worktreeMergeLaneID(repository, candidateTarget)
			if worktreeMergeOperationID(lane, sources[:1]) < worktreeMergeOperationID(lane, sources) {
				target = candidateTarget
				break
			}
		}
		if target == "" {
			t.Fatal("no target produced old-before-replacement operation order")
		}
		runEngineGit(t, fixture.canonical, "branch", target, "main")
		runEngineGit(t, fixture.canonical, "push", "origin", target)
		return target
	}, func(fixture engineFixture, old, replacement WorktreeMergeReceipt) {
		if filepath.Base(old.ReceiptPath) >= filepath.Base(replacement.ReceiptPath) || old.ID != worktreeMergeOperationID(old.Lane, sources[:1]) || replacement.ID != worktreeMergeOperationID(old.Lane, sources) {
			t.Fatalf("native operation ordering/identity changed: old=%s replacement=%s", old.ID, replacement.ID)
		}
		claim, err := ActiveMergeLaneClaim(fixture.githubDir, repository, sources[0].Branch)
		if err != nil || claim == nil || claim.ReceiptPath != replacement.ReceiptPath {
			t.Fatalf("global branch scan after authentic rebatch = %+v, %v; want replacement %s", claim, err, replacement.ReceiptPath)
		}
	})
}
