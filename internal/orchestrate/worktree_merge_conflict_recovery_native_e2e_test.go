//go:build e2e

package orchestrate

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestE2EConflictRecoveryNativeProofAndAppendOnlyAcknowledgement(t *testing.T) {
	t.Parallel()
	f := newConflictRecoveryFixture(t)
	recovered := f.receipt
	recovered.Candidate.SHA = ""
	changed, err := recoverResolvedWorktreeMergeCandidate(t.Context(), f.engine.githubDir, &recovered, 5*time.Second, 0)
	if err != nil || !changed || recovered.Candidate.SHA != f.head || recovered.Status != WorktreeMergePreparing || recovered.Failure != "" {
		t.Fatalf("actual empty candidate recovery=%+v changed=%v error=%v", recovered, changed, err)
	}
	advanced := f.receipt
	changed, err = advanceResolvedConflictWorktreeMergeCandidate(t.Context(), f.engine.githubDir, &advanced, 5*time.Second, 0)
	if err != nil || !changed || advanced.Candidate.SHA != f.head || advanced.Status != WorktreeMergePreparing || advanced.Failure != "" {
		t.Fatalf("actual recorded descendant advance=%+v changed=%v error=%v", advanced, changed, err)
	}
	ack, err := readConflictCandidateAdvance(conflictCandidateAdvancePath(f.receipt.ReceiptPath))
	if err != nil || ack.OriginalCandidate != f.receipt.Candidate || ack.AdvancedCandidateSHA != f.head || ack.CurrentTargetSHA != f.receipt.TargetSHA {
		t.Fatalf("actual immutable acknowledgement=%+v error=%v", ack, err)
	}
	durable, err := readWorktreeMergeReceipt(f.receipt.ReceiptPath)
	if err != nil || durable.Candidate != f.receipt.Candidate || durable.Status != WorktreeMergeConflict {
		t.Fatalf("ack must precede mutable receipt rewrite: %+v error=%v", durable, err)
	}
	if needs, err := conflictCandidateAdvanceNeedsValidation(advanced); err != nil || !needs {
		t.Fatalf("actual acknowledgement needs validation=%v error=%v", needs, err)
	}
	retry := f.receipt
	if changed, err := advanceResolvedConflictWorktreeMergeCandidate(t.Context(), f.engine.githubDir, &retry, 5*time.Second, 0); err != nil || !changed || retry.Candidate.SHA != f.head {
		t.Fatalf("matching immutable acknowledgement replay=%+v changed=%v error=%v", retry, changed, err)
	}
}

