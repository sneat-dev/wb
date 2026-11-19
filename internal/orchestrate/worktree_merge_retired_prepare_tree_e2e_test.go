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
)

//nolint:paralleltest // The unavailable scratch-directory case temporarily changes TMPDIR with t.Setenv.
func TestE2ERetiredPrepareTreeProvesTwoIndependentLandings(t *testing.T) {
	f := newExplicitRootEngineFixture(t)
	git := func(args ...string) string { return strings.TrimSpace(runEngineGit(t, f.canonical, args...)) }
	writeEngineGoModule(t, f.canonical, "package app\n")
	git("add", "go.mod", "app.go")
	git("commit", "-m", "build: seed module")
	git("push", "origin", "main")
	a := createMergeSource(t, f, "review-one", "feature/one", "one.txt", "one\n")
	b := createMergeSource(t, f, "review-two", "feature/two", "two.txt", "two\n")
	receipt, err := PrepareWorktreeMerge(t.Context(), WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{a.WorktreeDir, b.WorktreeDir}, Target: "main", Model: "fixture", AgentRuntime: "fixture", ValidateLocally: true})
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	raw := runEngineGit(t, f.canonical, "cat-file", "commit", receipt.Candidate.SHA)
	file := filepath.Join(t.TempDir(), "original.commit")
	writeEngineFile(t, file, raw)
	git("merge", "--no-ff", "-m", "merge first independently", receipt.Sources[0].SHA)
	first := git("rev-parse", "HEAD")
	runEngineGit(t, b.WorktreeDir, "merge", "-m", "include first landing", first)
	updated := git("rev-parse", b.Branch)
	git("merge", "--no-ff", "-m", "merge second independently", updated)
	landing := git("rev-parse", "HEAD")
	git("push", "origin", "main")
	if git("rev-parse", landing+"^{tree}") != git("rev-parse", receipt.Candidate.SHA+"^{tree}") || landing == receipt.Candidate.SHA {
		t.Fatal("fixture did not make a distinct equal-tree landing")
	}
	home, err := wbhome.Root(f.githubDir)
	if err != nil {
		t.Fatal(err)
	}
	retired := append([]WorktreeMergeSource{{Task: receipt.Candidate.Task, Branch: receipt.Candidate.Branch, Worktree: receipt.Candidate.Worktree, SHA: receipt.Candidate.SHA}}, receipt.Sources...)
	cleanupPaths := make([]string, 0, len(retired))
	for i, m := range retired {
		git("worktree", "remove", m.Worktree)
		git("branch", "-D", m.Branch)
		when := time.Now().UTC().Add(time.Duration(i) * time.Second)
		row := map[string]any{"repository": receipt.Repository, "base": "main", "task": m.Task, "branch": m.Branch, "worktree_dir": m.Worktree, "canonical_dir": f.canonical, "head_sha": m.SHA, "remote_target_sha": landing, "clean": true, "branch_deleted": true, "worktree_gone": true, "applied": true, "eligible": true, "integrated_at_origin": true}
		if i == 0 {
			row["absorbed_at_origin"] = true
			row["absorbed_by_sha"] = landing
		} else {
			head, merged := m.SHA, first
			if i == 2 {
				head, merged = updated, landing
			}
			row["head_sha"] = head
			row["merged_pull_request"] = map[string]any{"repository": receipt.Repository, "base": "main", "number": i, "url": "https://github.com/acme/app/pull/" + string(rune('0'+i)), "head_sha": head, "merge_sha": merged, "merged_at": when, "state": "MERGED"}
		}
		data, err := json.Marshal(map[string]any{"generated_at": when, "phase": "applied", "apply": true, "task": m.Task, "results": []any{row}})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(home, "reports", "worktree-cleanup", when.Format("20060102T150405.000000000Z"), "cleanup.json")
		writeEngineFile(t, path, string(data))
		cleanupPaths = append(cleanupPaths, path)
	}
	opt := WorktreeMergeTargetAbsorbedSweepOptions{ProjectsRoot: f.githubDir, Receipts: []string{receipt.ReceiptPath}, CandidateCommitFile: file, Actor: "fixture", Reason: "reviewed independent landings"}
	remoteTip := landing
	fixturePR := func(_ context.Context, _ string, args ...string) (string, error) {
		if args[0] == "api" {
			return remoteTip, nil
		}
		if args[1] == "list" {
			return "[]", nil
		}
		idx := 0
		head, merged := receipt.Sources[0].SHA, first
		if args[2] == "2" {
			idx = 1
			head, merged = updated, landing
		}
		return fmt.Sprintf(`{"state":"MERGED","mergedAt":"2026-01-01T00:00:00Z","baseRefName":"main","headRefName":%q,"headRefOid":%q,"mergeCommit":{"oid":%q}}`, receipt.Sources[idx].Branch, head, merged), nil
	}
	ctx := githubobserver.WithReader(t.Context(), githubobserver.Reader{Read: func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		output, err := fixturePR(ctx, dir, args...)
		return []byte(output), err
	}})
	sweep := func(o WorktreeMergeTargetAbsorbedSweepOptions) (WorktreeMergeTargetAbsorbedSweep, error) {
		return SweepTargetAbsorbedWorktreeMergeReceipts(ctx, o)
	}
	check := func(refusal string) {
		t.Helper()
		_, e := sweep(opt)
		if e == nil || !strings.Contains(e.Error(), refusal) {
			t.Fatalf("want %q; got %v", refusal, e)
		}
	}
	remoteTip = first
	check("repository target differs")
	remoteTip = landing
	writeEngineFile(t, file, raw+"extra")
	check("commit body")
	writeEngineFile(t, file, raw)
	git("push", "origin", receipt.Sources[0].SHA+":refs/heads/"+a.Branch)
	check("published")
	git("push", "origin", "--delete", a.Branch)
	// An observed PR may have integrated only one source, despite the recorded source commits still existing.
	clean, err := os.ReadFile(cleanupPaths[0])
	if err != nil {
		t.Fatal(err)
	}
	writeEngineFile(t, cleanupPaths[0], strings.ReplaceAll(string(clean), landing, first))
	check("candidate tree differs")
	writeEngineFile(t, cleanupPaths[0], string(clean))
	exerciseRetiredPrepareRefusals(t, opt, receipt, fixturePR, raw, cleanupPaths)
	plain := opt
	plain.CandidateCommitFile = ""
	result, err := SweepTargetAbsorbedWorktreeMergeReceipts(t.Context(), plain)
	if err != nil || result.Results[0].Outcome != WorktreeMergeTargetAbsorbedNotAbsorbed {
		t.Fatalf("ancestry-only path: %+v %v", result, err)
	}
	result, err = sweep(opt)
	if err != nil || result.Results[0].Outcome != WorktreeMergeTargetAbsorbedWouldRelease {
		t.Fatalf("preview: %+v %v", result, err)
	}
	if len(targetAbsorbedAcknowledgements(t, receipt)) != 0 {
		t.Fatal("preview wrote acknowledgement")
	}
	opt.Apply = true
	result, err = sweep(opt)
	if err != nil || result.Results[0].Outcome != WorktreeMergeTargetAbsorbedReleased {
		t.Fatalf("apply: %+v %v", result, err)
	}
	ack, _, err := readWorktreeMergeTargetAbsorbedAcknowledgement(receipt)
	if err != nil || slices.Contains(ack.ProvedCommits, receipt.Candidate.SHA) || !slices.Contains(ack.ProvedCommits, landing) || ack.RetiredPrepare.Candidate != receipt.Candidate.SHA {
		t.Fatalf("wrong recorded ancestry: %+v, %v", ack, err)
	}
	result, err = sweep(opt)
	if err != nil || result.Results[0].Outcome != WorktreeMergeTargetAbsorbedAlreadyReleased {
		t.Fatalf("replay: %+v %v", result, err)
	}
	result, err = SweepTargetAbsorbedWorktreeMergeReceipts(t.Context(), plain)
	if err != nil || result.Results[0].Outcome != WorktreeMergeTargetAbsorbedAlreadyReleased {
		t.Fatalf("ordinary scan: %+v %v", result, err)
	}
	successor := createMergeSource(t, f, "next-release", "feature/next-release", "next.txt", "current\n")
	fresh, err := PrepareWorktreeMerge(t.Context(), WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{successor.WorktreeDir}, Target: "main", Model: "fixture", AgentRuntime: "fixture", ValidateLocally: true})
	if err != nil || fresh.Status != WorktreeMergePrepared || fresh.ID == receipt.ID || len(fresh.Validation.Results) == 0 {
		t.Fatalf("next independently validated preparation: %s %v", fresh.Status, err)
	}
	after, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("historical receipt changed")
	}
}
