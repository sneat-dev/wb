//go:build e2e

package orchestrate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

type retiredProofReader = func(context.Context, string, ...string) (string, error)

func exerciseRetiredPrepareRefusals(t *testing.T, opts WorktreeMergeTargetAbsorbedSweepOptions, receipt WorktreeMergeReceipt, reader retiredProofReader, raw string, cleanup []string) {
	check := func(o WorktreeMergeTargetAbsorbedSweepOptions, run runner.Runner, read retiredProofReader, want string) {
		t.Helper()
		result, err := acknowledgeRetiredPrepareTree(t.Context(), run, o, time.Now(), read)
		if err == nil || !strings.Contains(err.Error(), want) || result.Outcome == WorktreeMergeTargetAbsorbedReleased {
			t.Fatalf("want historical proof refusal %q, got %+v, %v", want, result, err)
		}
	}
	rejected := func(want string) { check(opts, defaultRunner, reader, want) }
	original := func(path string) []byte {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	orig := original(receipt.ReceiptPath)
	checkReceipt := func(r WorktreeMergeReceipt, want string) {
		t.Helper()
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		writeEngineFile(t, receipt.ReceiptPath, string(b))
		rejected(want)
		writeEngineFile(t, receipt.ReceiptPath, string(orig))
	}
	canonical, err := worktrees.CanonicalRepositoryPath(opts.ProjectsRoot, receipt.Repository)
	if err != nil {
		t.Fatal(err)
	}
	cleanResult, err := acknowledgeRetiredPrepareTree(t.Context(), defaultRunner, opts, time.Now(), reader)
	if err != nil {
		t.Fatal(err)
	}
	proof := cleanResult.RetiredPrepare
	_, err = collectRetiredPrepareProof(t.Context(), defaultRunner, opts.ProjectsRoot, canonical, canonical, receipt, "", proof.Tree, cleanResult.TargetSHA, reader)
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete proof: %v", err)
	}
	ackPath := worktreeMergeTargetAbsorbedAcknowledgementPath(receipt.ReceiptPath, proof.ReceiptSHA256)
	writeEngineFile(t, ackPath, "{}")
	rejected("does not prove receipt")
	if err := os.Remove(ackPath); err != nil {
		t.Fatal(err)
	}
	// An alternative recorded proof, even if it independently names the same Git trees, cannot be replaced.
	rival := *proof
	rival.CleanupDigests = slices.Clone(proof.CleanupDigests)
	rival.CleanupDigests[0] = strings.Repeat("7", 64)
	makeRival := func() {
		t.Helper()
		_, _, err := recordWorktreeMergeAbsorptionEvidence(receipt, cleanResult.TargetSHA, absorptionEvidenceCommits(receipt, &rival), "earlier", "prior evidence", time.Now(), &rival)
		if err != nil {
			t.Fatal(err)
		}
	}
	makeRival()
	previous := original(ackPath)
	writeEngineFile(t, ackPath, strings.Replace(string(previous), proof.Tree, "malformed", 1))
	rejected("invalid retired prepare tree proof")
	writeEngineFile(t, ackPath, string(previous))
	rejected("different historical")
	if err := os.Remove(ackPath); err != nil {
		t.Fatal(err)
	}
	applied := opts
	applied.Apply = true
	reads := 0
	check(applied, defaultRunner, func(ctx context.Context, dir string, args ...string) (string, error) {
		if args[0] == "api" {
			reads++
			if reads == 2 {
				makeRival()
			}
		}
		return reader(ctx, dir, args...)
	}, "different historical")
	if err := os.Remove(ackPath); err != nil {
		t.Fatal(err)
	}
	check(applied, &retiredWitnessGit{Runner: defaultRunner, changeTip: true}, reader, "target moved")
	check(opts, &retiredWitnessGit{Runner: defaultRunner, failHash: true}, reader, "native hash unavailable")
	originalTempDir := os.TempDir()
	t.Setenv("TMPDIR", opts.CandidateCommitFile)
	rejected("not a directory")
	t.Setenv("TMPDIR", originalTempDir)
	if err := verifyRetiredPrepareMergeTree(t.Context(), defaultRunner, canonical, filepath.Join(t.TempDir(), "objects.git"), []byte(raw), receipt, strings.Repeat("c", 40)); err == nil || !strings.Contains(err.Error(), "not the clean merge") {
		t.Fatalf("unproved candidate tree: %v", err)
	}
	outside := opts
	outside.Receipts = []string{opts.CandidateCommitFile}
	check(outside, defaultRunner, reader, "outside")
	writeEngineFile(t, receipt.ReceiptPath, "{")
	rejected("decode")
	writeEngineFile(t, receipt.ReceiptPath, string(orig))
	wrong := receipt
	wrong.Lane = "another"
	checkReceipt(wrong, "wrong historical")
	wrong = receipt
	wrong.PullRequest = "published"
	checkReceipt(wrong, "requires one validated")
	blocker, err := AcquireOperationLock(opts.ProjectsRoot, receipt.Lane, true)
	if err != nil {
		t.Fatal(err)
	}
	rejected("already")
	if err := blocker.Release(); err != nil {
		t.Fatal(err)
	}
	// A symlink still resolves to valid receipt bytes through an ordinary read.
	// The secure locked snapshot must reject it.
	saved := receipt.ReceiptPath + ".saved"
	if err := os.Rename(receipt.ReceiptPath, saved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(saved, receipt.ReceiptPath); err != nil {
		t.Fatal(err)
	}
	rejected(syscall.ELOOP.Error())
	if err := os.Rename(saved, receipt.ReceiptPath); err != nil {
		t.Fatal(err)
	}
	absent := opts
	absent.CandidateCommitFile = filepath.Join(t.TempDir(), "absent")
	check(absent, defaultRunner, reader, syscall.ENOENT.Error())
	writeEngineFile(t, opts.CandidateCommitFile, "invalid")
	rejected("ordered")
	writeEngineFile(t, opts.CandidateCommitFile, raw)
	wrong = receipt
	wrong.Repository = "../bad"
	wrong.Lane = worktreeMergeLaneID(wrong.Repository, wrong.Target)
	wrong.ID = worktreeMergeOperationID(wrong.Lane, wrong.Sources)
	wrong.Candidate.Task = wrong.ID
	checkReceipt(wrong, "safe path segments")
	unavailable := errors.New("proof input unavailable")
	// Six ancestry checks cover the two PRs. The seventh verifies the received base
	// and the eighth verifies the historical landing against the present target.
	// The second fetch fences the target immediately before acknowledgement.
	for _, c := range []struct {
		args []string
		nth  int
	}{
		{[]string{"fetch"}, 1}, {[]string{"clone"}, 1}, {[]string{"--no-replace-objects", "merge-tree"}, 1}, {[]string{"worktree", "list"}, 1}, {[]string{"--no-replace-objects", "rev-parse"}, 1}, {[]string{"--no-replace-objects", "merge-base"}, 1},
		{[]string{"--no-replace-objects", "merge-base"}, 7}, {[]string{"--no-replace-objects", "merge-base"}, 8}, {[]string{"fetch"}, 2},
	} {
		count := 0
		run := &alreadyLandedFaultRunner{Runner: defaultRunner, err: unavailable, refuse: func(_, _ string, args []string) bool {
			if len(args) >= len(c.args) && slices.Equal(args[:len(c.args)], c.args) {
				count++
				return count == c.nth
			}
			return false
		}}
		applied := opts
		applied.Apply = true
		check(applied, run, reader, unavailable.Error())
	}
	for _, c := range []struct {
		index          int
		from, to, want string
	}{
		{0, `"applied":true`, `"applied":false`, "does not prove"},
		{0, `"absorbed_at_origin":true`, `"absorbed_at_origin":false`, "exact absorbed"},
		{1, `"state":"MERGED"`, `"state":"OPEN"`, "original merged"},
	} {
		b := original(cleanup[c.index])
		changed := bytes.ReplaceAll(b, []byte(c.from), []byte(c.to))
		if bytes.Equal(changed, b) {
			t.Fatalf("fixture field absent: %s", c.from)
		}
		writeEngineFile(t, cleanup[c.index], string(changed))
		rejected(c.want)
		writeEngineFile(t, cleanup[c.index], string(b))
	}
	for _, c := range []struct {
		verb, value, want string
		fail              bool
	}{
		{"list", "[{}]", "pull request", false}, {"view", "", "unavailable", true}, {"view", "{", "unexpected", false}, {"view", "{}", "remote pull request", false}, {"api", "", "unavailable", true},
	} {
		check(opts, defaultRunner, func(ctx context.Context, dir string, args ...string) (string, error) {
			if slices.Contains(args, c.verb) {
				if c.fail {
					return "", unavailable
				}
				return c.value, nil
			}
			return reader(ctx, dir, args...)
		}, c.want)
	}
	check(opts, defaultRunner, func(ctx context.Context, dir string, args ...string) (string, error) {
		value, err := reader(ctx, dir, args...)
		if len(args) > 1 && args[1] == "view" {
			value = strings.ReplaceAll(value, receipt.Sources[0].Branch, "wrong-head-branch")
		}
		return value, err
	}, "pull request differs")
	// Change terminal evidence between the first inspection and the final collection.
	for _, i := range []struct {
		when    int
		corrupt bool
		want    string
	}{{1, true, "no terminal cleanup receipt"}, {1, false, "changed during proof"}, {2, false, "changed while"}} {
		calls := 0
		b := original(cleanup[0])
		applied := opts
		applied.Apply = true
		check(applied, defaultRunner, func(ctx context.Context, dir string, args ...string) (string, error) {
			if args[0] == "api" {
				calls++
				if calls == i.when {
					switch {
					case i.corrupt:
						writeEngineFile(t, cleanup[0], "invalid")
					case i.when == 2:
						writeEngineFile(t, receipt.ReceiptPath, string(orig)+" ")
					default:
						writeEngineFile(t, cleanup[0], string(b)+" ")
					}
				}
			}
			return reader(ctx, dir, args...)
		}, i.want)
		writeEngineFile(t, cleanup[0], string(b))
		writeEngineFile(t, receipt.ReceiptPath, string(orig))
	}
}

type retiredWitnessGit struct {
	runner.Runner
	changeTip, failHash bool
	tipReads            int
}

func (g *retiredWitnessGit) RunWithInput(ctx context.Context, dir string, input []byte, name string, args ...string) (runner.Result, error) {
	if g.failHash {
		return runner.Result{}, errors.New("native hash unavailable")
	}
	return g.Runner.RunWithInput(ctx, dir, input, name, args...)
}
func (g *retiredWitnessGit) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if g.changeTip && args[0] == "rev-parse" && strings.Contains(strings.Join(args, " "), "refs/remotes/origin/") {
		g.tipReads++
		if g.tipReads == 2 {
			return runner.Result{Stdout: strings.Repeat("9", 40)}, nil
		}
	}
	return g.Runner.RunOpts(ctx, dir, opts, name, args...)
}
