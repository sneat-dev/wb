//go:build e2e

package orchestrate

import (
	"bytes"
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/testenv"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func preparedRebatchNativePair(t *testing.T) (engineFixture, WorktreeMergeReceipt, WorktreeMergeReceipt, WorktreeMergePreparedRebatch) {
	t.Helper()
	f := newExplicitRootEngineFixture(t)
	first := createMergeSource(t, f, "rebatch-first", "feature/rebatch-first", "first.txt", "first\n")
	options := WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{first.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"}
	original, err := PrepareWorktreeMerge(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	extra := createMergeSource(t, f, "rebatch-extra", "feature/rebatch-extra", "extra.txt", "extra\n")
	options.Sources = append(options.Sources, extra.WorktreeDir)
	options.RebatchReceipt = original.ReceiptPath
	replacement, err := PrepareWorktreeMerge(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	ack, err := readPreparedWorktreeMergeRebatch(rebatchPath(original.ReceiptPath), original)
	if err != nil {
		t.Fatal(err)
	}
	return f, original, replacement, ack
}

func TestE2EPreparedRebatchAdmissionRefusesIncompleteOrChangedNativeEvidence(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"lane", "ineligible original", "target fetch unavailable", "incomplete source", "removed source", "ancestry unavailable"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f, original, replacement, _ := preparedRebatchNativePair(t)
			sources := append([]WorktreeMergeSource(nil), replacement.Sources...)
			want := ""
			switch mode {
			case "lane":
				original.Lane = "wrong-lane"
				if err := persistWorktreeMergeReceipt(original); err != nil {
					t.Fatal(err)
				}
				want = "inconsistent lane"
			case "ineligible original":
				original.Status = WorktreeMergeComplete
				if err := persistWorktreeMergeReceipt(original); err != nil {
					t.Fatal(err)
				}
				want = "not an unlanded prepared candidate"
			case "target fetch unavailable":
				runEngineGit(t, original.Candidate.Worktree, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "unavailable.git"))
				want = "fetch exact remote target"
			case "incomplete source":
				sources[1].SHA = ""
				want = "incomplete ref identity"
			case "removed source":
				sources[0] = sources[1]
				sources[0].Branch = "other-branch"
				want = "removes immutable source"
			case "ancestry unavailable":
				sources[0].Worktree = filepath.Join(t.TempDir(), "unavailable")
				want = "verify rebatch source"
			}
			before, err := os.ReadFile(original.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			_, err = validatePreparedWorktreeMergeRebatch(t.Context(), f.githubDir, original.ReceiptPath, original.Repository, original.Target, sources)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("%s refusal=%v want %s", mode, err, want)
			}
			after, readErr := os.ReadFile(original.ReceiptPath)
			if readErr != nil || !bytes.Equal(before, after) {
				t.Fatalf("refusal mutated original: %v", readErr)
			}
			if _, err := os.Stat(original.Candidate.Worktree); err != nil {
				t.Fatalf("refusal retired original: %v", err)
			}
		})
	}
}

func TestE2EPreparedRebatchAcknowledgementRejectsAbsentAndConflictingEvidence(t *testing.T) {
	t.Parallel()
	if err := ensurePreparedWorktreeMergeRebatch(t.Context(), nil, nil, nil); err == nil || !strings.Contains(err.Error(), "evidence is required") {
		t.Fatalf("nil evidence=%v", err)
	}
	if err := ensurePreparedWorktreeMergeRebatch(t.Context(), &WorktreeMergePreparedRebatch{}, nil, nil); err == nil || !strings.Contains(err.Error(), "replacement receipt is required") {
		t.Fatalf("nil replacement=%v", err)
	}
	for _, mode := range []string{"original missing", "replacement differs", "malformed acknowledgement"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			_, original, replacement, ack := preparedRebatchNativePair(t)
			want := ""
			var missing bool
			switch mode {
			case "original missing":
				if err := os.Remove(original.ReceiptPath); err != nil {
					t.Fatal(err)
				}
				missing = true
			case "replacement differs":
				replacement.Candidate.SHA = "different"
				want = "binds different replacement evidence"
			case "malformed acknowledgement":
				if err := os.WriteFile(rebatchPath(original.ReceiptPath), []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
				want = "decode"
			}
			before, err := os.ReadFile(rebatchPath(original.ReceiptPath))
			if err != nil {
				t.Fatal(err)
			}
			err = ensurePreparedWorktreeMergeRebatch(t.Context(), &ack, &replacement, time.Sleep)
			if err == nil || (missing && !errors.Is(err, os.ErrNotExist)) || (want != "" && !strings.Contains(err.Error(), want)) {
				t.Fatalf("%s error=%v", mode, err)
			}
			after, readErr := os.ReadFile(rebatchPath(original.ReceiptPath))
			if readErr != nil || !bytes.Equal(before, after) {
				t.Fatalf("refusal changed acknowledgement: %v", readErr)
			}
			if _, err := os.Stat(original.Candidate.Worktree); err != nil {
				t.Fatalf("refusal retired candidate: %v", err)
			}
		})
	}
}

