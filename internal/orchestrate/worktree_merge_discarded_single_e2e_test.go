//go:build e2e

package orchestrate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestE2EDiscardedSinglePrepareRetainsBothParentsAndActualRetiredSource(t *testing.T) {
	t.Parallel()
	f := newExplicitRootEngineFixture(t)
	git := func(args ...string) string { return strings.TrimSpace(runEngineGit(t, f.canonical, args...)) }
	writeEngineGoModule(t, f.canonical, "package app\n")
	git("add", "go.mod", "app.go")
	git("commit", "-m", "seed module")
	git("push", "origin", "main")
	source := createMergeSource(t, f, "single-feature", "feature/single", "feature", "original")
	writeEngineFile(t, filepath.Join(f.canonical, "unrelated"), "target only")
	git("add", "unrelated")
	git("commit", "-m", "diverge original target")
	git("push", "origin", "main")
	r, err := PrepareWorktreeMerge(t.Context(), WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "fixture", AgentRuntime: "fixture", ValidateLocally: true})
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	raw := runEngineGit(t, f.canonical, "cat-file", "commit", r.Candidate.SHA)
	witness := filepath.Join(t.TempDir(), "original.commit")
	writeEngineFile(t, witness, raw)
	_, err = worktrees.Abort(t.Context(), worktrees.AbortOptions{ProjectsRoot: f.githubDir, Task: r.Candidate.Task, Base: r.Target, Disposition: worktrees.AbortDiscarded, Apply: true, DeleteRemote: true})
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, source.WorktreeDir, "commit", "--allow-empty", "-m", "last original source head")
	final := git("rev-parse", source.Branch)
	git("switch", "-c", "batch", final)
	writeEngineFile(t, filepath.Join(f.canonical, "another-change"), "batch")
	git("add", "another-change")
	git("commit", "-m", "separate batch head")
	batch := git("rev-parse", "HEAD")
	git("switch", "main")
	git("merge", "--no-ff", "batch", "-m", "separate protected merge")
	landing := git("rev-parse", "HEAD")
	git("push", "origin", "main")
	git("worktree", "remove", source.WorktreeDir)
	git("branch", "-D", source.Branch)
	home, err := wbhome.Root(f.githubDir)
	if err != nil {
		t.Fatal(err)
	}
	when := time.Now().UTC()
	row := map[string]any{"repository": r.Repository, "base": r.Target, "task": r.Sources[0].Task, "branch": source.Branch, "worktree_dir": source.WorktreeDir, "canonical_dir": f.canonical, "head_sha": final, "remote_target_sha": landing, "clean": true, "branch_deleted": true, "worktree_gone": true, "applied": true, "eligible": true, "integrated_at_origin": true, "absorbed_at_origin": true, "absorbed_by_sha": landing, "merged_pull_request": map[string]any{"repository": r.Repository, "base": r.Target, "number": 2, "url": "https://github.com/acme/app/pull/2", "head_sha": batch, "merge_sha": landing, "merged_at": when, "state": "MERGED"}}
	report := filepath.Join(home, "reports", "worktree-cleanup", when.Format("20060102T150405.000000000Z"), "cleanup.json")
	writeRow := func() {
		t.Helper()
		data, err := json.Marshal(map[string]any{"generated_at": when, "phase": "applied", "apply": true, "task": r.Sources[0].Task, "results": []any{row}})
		if err != nil {
			t.Fatal(err)
		}
		writeEngineFile(t, report, string(data))
	}
	writeRow()
	ctx := githubobserver.WithReader(t.Context(), githubobserver.Reader{Read: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "api" {
			return []byte(landing), nil
		}
		if args[1] == "list" {
			return []byte("[]"), nil
		}
		return fmt.Appendf(nil, `{"state":"MERGED","mergedAt":"2026-01-01T00:00:00Z","baseRefName":"main","headRefName":"batch","headRefOid":%q,"mergeCommit":{"oid":%q}}`, batch, landing), nil
	}})
	opt := WorktreeMergeTargetAbsorbedSweepOptions{ProjectsRoot: f.githubDir, Receipts: []string{r.ReceiptPath}, CandidateCommitFile: witness, Actor: "test", Reason: "audited cleanup"}
	expectError := func() {
		t.Helper()
		if _, err := SweepTargetAbsorbedWorktreeMergeReceipts(ctx, opt); err == nil {
			t.Fatal("accepted missing or contradictory evidence")
		}
	}
	row["absorbed_at_origin"] = false
	writeRow()
	expectError()
	row["absorbed_at_origin"] = true
	row["head_sha"] = r.TargetSHA
	writeRow()
	expectError()
	row["head_sha"] = final
	writeRow()
	check := func(want string) {
		t.Helper()
		result, err := SweepTargetAbsorbedWorktreeMergeReceipts(ctx, opt)
		if err != nil || len(result.Results) != 1 || result.Results[0].Outcome != want {
			t.Fatalf("want %s: %+v %v", want, result, err)
		}
	}
	check(WorktreeMergeTargetAbsorbedWouldRelease)
	opt.Apply = true
	check(WorktreeMergeTargetAbsorbedReleased)
	check(WorktreeMergeTargetAbsorbedAlreadyReleased)
	ack, _, err := readWorktreeMergeTargetAbsorbedAcknowledgement(r)
	if err != nil || ack.RetiredPrepare.DiscardedSourceHead != final || slices.Contains(ack.ProvedCommits, r.Candidate.SHA) || !slices.Contains(ack.ProvedCommits, r.TargetSHA) {
		t.Fatalf("proof: %+v %v", ack, err)
	}
	after, err := os.ReadFile(r.ReceiptPath)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatal("original receipt changed")
	}
}