func TestE2EConflictRecoveryNegativeCommandsPreserveNativePreconditions(t *testing.T) {
	t.Parallel()
	f := newConflictRecoveryFixture(t)
	sentinel := errors.New("controlled exact native read refused")
	for _, tc := range []struct{ name, dir, argv string }{
		{"candidate status", f.receipt.Candidate.Worktree, "status --porcelain=v1"},
		{"publication read", f.receipt.Candidate.Worktree, "ls-remote --heads origin refs/heads/" + f.receipt.Candidate.Branch},
		{"source status", f.receipt.Sources[0].Worktree, "status --porcelain=v1"},
		{"source HEAD", f.receipt.Sources[0].Worktree, "rev-parse --verify HEAD^{commit}"},
		{"candidate HEAD", f.receipt.Candidate.Worktree, "rev-parse --verify HEAD^{commit}"},
		{"target ancestry", f.receipt.Candidate.Worktree, "merge-base " + f.receipt.TargetSHA + " " + f.head},
		{"source ancestry", f.receipt.Candidate.Worktree, "merge-base " + f.receipt.Sources[0].SHA + " " + f.head},
	} {
		//nolint:paralleltest // native subcases share this private repository; fetch/ref operations and Git locks must not race.
		t.Run(tc.name, func(t *testing.T) {
			for _, empty := range []bool{true, false} {
				receipt := f.receipt
				if empty {
					receipt.Candidate.SHA = ""
				}
				before := receipt
				r := conflictNegativeRunner{Runner: runner.New(), dir: tc.dir, argv: tc.argv, err: sentinel}
				var changed bool
				var err error
				if empty {
					changed, err = recoverResolvedCandidateWithRunner(t.Context(), r, f.engine.githubDir, &receipt, 5*time.Second, 0)
				} else {
					changed, err = advanceResolvedConflictCandidateWithRunner(t.Context(), r, f.engine.githubDir, &receipt, 5*time.Second, 0, nativeConflictAdvanceStore())
				}
				if changed || !errors.Is(err, sentinel) || !reflect.DeepEqual(receipt, before) {
					t.Fatalf("empty=%v negative=%s changed=%v receipt=%+v error=%v", empty, tc.name, changed, receipt, err)
				}
			}
		})
	}
	for _, tc := range []struct{ name, argv string }{
		{"recorded predecessor ancestry", "merge-base " + f.receipt.Candidate.SHA + " " + f.head},
		{"target fetch", "fetch --no-tags origin +refs/heads/main:refs/remotes/origin/main"},
		{"fetched target revision", "rev-parse --verify refs/remotes/origin/main^{commit}"},
	} {
		//nolint:paralleltest // these native commands share one private repository and its refs.
		t.Run(tc.name, func(t *testing.T) {
			receipt := f.receipt
			before := receipt
			r := conflictNegativeRunner{Runner: runner.New(), dir: receipt.Candidate.Worktree, argv: tc.argv, err: sentinel}
			changed, err := advanceResolvedConflictCandidateWithRunner(t.Context(), r, f.engine.githubDir, &receipt, 5*time.Second, 0, nativeConflictAdvanceStore())
			if changed || !errors.Is(err, sentinel) || !reflect.DeepEqual(before, receipt) {
				t.Fatalf("changed=%v receipt=%+v error=%v", changed, receipt, err)
			}
		})
	}
}

func TestE2EConflictRecoveryAcknowledgementFaultsCannotResetReceipt(t *testing.T) {
	t.Parallel()
	f := newConflictRecoveryFixture(t)
	sentinel := errors.New("controlled acknowledgement effect refused")
	for _, fault := range []string{"hash", "initial read", "different existing", "persist", "collision same", "collision different", "collision unreadable"} {
		//nolint:paralleltest // actual recovery fetches share one private candidate; only acknowledgement paths are separate.
		t.Run(fault, func(t *testing.T) {
			receipt := f.receipt
			receipt.ReceiptPath = filepath.Join(t.TempDir(), "receipt.json")
			if err := persistWorktreeMergeReceipt(receipt); err != nil {
				t.Fatal(err)
			}
			before := receipt
			store := nativeConflictAdvanceStore()
			switch fault {
			case "hash":
				store.receiptSHA256 = func(string) (string, error) { return "", sentinel }
			case "initial read":
				store.read = func(string) (WorktreeMergeConflictCandidateAdvance, error) {
					return WorktreeMergeConflictCandidateAdvance{}, sentinel
				}
			case "different existing":
				store.read = func(string) (WorktreeMergeConflictCandidateAdvance, error) {
					return WorktreeMergeConflictCandidateAdvance{ID: "different immutable evidence"}, nil
				}
			case "persist":
				store.persist = func(string, WorktreeMergeConflictCandidateAdvance) error { return sentinel }
			default:
				store.persist = func(path string, ack WorktreeMergeConflictCandidateAdvance) error {
					// Deterministic competing-writer observation: write the real private
					// target before the actual exclusive native publication, without a race.
					switch fault {
					case "collision unreadable":
						if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
							t.Fatal(err)
						}
					case "collision different":
						winner := ack
						winner.AdvancedCandidateSHA = receipt.Sources[0].SHA
						winner.ID = conflictCandidateAdvanceID(winner)
						if err := persistConflictCandidateAdvance(path, winner); err != nil {
							t.Fatal(err)
						}
					default:
						if err := persistConflictCandidateAdvance(path, ack); err != nil {
							t.Fatal(err)
						}
					}
					return persistConflictCandidateAdvance(path, ack)
				}
			}
			changed, err := advanceResolvedConflictCandidateWithRunner(t.Context(), runner.New(), f.engine.githubDir, &receipt, 5*time.Second, 0, store)
			if fault == "collision same" {
				if err != nil || !changed || receipt.Candidate.SHA != f.head {
					t.Fatalf("same immutable winner not adopted: changed=%v error=%v", changed, err)
				}
				return
			}
			if changed || err == nil || !reflect.DeepEqual(receipt, before) {
				t.Fatalf("fault=%s changed=%v receipt=%+v error=%v", fault, changed, receipt, err)
			}
			if (fault == "hash" || fault == "initial read" || fault == "persist") && !errors.Is(err, sentinel) {
				t.Fatalf("effect error identity=%v", err)
			}
		})
	}
}

