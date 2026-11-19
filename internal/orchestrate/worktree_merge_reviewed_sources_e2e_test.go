//go:build e2e

package orchestrate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
)

func TestE2EReviewedRepairRetainsEveryAdvanceAndRerunsValidation(t *testing.T) {
	t.Parallel()
	f := newExplicitRootEngineFixture(t)
	writeEngineGoModule(t, f.canonical, "package app\n")
	runEngineGit(t, f.canonical, "add", "go.mod", "app.go")
	runEngineGit(t, f.canonical, "commit", "-m", "test: establish valid target")
	runEngineGit(t, f.canonical, "push", "origin", "main")
	a := createMergeSource(t, f, "review-a", "topic/a", "broken.go", "package app\nfunc fail() { unresolved() }\n")
	b := createMergeSource(t, f, "review-b", "topic/b", "b.txt", "original\n")
	prepare := WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{a.WorktreeDir, b.WorktreeDir}, Target: "main", Model: "fixture", AgentRuntime: "fixture", ValidateLocally: true}
	receipt, err := PrepareWorktreeMerge(t.Context(), prepare)
	if err == nil || receipt.Status != WorktreeMergeValidationFailed {
		t.Fatalf("expected real candidate validation failure; status=%s, error=%v", receipt.Status, err)
	}
	before, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	commit := func(dir, name, value string) string {
		writeEngineFile(t, filepath.Join(dir, name), value)
		runEngineGit(t, dir, "add", name)
		runEngineGit(t, dir, "commit", "-m", "fix: repair "+name)
		return strings.TrimSpace(runEngineGit(t, dir, "rev-parse", "HEAD"))
	}
	advanceA := commit(a.WorktreeDir, "broken.go", "package app\nfunc fail() {}\n")
	advanceB := commit(b.WorktreeDir, "b.txt", "reviewed\n")
	opt := WorktreeMergeValidationFailureSupersessionOptions{ProjectsRoot: f.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: b.WorktreeDir, Apply: true, Actor: "reviewer", Reason: "both repairs reviewed"}
	reject := func(reason string) {
		t.Helper()
		if _, err := SupersedeValidationFailedWorktreeMerge(t.Context(), opt); err == nil || !strings.Contains(err.Error(), reason) {
			t.Fatalf("wanted refusal %q, got %v", reason, err)
		}
		if _, err := os.Stat(validationFailureSupersessionPath(receipt.ReceiptPath)); !os.IsNotExist(err) {
			t.Fatalf("refusal wrote sidecar: %v", err)
		}
	}
	reject("differs from landed target")
	opt.ReviewedSources = []string{"topic/b=" + advanceB}
	reject("pin every")
	opt.ReviewedSources = append(opt.ReviewedSources, "topic/a="+receipt.Sources[0].SHA)
	reject("no exact reviewed head")
	opt.ReviewedSources[1] = "topic/a=" + advanceA
	reject("does not contain required immutable root")
	runEngineGit(t, b.WorktreeDir, "merge", "--no-edit", advanceA)
	replacement := strings.TrimSpace(runEngineGit(t, b.WorktreeDir, "rev-parse", "HEAD"))
	opt.ReviewedSources[0] = "topic/b=" + replacement
	opt.Apply = false
	preview, err := SupersedeValidationFailedWorktreeMerge(t.Context(), opt)
	if err != nil || len(preview.ReviewedSourceHeads) != 2 || preview.Replacement.SHA != replacement {
		t.Fatalf("bad dry-run proof: %+v %v", preview, err)
	}
	if _, err := os.Stat(preview.AcknowledgementPath); !os.IsNotExist(err) {
		t.Fatal("dry run wrote an acknowledgement")
	}
	opt.Apply = true
	checkReviewedPinRaces(t, opt, receipt, a.WorktreeDir, advanceA, replacement)
	ack, err := SupersedeValidationFailedWorktreeMerge(t.Context(), opt)
	if err != nil || ack.ID != preview.ID {
		t.Fatalf("apply: %v %s %s", err, ack.ID, preview.ID)
	}
	if replay, err := SupersedeValidationFailedWorktreeMerge(t.Context(), opt); err != nil || replay.ID != ack.ID {
		t.Fatalf("replay: %v", err)
	}
	damaged := ack
	damaged.ReviewedSourceHeads = []string{"bad"}
	wire, err := json.Marshal(damaged)
	if err != nil {
		t.Fatal(err)
	}
	invalid := filepath.Join(t.TempDir(), "invalid.json")
	writeEngineFile(t, invalid, string(wire))
	if _, err := readValidationFailureSupersession(invalid, receipt); err == nil {
		t.Fatal("invalid pinned acknowledgement was accepted")
	}
	after, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatal("failed receipt changed")
	}
	next, err := PrepareWorktreeMerge(t.Context(), prepare)
	if err != nil || next.Status != WorktreeMergePrepared || next.ID == receipt.ID || next.Candidate.SHA == receipt.Candidate.SHA || len(next.Sources) != 2 || len(next.Validation.Results) == 0 {
		t.Fatalf("fresh validated preparation after repair: status=%s, error=%v", next.Status, err)
	}
}

