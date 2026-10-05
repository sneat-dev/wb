package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
)

func TestProtocolGapCorrectionRejectsInvalidReceiptBeforeSidecarRead(t *testing.T) {
	t.Parallel()
	r := WorktreeMergeReceipt{ReceiptPath: filepath.Join(t.TempDir(), "absent.json")}
	err := validateSelfSupersessionCorrectionWithRunner(t.Context(), defaultRunner, worktreeMergeReceiptSHA256, t.TempDir(), r, WorktreeMergeValidationFailureSupersession{})
	if err == nil || !strings.Contains(err.Error(), "inconsistent immutable receipt identity") || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("receipt eligibility did not precede missing correction: %v", err)
	}
}

func TestProtocolGapConflictShapesPropagateActualRetirementDecodeError(t *testing.T) {
	t.Parallel()
	for _, legacy := range []bool{false, true} {
		name := "prepare failure"
		if legacy {
			name = "legacy conflict"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := WorktreeMergeReceipt{ReceiptPath: filepath.Join(t.TempDir(), "receipt.json"), Status: WorktreeMergeConflict, PullRequest: "17"}
			path := retiredPublicationAcknowledgementPath(r.ReceiptPath)
			if err := os.WriteFile(path, []byte("{broken"), 0600); err != nil {
				t.Fatal(err)
			}
			var expected error
			if _, err := effectiveUnpublishedConflict(r); err != nil {
				expected = err
			} else {
				t.Fatal("malformed native sidecar was accepted")
			}
			var got error
			if legacy {
				got = validateLegacyConflictReceiptShape(r, r.ReceiptPath)
			} else {
				got = validatePrepareFailureSupersessionReceipt(r, r.ReceiptPath)
			}
			if got == nil || got.Error() != expected.Error() {
				t.Fatalf("retirement read error lost precedence: %v want %v", got, expected)
			}
			protocolUnchangedBytes(t, path, []byte("{broken"))
		})
	}
}

// protocolGapPostReadRunner delegates every successful command to the real
// runner, then permits one physical private mutation after that observation.
type protocolGapPostReadRunner struct {
	runner.Runner
	after func(string, string, []string)
}

func (r protocolGapPostReadRunner) RunOpts(ctx context.Context, dir string, o runner.RunOptions, name string, args ...string) (runner.Result, error) {
	result, err := r.Runner.RunOpts(ctx, dir, o, name, args...)
	if err == nil && r.after != nil {
		r.after(dir, name, args)
	}
	return result, err
}