func TestE2EConflictRecoveryNativeCustodyAndDAGRefusals(t *testing.T) {
	t.Parallel()
	for _, fault := range []string{"missing candidate", "branch", "canonical", "invalid repository", "claim", "claim file", "dirty candidate", "dirty source", "advanced source", "unmerged source", "unrelated predecessor", "published", "target drift", "unchanged head"} {
		t.Run(fault, func(t *testing.T) {
			t.Parallel()
			f := newConflictRecoveryFixture(t)
			receipt := f.receipt
			switch fault {
			case "missing candidate":
				receipt.Candidate.Worktree = filepath.Join(t.TempDir(), "missing")
			case "branch":
				receipt.Candidate.Branch = "wb/other"
			case "canonical":
				receipt.Repository = "acme/other"
			case "invalid repository":
				receipt.Repository = "../outside"
			case "claim":
				receipt.Candidate.Task = "other-task"
			case "claim file":
				if err := os.WriteFile(f.claimPath, []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "dirty candidate":
				writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "dirty.txt"), "dirty\n")
			case "dirty source":
				writeEngineFile(t, filepath.Join(receipt.Sources[0].Worktree, "dirty.txt"), "dirty\n")
			case "advanced source":
				writeEngineFile(t, filepath.Join(receipt.Sources[0].Worktree, "advanced.txt"), "advanced\n")
				runEngineGit(t, receipt.Sources[0].Worktree, "add", "advanced.txt")
				runEngineGit(t, receipt.Sources[0].Worktree, "commit", "-m", "test: advance selected source")
			case "unmerged source", "unrelated predecessor":
				extra := createMergeSource(t, f.engine, "extra-source", "feature/extra-source", "extra.txt", "extra\n")
				sha := strings.TrimSpace(runEngineGit(t, extra.WorktreeDir, "rev-parse", "HEAD"))
				if fault == "unrelated predecessor" {
					receipt.Candidate.SHA = sha
				} else {
					receipt.Sources = append(receipt.Sources, WorktreeMergeSource{Task: "extra-source", Worktree: extra.WorktreeDir, Branch: extra.Branch, SHA: sha})
				}
			case "published":
				runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", receipt.Candidate.Branch)
			case "target drift":
				writeEngineFile(t, filepath.Join(f.engine.canonical, "drift.txt"), "drift\n")
				runEngineGit(t, f.engine.canonical, "add", "drift.txt")
				runEngineGit(t, f.engine.canonical, "commit", "-m", "test: advance remote target")
				runEngineGit(t, f.engine.canonical, "push", "origin", "main")
			case "unchanged head":
				receipt.Candidate.SHA = f.head
			}
			before := receipt
			changed, err := advanceResolvedConflictWorktreeMergeCandidate(t.Context(), f.engine.githubDir, &receipt, 5*time.Second, 0)
			if fault == "unchanged head" {
				if changed || err != nil || !reflect.DeepEqual(receipt, before) {
					t.Fatalf("unchanged native candidate changed=%v err=%v", changed, err)
				}
				return
			}
			if changed || err == nil || !reflect.DeepEqual(receipt, before) {
				t.Fatalf("native fault %s changed=%v error=%v receipt=%+v", fault, changed, err, receipt)
			}
			if fault == "unrelated predecessor" || fault == "target drift" {
				return
			}
			receipt = before
			receipt.Candidate.SHA = ""
			before = receipt
			changed, err = recoverResolvedWorktreeMergeCandidate(t.Context(), f.engine.githubDir, &receipt, 5*time.Second, 0)
			if changed || err == nil || !reflect.DeepEqual(receipt, before) {
				t.Fatalf("empty native fault %s changed=%v error=%v receipt=%+v", fault, changed, err, receipt)
			}
		})
	}
}