func TestE2EPreparedRebatchCleanupRequiresExactDurableProofAndRemoteContainment(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"original missing", "ack missing", "replacement differs", "landing absent from target", "fetch unavailable", "landing ancestry unavailable"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f, original, replacement, ack := preparedRebatchNativePair(t)
			replacement.LandingSHA = replacement.Candidate.SHA
			want := ""
			missing := false
			switch mode {
			case "original missing":
				if err := os.Remove(original.ReceiptPath); err != nil {
					t.Fatal(err)
				}
				want = "read rebatched receipt"
			case "ack missing":
				if err := os.Remove(rebatchPath(original.ReceiptPath)); err != nil {
					t.Fatal(err)
				}
				want = "validate rebatched receipt"
			case "replacement differs":
				replacement.Candidate.SHA = "other"
				want = "exact append-only rebatch cleanup proof"
			case "landing absent from target":
				want = "not contained in exact current target"
			case "fetch unavailable":
				runEngineGit(t, replacement.Candidate.Worktree, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "unavailable.git"))
				missing = true
			case "landing ancestry unavailable":
				replacement.LandingSHA = strings.Repeat("f", 40)
				missing = true
			}
			err := validateRebatchedWorktreeMergeCleanup(context.Background(), f.githubDir, replacement)
			if err == nil || (!missing && !strings.Contains(err.Error(), want)) {
				t.Fatalf("%s cleanup refusal=%v want %s", mode, err, want)
			}
			for _, path := range []string{original.Candidate.Worktree, replacement.Candidate.Worktree} {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("refusal removed native asset %s: %v", path, err)
				}
			}
			if mode != "ack missing" {
				got, err := os.ReadFile(rebatchPath(original.ReceiptPath))
				if err != nil || !bytes.Contains(got, []byte(ack.ID)) {
					t.Fatalf("cleanup altered append-only ack: %v", err)
				}
			}
		})
	}
}

