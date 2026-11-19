//go:build e2e

package orchestrate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	// A same-parent merge can include no feature content at all.
	fake := git("commit-tree", r.TargetSHA+"^{tree}", "-p", r.TargetSHA, "-p", batch, "-m", "phony ancestry-only merge")
	if err := verifyRetiredBatchLanding(t.Context(), defaultRunner, f.canonical, fake, batch, r.TargetSHA); err == nil {
		t.Fatal("ancestry-only landing passed")
	}
	for _, wrong := range [][3]string{
		{strings.Repeat("2", 40), batch, r.TargetSHA},
		{final, batch, r.TargetSHA},
		{landing, batch, final},
	} {
		if err := verifyRetiredBatchLanding(t.Context(), defaultRunner, f.canonical, wrong[0], wrong[1], wrong[2]); err == nil {
			t.Fatal("unproved landing accepted", wrong)
		}
	}
	unavailable := errors.New("native merge-tree failed")
	fault := &alreadyLandedFaultRunner{Runner: defaultRunner, err: unavailable, refuse: func(_, _ string, args []string) bool { return slices.Contains(args, "merge-tree") }}
	if err := verifyRetiredBatchLanding(t.Context(), fault, f.canonical, landing, batch, r.TargetSHA); err == nil || !strings.Contains(err.Error(), unavailable.Error()) {
		t.Fatal("failed native replay did not block acknowledgement", err)
	}
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
	remoteHead, remoteBase, remoteLanding, candidatePRs := batch, "main", landing, "[]"
	ctx := githubobserver.WithReader(t.Context(), githubobserver.Reader{Read: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "api" {
			return []byte(landing), nil
		}
		if args[1] == "list" {
			return []byte(candidatePRs), nil
		}
		return fmt.Appendf(nil, `{"state":"MERGED","mergedAt":"2026-01-01T00:00:00Z","baseRefName":%q,"headRefName":"batch","headRefOid":%q,"mergeCommit":{"oid":%q}}`, remoteBase, remoteHead, remoteLanding), nil
	}})
	opt := WorktreeMergeTargetAbsorbedSweepOptions{ProjectsRoot: f.githubDir, Receipts: []string{r.ReceiptPath}, CandidateCommitFile: witness, Actor: "test", Reason: "audited cleanup"}
	expectError := func() {
		t.Helper()
		if _, err := SweepTargetAbsorbedWorktreeMergeReceipts(ctx, opt); err == nil {
			t.Fatal("accepted missing or contradictory evidence")
		}
	}
	candidatePRs = `[{"number":9,"headRefName":"candidate"}]`
	expectError()
	candidatePRs = "[]"
	remoteBase = "other"
	expectError()
	remoteBase = "main"
	pr := row["merged_pull_request"].(map[string]any)
	pr["head_sha"], remoteHead = r.TargetSHA, r.TargetSHA
	writeRow()
	expectError()
	pr["head_sha"], remoteHead = batch, batch
	// Substitute the ancestry-only landing observed by the PR and cleanup.
	pr["merge_sha"], row["remote_target_sha"], row["absorbed_by_sha"], remoteLanding = fake, fake, fake, fake
	writeRow()
	expectError()
	pr["merge_sha"], row["remote_target_sha"], row["absorbed_by_sha"], remoteLanding = landing, landing, landing, landing
	writeRow()
	// Exercise the actual disposition checks before any purported acknowledgement.
	for _, target := range []string{r.Candidate.SHA, strings.Repeat("7", 40)} {
		_, err := collectRetiredPrepareProof(ctx, defaultRunner, f.githubDir, f.canonical, f.canonical, r, "", "", target, githubRead)
		if err == nil || (target == r.Candidate.SHA && !strings.Contains(err.Error(), "already reached target")) {
			t.Fatalf("unexpected candidate ancestry disposition: %v", err)
		}
	}
	missing := func(pattern string) {
		t.Helper()
		names, e := filepath.Glob(pattern)
		if e != nil || len(names) != 1 {
			t.Fatalf("fixture paths %v: %v", names, e)
		}
		body, e := os.ReadFile(names[0])
		if e != nil {
			t.Fatal(e)
		}
		writeEngineFile(t, names[0], "{}")
		expectError()
		writeEngineFile(t, names[0], string(body))
	}
	missing(filepath.Join(home, "reports", "worktree-cleanup", "backlog", "*.json"))
	missing(filepath.Join(home, "worklogs", r.Candidate.Task, "runs", "*", "terminals", "*.json"))
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
	altered := *ack.RetiredPrepare
	altered.DiscardedSourceHead = ""
	if altered.matches(r) {
		t.Fatal("missing final retired source head accepted")
	}
	for _, sources := range [][]WorktreeMergeSource{nil, {r.Sources[0], r.Sources[0]}} {
		bad := r
		bad.Sources = sources
		if applicableRetiredPrepare(bad) {
			t.Fatal("invalid source count or repeated source accepted")
		}
	}
	after, err := os.ReadFile(r.ReceiptPath)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatal("original receipt changed")
	}
}
