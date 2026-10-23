//go:build e2e

package orchestrate

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/retiredcandidateack"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestE2ERetiredPrepareOwnerReadAdmissionAndSecondReadErrors(t *testing.T) {
	t.Parallel()
	f, r := retirementOwnerFixture(t, false)
	verify := retirementOwnerReceiptBytes(t, r)
	o := retirementOwnerOptions(f, r)
	sentinel := errors.New("selected receipt read refusal")
	// All rows are synchronous on one private read-only native fixture.
	for _, stage := range []string{"resolver", "first read", "first validation", "actor", "second read", "second validation", "lock"} {
		//nolint:paralleltest // Rows share one private native fixture and execute mutations/locks synchronously.
		t.Run(stage, func(t *testing.T) {
			calls := 0
			options := o
			read := func(path string) (WorktreeMergeReceipt, error) {
				calls++
				if stage == "first read" && calls == 1 || stage == "second read" && calls == 2 {
					return WorktreeMergeReceipt{}, sentinel
				}
				actual, err := readWorktreeMergeReceipt(path)
				if err == nil && (stage == "first validation" && calls == 1 || stage == "second validation" && calls == 2) {
					actual.Phase = WorktreeMergePhaseLand
				}
				return actual, err
			}
			if stage == "resolver" {
				options.Receipt = ""
			}
			if stage == "actor" {
				options.Actor = ""
			}
			var held OperationLock
			if stage == "lock" {
				var err error
				held, err = AcquireOperationLock(f.githubDir, r.Lane, true)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := held.Release(); err != nil {
						t.Error(err)
					}
				}()
			}
			_, err := acknowledgeRetiredPrepareCandidate(t.Context(), options, read, defaultRunner, retiredcandidateack.FileSHA256, retiredcandidateack.Persist)
			if err == nil {
				t.Fatal("selected negative stage accepted")
			}
			if strings.Contains(stage, "read") && !errors.Is(err, sentinel) {
				t.Fatalf("read identity lost: %v", err)
			}
			if stage == "second read" || stage == "second validation" {
				if calls != 2 {
					t.Fatalf("second read not reached: %d", calls)
				}
			}
			if stage == "resolver" && calls != 0 {
				t.Fatalf("resolver refusal observed read: %d", calls)
			}
			if stage == "actor" && calls != 1 {
				t.Fatalf("actor refusal observed late effects: %d", calls)
			}
			if stage != "lock" {
				retirementOwnerReleased(t, f.githubDir, r.Lane)
			}
			verify()
			if _, err := os.Stat(retiredcandidateack.Path(r.ReceiptPath)); !os.IsNotExist(err) {
				t.Fatalf("refusal published sidecar: %v", err)
			}
		})
	}
}

func TestE2ERetiredPrepareOwnerExactNativeObservationFailures(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"status", "branch", "head", "candidate publication", "target ref", "default branch", "fetch", "default revision", "containment", "hash", "persist", "second proof"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			f, r := retirementOwnerFixture(t, false)
			verify := retirementOwnerReceiptBytes(t, r)
			o := retirementOwnerOptions(f, r)
			sentinel := errors.New("selected native retirement observation " + stage)
			consumed := 0
			args := map[string][]string{"status": {"status", "--porcelain"}, "branch": {"symbolic-ref", "--quiet", "--short", "HEAD"}, "head": {"rev-parse", "--verify", "HEAD^{commit}"}, "candidate publication": {"ls-remote", "origin", "refs/heads/" + r.Candidate.Branch}, "target ref": {"ls-remote", "origin", "refs/heads/" + r.Target}, "default branch": {"ls-remote", "--symref", "origin", "HEAD"}, "fetch": {"fetch", "--no-tags", "origin", "+refs/heads/main:refs/remotes/origin/main"}, "default revision": {"rev-parse", "--verify", "refs/remotes/origin/main^{commit}"}, "containment": {"merge-base", r.TargetSHA, r.TargetSHA}}
			run := retirementOwnerRunner{Runner: defaultRunner, before: func(_ context.Context, dir, name string, actual []string) error {
				if dir == r.Candidate.Worktree && name == "git" && reflect.DeepEqual(actual, args[stage]) {
					consumed++
					return sentinel
				}
				return nil
			}}
			hash := retiredcandidateack.FileSHA256
			persist := retiredcandidateack.Persist
			if stage == "hash" {
				hash = func(path string) (string, error) {
					if path != r.ReceiptPath {
						t.Fatal("hash physical scope changed")
					}
					consumed++
					return "", sentinel
				}
			}
			if stage == "persist" {
				persist = func(path string, a retiredcandidateack.Acknowledgement) error {
					if path != retiredcandidateack.Path(r.ReceiptPath) || a.Candidate.SHA != r.TargetSHA {
						t.Fatalf("real proof not retained: %+v", a)
					}
					consumed++
					return sentinel
				}
			}
			if stage == "second proof" {
				hash = func(path string) (string, error) {
					value, err := retiredcandidateack.FileSHA256(path)
					if err == nil {
						consumed++
						if consumed == 1 {
							writeEngineFile(t, filepath.Join(r.Candidate.Worktree, "late-dirty.txt"), "actual change after first proof\n")
						}
					}
					return value, err
				}
			}
			_, err := acknowledgeRetiredPrepareCandidate(t.Context(), o, readWorktreeMergeReceipt, run, hash, persist)
			if err == nil || consumed != 1 {
				t.Fatalf("named native stage not consumed: %v count=%d", err, consumed)
			}
			if stage == "second proof" {
				if !strings.Contains(err.Error(), "local changes") {
					t.Fatalf("mutable second proof ignored: %v", err)
				}
			} else if !errors.Is(err, sentinel) {
				t.Fatalf("primary observation identity lost: %v", err)
			}
			verify()
			if _, err := os.Stat(retiredcandidateack.Path(r.ReceiptPath)); !os.IsNotExist(err) {
				t.Fatalf("failed stage published sidecar: %v", err)
			}
			retirementOwnerReleased(t, f.githubDir, r.Lane)
		})
	}
}