func TestProtocolGapCorrectionNativeShapeAndLateRootRefusals(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"immutable claim hash read", "non-directory replacement", "replacement base mismatch", "uncontained recorded replacement", "historical repository read", "late root observation", "late native false root"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			if mode == "non-directory replacement" && runtime.GOOS == "windows" {
				t.Skip("Windows Lstat may classify an intermediate non-directory as missing; Unix ENOTDIR contract")
			}
			f, r, rep, a, o := protocolCorrectionFixture(t)
			o.Apply, o.Actor, o.Reason = true, "private reviewer", "exact native correction boundary"
			c, err := CorrectValidationFailedSelfSupersession(t.Context(), o)
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(r.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			claim, err := validateMergeAcknowledgementCandidate(t.Context(), f.githubDir, r, r.Candidate)
			if err != nil {
				t.Fatal(err)
			}
			claimBefore, err := os.ReadFile(claim.ClaimPath)
			if err != nil {
				t.Fatal(err)
			}
			remoteBefore := strings.TrimSpace(runEngineGit(t, f.repository.CloneURL, "rev-parse", "refs/heads/main"))
			want := ""
			consumed := false
			sentinel := errors.New("selected late immutable root observation")
			hash := worktreeMergeReceiptSHA256
			run := defaultRunner
			gitPath := filepath.Join(rep.WorktreeDir, ".git")
			retained := gitPath + ".owned-gap-retained"
			moved := false
			t.Cleanup(func() {
				if moved {
					if err := os.Rename(retained, gitPath); err != nil {
						t.Errorf("restore private Git pointer: %v", err)
					}
				}
			})
			switch mode {
			case "immutable claim hash read":
				want = "read corrected self-supersession immutable claim"
				hash = func(path string) (string, error) {
					if path == claim.ClaimPath {
						consumed = true
						return "", sentinel
					}
					return worktreeMergeReceiptSHA256(path)
				}
			case "non-directory replacement":
				blocker := filepath.Join(t.TempDir(), "regular-file")
				if err := os.WriteFile(blocker, []byte("owned blocker"), 0600); err != nil {
					t.Fatal(err)
				}
				c.CorrectedReplacement.Worktree = filepath.Join(blocker, "replacement")
				want = "inspect corrected self-supersession replacement"
			case "replacement base mismatch":
				c.ReplacementClaimBaseSHA = "different recorded base"
				want = "replacement identity or claim base no longer matches"
			case "uncontained recorded replacement":
				c.CorrectedReplacement.SHA = strings.TrimSpace(runEngineGit(t, rep.WorktreeDir, "commit-tree", "HEAD^{tree}", "-p", c.CorrectedReplacement.SHA, "-m", "owned uncontained descendant"))
				want = "does not retain its recorded replacement commit"
			case "historical repository read":
				want = "validate corrected self-supersession historical source"
				run = protocolGapPostReadRunner{Runner: defaultRunner, after: func(dir, name string, args []string) {
					if !consumed && dir == rep.WorktreeDir && name == "git" && reflect.DeepEqual(args, []string{"merge-base", c.CorrectedReplacement.SHA, c.CorrectedReplacement.SHA}) {
						if err := os.Rename(gitPath, retained); err != nil {
							t.Fatal(err)
						}
						moved = true
						consumed = true
					}
				}}
			case "late native false root":
				want = "does not contain recorded immutable root " + c.CorrectedReplacement.SHA
				writeEngineFile(t, filepath.Join(rep.WorktreeDir, "owned-late-root-head.txt"), "real private descendant before custody\n")
				runEngineGit(t, rep.WorktreeDir, "add", "owned-late-root-head.txt")
				runEngineGit(t, rep.WorktreeDir, "commit", "-m", "test: real native corrected replacement descendant")
				head := strings.TrimSpace(runEngineGit(t, rep.WorktreeDir, "rev-parse", "HEAD"))
				tree := strings.TrimSpace(runEngineGit(t, rep.WorktreeDir, "rev-parse", head+"^{tree}"))
				if contains, err := isMergeAncestor(t.Context(), rep.WorktreeDir, c.CorrectedReplacement.SHA, head); err != nil || !contains {
					t.Fatalf("real descendant does not retain recorded replacement before custody: %v %v", contains, err)
				}
				replacement := strings.TrimSpace(runEngineGit(t, rep.WorktreeDir, "commit-tree", tree, "-p", r.TargetSHA, "-m", "test: owned late graph bypasses recorded replacement"))
				replaced := false
				t.Cleanup(func() {
					if replaced {
						runEngineGit(t, rep.WorktreeDir, "replace", "-d", head)
					}
				})
				count := 0
				run = prepareOwnerObservedRunner{Runner: defaultRunner, before: func(_ context.Context, dir, name string, args []string) error {
					if dir == rep.WorktreeDir && name == "git" && reflect.DeepEqual(args, []string{"merge-base", c.CorrectedReplacement.SHA, head}) {
						count++
						if count == 2 {
							// Only after native Guard/claim, first true ancestry,
							// historical roots and target fetch: change the private
							// commit interpretation, then delegate the exact query.
							runEngineGit(t, rep.WorktreeDir, "replace", head, replacement)
							replaced = true
							consumed = true
						}
					}
					return nil
				}}
				t.Cleanup(func() {
					if !consumed || count != 2 {
						t.Errorf("late native root stage consumed=%v queries=%d", consumed, count)
						return
					}
					if got := strings.TrimSpace(runEngineGit(t, rep.WorktreeDir, "merge-base", c.CorrectedReplacement.SHA, head)); got != r.TargetSHA {
						t.Errorf("actual late false query has common ancestor %s, want %s", got, r.TargetSHA)
					}
					if got := strings.TrimSpace(runEngineGit(t, rep.WorktreeDir, "rev-parse", head+"^{tree}")); got != tree {
						t.Errorf("late observation changed native candidate tree: %s", got)
					}
					runEngineGit(t, rep.WorktreeDir, "replace", "-d", head)
					replaced = false
					cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cleanupCancel()
					if contains, err := isMergeAncestor(cleanupCtx, rep.WorktreeDir, c.CorrectedReplacement.SHA, head); err != nil || !contains {
						t.Errorf("restored native recorded ancestry=%v %v", contains, err)
					}
				})
			case "late root observation":
				want = "selected late immutable root observation"
				count := 0
				run = prepareOwnerObservedRunner{Runner: defaultRunner, before: func(_ context.Context, dir, name string, args []string) error {
					if dir == rep.WorktreeDir && name == "git" && reflect.DeepEqual(args, []string{"merge-base", c.CorrectedReplacement.SHA, c.CorrectedReplacement.SHA}) {
						count++
						if count == 2 {
							consumed = true
							return sentinel
						}
					}
					return nil
				}}
			}
			c.ID = selfSupersessionCorrectionID(c)
			// This test owns only the sidecar fixture. Native claims/receipt/DAG stay real;
			// recalculate its actual identity after the selected negative record mutation.
			bytes, err := json.MarshalIndent(c, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			bytes = append(bytes, '\n')
			if err := os.WriteFile(c.CorrectionPath, bytes, 0600); err != nil {
				t.Fatal(err)
			}
			err = validateSelfSupersessionCorrectionWithRunner(t.Context(), run, hash, f.githubDir, r, a)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("selected %s refusal=%v want %s", mode, err, want)
			}
			if (mode == "late root observation" || mode == "immutable claim hash read") && (!consumed || !errors.Is(err, sentinel)) {
				t.Fatalf("late error observation identity=%v consumed=%v", err, consumed)
			}
			if mode == "historical repository read" && !consumed {
				t.Fatal("actual ancestry did not reach post-read native mutation")
			}
			if moved {
				if err := os.Rename(retained, gitPath); err != nil {
					t.Fatal(err)
				}
				moved = false
			}
			protocolUnchangedBytes(t, r.ReceiptPath, before)
			protocolUnchangedBytes(t, claim.ClaimPath, claimBefore)
			protocolUnchangedBytes(t, c.CorrectionPath, bytes)
			if remote := strings.TrimSpace(runEngineGit(t, f.repository.CloneURL, "rev-parse", "refs/heads/main")); remote != remoteBefore {
				t.Fatalf("read refusal advanced real remote: %s", remote)
			}
		})
	}
}