//nolint:paralleltest // Serial owned PATH observer delegates all positive commands to actual git.
func TestE2EPreparedRebatchFinalHashFailureOccursAfterActualSourceAncestry(t *testing.T) {
	f, original, replacement, _ := preparedRebatchNativePair(t)
	if err := os.Remove(rebatchPath(original.ReceiptPath)); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(original.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(original.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	restore := func() {
		if err := os.WriteFile(original.ReceiptPath, before, info.Mode().Perm()); err != nil {
			t.Errorf("restore declared receipt fault: %v", err)
			return
		}
		got, err := os.ReadFile(original.ReceiptPath)
		if err != nil || !bytes.Equal(got, before) {
			t.Errorf("restored original differs: %v", err)
		}
		stat, err := os.Stat(original.ReceiptPath)
		if err != nil || stat.Mode().Perm() != info.Mode().Perm() {
			t.Errorf("restored original mode differs: %v", err)
		}
	}
	t.Cleanup(restore)
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	git, err = filepath.Abs(git)
	if err != nil {
		t.Fatal(err)
	}
	source := replacement.Sources[0]
	physical, err := filepath.EvalSymlinks(source.Worktree)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "ancestry-observed")
	bin := t.TempDir()
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	body := "#!/bin/sh\nset -eu\n" + quote(git) + " \"$@\"\n" +
		"if [ \"$PWD\" = " + quote(physical) + " ] && [ \"$#\" = 3 ] && [ \"$1\" = merge-base ] && [ \"$2\" = " + quote(original.Sources[0].SHA) + " ] && [ \"$3\" = " + quote(source.SHA) + " ]; then\n  printf observed >" + quote(marker) + "\n  rm " + quote(original.ReceiptPath) + "\nfi\n"
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "git"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, err = validatePreparedWorktreeMergeRebatch(t.Context(), f.githubDir, original.ReceiptPath, original.Repository, original.Target, replacement.Sources)
	restore()
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final hash fault=%v", err)
	}
	observed, readErr := os.ReadFile(marker)
	if readErr != nil || string(observed) != "observed" {
		t.Fatalf("native ancestry hook did not occur: %q %v", observed, readErr)
	}
	if _, err := os.Stat(rebatchPath(original.ReceiptPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("hash refusal wrote acknowledgement: %v", err)
	}
	if _, err := os.Stat(original.Candidate.Worktree); err != nil {
		t.Fatalf("hash refusal cleaned candidate: %v", err)
	}
}

//nolint:paralleltest // Existing GH fixture owns PATH and XDG_STATE_HOME.
func TestE2EPreparedRebatchRepairsDurableSupersessionAndSurfacesPersistenceFaults(t *testing.T) {
	for _, mode := range []string{"repair", "repair write refusal", "close intent write refusal"} {
		//nolint:paralleltest // Each row uses the serial owned GH fixture.
		t.Run(mode, func(t *testing.T) {
			f, original, replacement, _ := preparedRebatchNativePair(t)
			runEngineGit(t, original.Candidate.Worktree, "push", "origin", "HEAD:refs/heads/"+original.Candidate.Branch)
			original.Phase = WorktreeMergePhaseLand
			original.Status = WorktreeMergeChecksFailed
			original.PullRequest = "41"
			original.PublishedCandidateSHA = original.Candidate.SHA
			if err := persistWorktreeMergeReceipt(original); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(rebatchPath(original.ReceiptPath)); err != nil {
				t.Fatal(err)
			}
			installWorktreeMergeDirectGH(t)
			t.Setenv("WB_TEST_CANDIDATE_SHA", original.Candidate.SHA)
			t.Setenv("WB_TEST_REMOTE", f.repository.CloneURL)
			closed := filepath.Join(t.TempDir(), "close.log")
			t.Setenv("WB_TEST_CLOSED_PR_LOG", closed)
			evidence, err := validatePreparedWorktreeMergeRebatch(t.Context(), f.githubDir, original.ReceiptPath, original.Repository, original.Target, replacement.Sources)
			if err != nil {
				t.Fatal(err)
			}
			originalBytes, err := os.ReadFile(original.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "close intent write refusal" {
				replacement.ReceiptPath = t.TempDir()
				err = ensurePreparedWorktreeMergeRebatch(t.Context(), evidence, &replacement, time.Sleep)
				if err == nil {
					t.Fatal("close intent write unexpectedly succeeded over directory")
				}
				if _, err := os.Stat(closed); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("close issued after failed intent persistence: %v", err)
				}
				if _, err := os.Stat(rebatchPath(original.ReceiptPath)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("ack issued after failed intent persistence: %v", err)
				}
			} else {
				if err := ensurePreparedWorktreeMergeRebatch(t.Context(), evidence, &replacement, time.Sleep); err != nil {
					t.Fatal(err)
				}
				ackBefore, err := os.ReadFile(rebatchPath(original.ReceiptPath))
				if err != nil {
					t.Fatal(err)
				}
				if replacement.SupersededPullRequest != "41" {
					t.Fatalf("missing persisted close intent: %+v", replacement)
				}
				replacement.SupersededPullRequest = ""
				if err := persistWorktreeMergeReceipt(replacement); err != nil {
					t.Fatal(err)
				}
				if mode == "repair write refusal" {
					parent := filepath.Dir(replacement.ReceiptPath)
					stat, err := os.Stat(parent)
					if err != nil {
						t.Fatal(err)
					}
					restore := func() {
						if err := os.Chmod(parent, stat.Mode().Perm()); err != nil {
							t.Errorf("restore owned parent permissions: %v", err)
						}
					}
					t.Cleanup(restore)
					if err := os.Chmod(parent, 0o500); err != nil {
						t.Fatal(err)
					}
					probe, err := os.CreateTemp(parent, "permission-probe-*")
					if err == nil {
						_ = probe.Close()
						_ = os.Remove(probe.Name())
						t.Fatal("fixture filesystem did not enforce owned directory write refusal")
					}
					err = ensurePreparedWorktreeMergeRebatch(t.Context(), evidence, &replacement, time.Sleep)
					restore()
					if err == nil {
						t.Fatal("supersession repair persistence failure was hidden")
					}
					stored, err := readWorktreeMergeReceipt(replacement.ReceiptPath)
					if err != nil || stored.SupersededPullRequest != "" {
						t.Fatalf("failed repair changed durable receipt: %+v %v", stored, err)
					}
				} else {
					if err := ensurePreparedWorktreeMergeRebatch(t.Context(), evidence, &replacement, time.Sleep); err != nil {
						t.Fatal(err)
					}
					stored, err := readWorktreeMergeReceipt(replacement.ReceiptPath)
					if err != nil || stored.SupersededPullRequest != "41" {
						t.Fatalf("repair not durable: %+v %v", stored, err)
					}
				}
				after, err := os.ReadFile(rebatchPath(original.ReceiptPath))
				if err != nil || !bytes.Equal(ackBefore, after) {
					t.Fatalf("repair changed append-only acknowledgement: %v", err)
				}
			}
			after, err := os.ReadFile(original.ReceiptPath)
			if err != nil || !bytes.Equal(originalBytes, after) {
				t.Fatalf("supersession changed original receipt: %v", err)
			}
			if _, err := os.Stat(original.Candidate.Worktree); err != nil {
				t.Fatalf("supersession prematurely cleaned original: %v", err)
			}
		})
	}
}