func TestE2ERetiredPrepareOwnerNativeStateAndWinnerContracts(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"dry run", "apply replay", "winner", "missing winner", "malformed sidecar", "dirty", "detached", "wrong branch", "advanced head", "published", "target exists", "missing default", "detached default", "uncontained"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			f, r := retirementOwnerFixture(t, stage == "uncontained")
			verify := retirementOwnerReceiptBytes(t, r)
			o := retirementOwnerOptions(f, r)
			persist := retiredcandidateack.Persist
			want := ""
			win := false
			switch stage {
			case "dry run":
				o.Apply = false
			case "apply replay":
			case "winner":
				persist = func(path string, a retiredcandidateack.Acknowledgement) error {
					if err := retiredcandidateack.Persist(path, a); err != nil {
						return err
					}
					win = true
					return os.ErrExist
				}
			case "missing winner":
				persist = func(string, retiredcandidateack.Acknowledgement) error { win = true; return os.ErrExist }
			case "malformed sidecar":
				if err := os.WriteFile(retiredcandidateack.Path(r.ReceiptPath), []byte("not JSON\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				want = ""
			case "dirty":
				writeEngineFile(t, filepath.Join(r.Candidate.Worktree, "dirty.txt"), "native dirty\n")
				want = "local changes"
			case "detached":
				runEngineGit(t, r.Candidate.Worktree, "checkout", "--detach")
				want = "read candidate branch"
			case "wrong branch":
				runEngineGit(t, r.Candidate.Worktree, "checkout", "-b", "wrong-branch")
				want = "no longer matches"
			case "advanced head":
				writeEngineFile(t, filepath.Join(r.Candidate.Worktree, "advance.txt"), "native head change\n")
				runEngineGit(t, r.Candidate.Worktree, "add", "advance.txt")
				runEngineGit(t, r.Candidate.Worktree, "commit", "-m", "test: head advance")
				want = "no longer matches"
			case "published":
				runEngineGit(t, r.Candidate.Worktree, "push", "origin", r.Candidate.Branch)
				want = "published"
			case "target exists":
				runEngineGit(t, r.Candidate.Worktree, "push", "origin", r.TargetSHA+":refs/heads/"+r.Target)
				want = "still exists"
			case "missing default":
				runEngineGit(t, f.repository.CloneURL, "symbolic-ref", "HEAD", "refs/heads/missing")
				want = "no default branch"
			case "detached default":
				runEngineGit(t, f.repository.CloneURL, "update-ref", "--no-deref", "HEAD", r.TargetSHA)
				want = "no default branch"
			case "uncontained":
				want = "not contained"
			}
			got, err := acknowledgeRetiredPrepareCandidate(t.Context(), o, readWorktreeMergeReceipt, defaultRunner, retiredcandidateack.FileSHA256, persist)
			positive := stage == "dry run" || stage == "apply replay" || stage == "winner"
			if positive {
				if err != nil || got.Candidate.SHA != r.TargetSHA || got.DefaultBranch != "main" || got.ID == "" {
					t.Fatalf("actual native proof: %+v %v", got, err)
				}
				if stage == "dry run" {
					if _, err := os.Stat(retiredcandidateack.Path(r.ReceiptPath)); !os.IsNotExist(err) {
						t.Fatalf("dryrun sidecar: %v", err)
					}
				} else {
					loaded, err := retiredcandidateack.Load(retiredcandidateack.Path(r.ReceiptPath), retiredPrepareCandidateIdentity(r, r.ReceiptPath))
					if err != nil || loaded.ID != got.ID {
						t.Fatalf("actual persisted winner invalid: %+v %v", loaded, err)
					}
				}
				if stage == "winner" && !win {
					t.Fatal("real winner path not consumed")
				}
				if stage == "apply replay" {
					again, err := AcknowledgeRetiredPrepareCandidate(t.Context(), o)
					if err != nil || again.ID != got.ID {
						t.Fatalf("native replay: %+v %v", again, err)
					}
				}
			} else {
				if err == nil || want != "" && !strings.Contains(err.Error(), want) {
					t.Fatalf("native refusal %s: %v want %s", stage, err, want)
				}
				if stage == "missing winner" && (!win || !errors.Is(err, os.ErrNotExist)) {
					t.Fatalf("winner authentication absent: %v", err)
				}
			}
			verify()
			retirementOwnerReleased(t, f.githubDir, r.Lane)
		})
	}
}