// These simulated Git observations isolate races between the initial observations
// and the final claim/target reread inside the same actual repair operation.
type reviewedRefObservation struct {
	runner.Runner
	directory, command, response string
	fault                        error
	afterHash, hashed            bool
}

func (r *reviewedRefObservation) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if dir == r.directory && name == "git" && r.command == strings.Join(args, " ") && r.afterHash == r.hashed {
		return runner.Result{Stdout: r.response}, r.fault
	}
	return r.Runner.RunOpts(ctx, dir, opts, name, args...)
}
func checkReviewedPinRaces(t *testing.T, opt WorktreeMergeValidationFailureSupersessionOptions, receipt WorktreeMergeReceipt, source, advance, replacement string) {
	t.Helper()
	targetRef := "rev-parse --verify refs/remotes/origin/" + receipt.Target + "^{commit}"
	fetch := "fetch --no-tags origin +refs/heads/" + receipt.Target + ":refs/remotes/origin/" + receipt.Target
	failure := errors.New("observation unavailable")
	for _, tc := range []struct {
		dir, command, response, want string
		after                        bool
		err                          error
	}{
		{opt.ReplacementWorktree, targetRef, advance, "original remote target", false, nil},
		{opt.ReplacementWorktree, "merge-base " + receipt.Candidate.SHA + " " + receipt.TargetSHA, receipt.Candidate.SHA, "must remain outside", false, nil},
		{opt.ReplacementWorktree, "ls-remote origin refs/heads/" + receipt.Candidate.Branch, receipt.Candidate.SHA, "published", false, nil},
		{source, "rev-parse --verify HEAD^{commit}", "", "read receipted source", true, failure},
		{source, "rev-parse --verify HEAD^{commit}", replacement, "changed during proof", true, nil},
		{opt.ReplacementWorktree, fetch, "", "fetch exact remote target", true, failure},
		{opt.ReplacementWorktree, targetRef, advance, "target changed during proof", true, nil},
	} {
		r := &reviewedRefObservation{Runner: defaultRunner, directory: tc.dir, command: tc.command, response: tc.response, fault: tc.err, afterHash: tc.after}
		hash := func(path string) (string, error) { r.hashed = true; return worktreeMergeReceiptSHA256(path) }
		_, err := supersedeValidationFailedWorktreeMergeWithRunner(t.Context(), r, readWorktreeMergeReceipt, hash, opt)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("wanted %q during %s, got %v", tc.want, tc.command, err)
		}
		if _, err := os.Stat(validationFailureSupersessionPath(receipt.ReceiptPath)); !os.IsNotExist(err) {
			t.Fatalf("refusal created acknowledgement: %v", err)
		}
	}
}