func TestE2EConflictRecoveryHistoricalNormalizationUsesNativeDAG(t *testing.T) {
	t.Parallel()
	f := newConflictRecoveryFixture(t)
	oldBase := f.receipt.TargetSHA
	writeEngineFile(t, filepath.Join(f.engine.canonical, "new-target.txt"), "new target\n")
	runEngineGit(t, f.engine.canonical, "add", "new-target.txt")
	runEngineGit(t, f.engine.canonical, "commit", "-m", "test: advance historical target")
	runEngineGit(t, f.engine.canonical, "push", "origin", "main")
	target := strings.TrimSpace(runEngineGit(t, f.engine.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, f.receipt.Candidate.Worktree, "fetch", "origin", "main")
	runEngineGit(t, f.receipt.Candidate.Worktree, "merge", "--no-edit", target)
	head := strings.TrimSpace(runEngineGit(t, f.receipt.Candidate.Worktree, "rev-parse", "HEAD"))
	if err := proveConflictTargetNormalizationWithRunner(t.Context(), defaultRunner, f.receipt.Candidate.Worktree, "main", target, oldBase, head, 5*time.Second, 0); err != nil {
		t.Fatal(err)
	}
	receipt := f.receipt
	receipt.TargetSHA = target
	receipt.Candidate.SHA = ""
	if changed, err := recoverResolvedWorktreeMergeCandidate(t.Context(), f.engine.githubDir, &receipt, 5*time.Second, 0); err != nil || !changed || receipt.Candidate.SHA != head {
		t.Fatalf("native historical normalization changed=%v error=%v receipt=%+v", changed, err, receipt)
	}
	// The recorded-SHA path deliberately retains exact historical base matching.
	recorded := f.receipt
	recorded.TargetSHA = target
	if changed, err := advanceResolvedConflictWorktreeMergeCandidate(t.Context(), f.engine.githubDir, &recorded, 5*time.Second, 0); changed || err == nil {
		t.Fatalf("recorded path normalized a distinct base: changed=%v error=%v", changed, err)
	}
	extra := createMergeSource(t, f.engine, "normalization-extra", "feature/normalization-extra", "extra.txt", "extra\n")
	unrelated := strings.TrimSpace(runEngineGit(t, extra.WorktreeDir, "rev-parse", "HEAD"))
	sentinel := errors.New("controlled exact normalization read refused")
	for _, tc := range []struct{ name, base, snapshot, target, fault string }{
		{"invalid base", "invalid-revision", target, "main", ""},
		{"unmerged base", unrelated, target, "main", ""},
		{"unmerged snapshot", oldBase, unrelated, "main", ""},
		{"fetch error", oldBase, target, "main", "fetch --no-tags origin +refs/heads/main:refs/remotes/origin/main"},
		{"current target ancestry error", oldBase, oldBase, "main", "merge-base " + target + " " + head},
	} {
		//nolint:paralleltest // each proof fetches refs in the same private candidate.
		t.Run(tc.name, func(t *testing.T) {
			run := conflictNegativeRunner{Runner: runner.New(), dir: f.receipt.Candidate.Worktree, argv: tc.fault, err: sentinel}
			err := proveConflictTargetNormalizationWithRunner(t.Context(), run, f.receipt.Candidate.Worktree, tc.target, tc.snapshot, tc.base, head, 5*time.Second, 0)
			if err == nil {
				t.Fatal("unproven native normalization accepted")
			}
			if tc.fault != "" && !errors.Is(err, sentinel) {
				t.Fatalf("named read identity=%v", err)
			}
		})
	}
	// A later actual remote commit cannot be treated as contained merely because
	// the candidate contains the prior receipt snapshot and immutable claim base.
	writeEngineFile(t, filepath.Join(f.engine.canonical, "later.txt"), "later\n")
	runEngineGit(t, f.engine.canonical, "add", "later.txt")
	runEngineGit(t, f.engine.canonical, "commit", "-m", "test: later remote target")
	runEngineGit(t, f.engine.canonical, "push", "origin", "main")
	if err := proveConflictTargetNormalizationWithRunner(t.Context(), defaultRunner, f.receipt.Candidate.Worktree, "main", target, oldBase, head, 5*time.Second, 0); err == nil {
		t.Fatal("unmerged current remote target accepted")
	}
	failed := f.receipt
	failed.TargetSHA = target
	failed.Candidate.SHA = ""
	if changed, err := recoverResolvedWorktreeMergeCandidate(t.Context(), f.engine.githubDir, &failed, 5*time.Second, 0); changed || err == nil {
		t.Fatalf("recovery ignored normalization refusal: changed=%v error=%v", changed, err)
	}
}

func TestE2EConflictRecoveryNativeObservationEntryPoints(t *testing.T) {
	t.Parallel()
	f := newConflictRecoveryFixture(t)
	if err := requireCleanMergeWorktree(t.Context(), f.receipt.Candidate.Worktree); err != nil {
		t.Fatal(err)
	}
	if err := recheckWorktreeMergeSources(t.Context(), f.receipt.Sources); err != nil {
		t.Fatal(err)
	}
	if contains, err := isMergeAncestor(t.Context(), f.receipt.Candidate.Worktree, f.receipt.TargetSHA, f.head); err != nil || !contains {
		t.Fatalf("actual immutable ancestry=%v error=%v", contains, err)
	}
	if target, err := fetchExactMergeTarget(t.Context(), f.receipt.Candidate.Worktree, "main"); err != nil || target != f.receipt.TargetSHA {
		t.Fatalf("actual exact target=%q error=%v", target, err)
	}
}

func TestE2EConflictRecoveryNativeWorkLogFailureAfterSuccessfulGuard(t *testing.T) {
	t.Parallel()
	f := newConflictRecoveryFixture(t)
	prompts := filepath.Join(f.receipt.Candidate.Worktree, ".wb", "local", "prompts")
	if err := os.RemoveAll(prompts); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prompts, []byte("private non-directory prompt fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	guard, err := worktrees.Guard(t.Context(), f.receipt.Candidate.Worktree, worktrees.GuardOptions{ProjectsRoot: f.engine.githubDir, Base: f.receipt.Target})
	if err != nil || guard.Kind != "linked" || guard.Branch != f.receipt.Candidate.Branch {
		t.Fatalf("actual Guard must retain custody before journal read: %+v error=%v", guard, err)
	}
	if _, err := worktrees.LoadWorkLogView(t.Context(), worktrees.LoadWorkLogOptions{ProjectsRoot: f.engine.githubDir, Worktree: guard.Path}); err == nil {
		t.Fatal("actual non-directory prompts were not refused")
	}
	for _, empty := range []bool{true, false} {
		receipt := f.receipt
		if empty {
			receipt.Candidate.SHA = ""
		}
		before := receipt
		var changed bool
		if empty {
			changed, err = recoverResolvedWorktreeMergeCandidate(t.Context(), f.engine.githubDir, &receipt, 5*time.Second, 0)
		} else {
			changed, err = advanceResolvedConflictWorktreeMergeCandidate(t.Context(), f.engine.githubDir, &receipt, 5*time.Second, 0)
		}
		if changed || err == nil || !strings.Contains(err.Error(), "load Work Log") || !strings.Contains(err.Error(), "prompts") || !reflect.DeepEqual(receipt, before) {
			t.Fatalf("native post-Guard journal error empty=%v changed=%v error=%v receipt=%+v", empty, changed, err, receipt)
		}
	}
}

func TestE2EConflictRecoveryNativeTargetAncestryRefusesEarlierCandidate(t *testing.T) {
	t.Parallel()
	f := newExplicitRootEngineFixture(t)
	earlier := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
	writeEngineFile(t, filepath.Join(f.canonical, "later-target.txt"), "later target\n")
	runEngineGit(t, f.canonical, "add", "later-target.txt")
	runEngineGit(t, f.canonical, "commit", "-m", "test: record later target")
	runEngineGit(t, f.canonical, "push", "origin", "main")
	source := createMergeSource(t, f, "earlier-source", "feature/earlier-source", "source.txt", "source\n")
	candidate := createMergeSource(t, f, "earlier-candidate", "wb/earlier-candidate", "candidate.txt", "candidate\n")
	sourceSHA := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	// Initial custody is genuinely valid. The physical branch drift occurs
	// only later, at the operation's exact post-custody HEAD observation.
	guard, err := worktrees.Guard(t.Context(), candidate.WorktreeDir, worktrees.GuardOptions{ProjectsRoot: f.githubDir, Base: "main"})
	if err != nil || guard.Kind != "linked" || guard.Branch != candidate.Branch {
		t.Fatalf("initial native Guard=%+v error=%v", guard, err)
	}
	view, err := worktrees.LoadWorkLogView(t.Context(), worktrees.LoadWorkLogOptions{ProjectsRoot: f.githubDir, Worktree: guard.Path})
	if err != nil || view.Claim == nil || view.Claim.Task != "earlier-candidate" || view.Claim.Branch != candidate.Branch || view.Claim.BaseSHA != candidate.BaseSHA || view.Claim.Lifecycle != "active" {
		t.Fatalf("initial native Work Log=%+v error=%v", view.Claim, err)
	}
	receipt := WorktreeMergeReceipt{Repository: f.repository.Slug, Target: "main", TargetSHA: candidate.BaseSHA, Phase: WorktreeMergePhasePrepare, Status: WorktreeMergeConflict, Candidate: WorktreeMergeCandidate{Task: "earlier-candidate", Worktree: candidate.WorktreeDir, Branch: candidate.Branch}, Sources: []WorktreeMergeSource{{Task: "earlier-source", Worktree: source.WorktreeDir, Branch: source.Branch, SHA: sourceSHA}}}
	if base := strings.TrimSpace(runEngineGit(t, candidate.WorktreeDir, "merge-base", candidate.BaseSHA, earlier)); base != earlier {
		t.Fatalf("native merge-base=%q, want actual earlier ancestor %q", base, earlier)
	}
	before := receipt
	run := &conflictTargetDriftRunner{Runner: runner.New(), worktree: candidate.WorktreeDir, earlier: earlier}
	changed, err := recoverResolvedCandidateWithRunner(t.Context(), run, f.githubDir, &receipt, 5*time.Second, 0)
	if !run.consumed || strings.TrimSpace(runEngineGit(t, candidate.WorktreeDir, "rev-parse", "HEAD")) != earlier {
		t.Fatal("exact post-custody physical drift was not observed")
	}
	if changed || err == nil || !strings.Contains(err.Error(), "does not contain recorded target") || !reflect.DeepEqual(receipt, before) {
		t.Fatalf("native negative target proof changed=%v error=%v receipt=%+v", changed, err, receipt)
	}
}
