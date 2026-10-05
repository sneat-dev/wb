package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPublishedOwnerImmutableSourceProofUsesActualGitIdentities(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"empty candidate", "missing candidate", "abbreviated candidate", "incomplete source", "missing source", "abbreviated source", "side source", "exact source"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			fixture := newExplicitRootEngineFixture(t)
			head := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
			source := WorktreeMergeSource{Task: "native", Worktree: fixture.canonical, Branch: "main", SHA: head}
			r := WorktreeMergeReceipt{Candidate: WorktreeMergeCandidate{SHA: head}, Sources: []WorktreeMergeSource{source}}
			want := ""
			switch stage {
			case "empty candidate":
				r.Candidate.SHA = ""
				want = "no immutable candidate SHA"
			case "missing candidate":
				r.Candidate.SHA = strings.Repeat("f", 40)
				want = "not an available exact commit"
			case "abbreviated candidate":
				r.Candidate.SHA = head[:8]
				want = "not an available exact commit"
			case "incomplete source":
				r.Sources[0].Task = ""
				want = "incomplete immutable historical source identity"
			case "missing source":
				r.Sources[0].SHA = strings.Repeat("f", 40)
				want = "not an available exact commit"
			case "abbreviated source":
				r.Sources[0].SHA = head[:8]
				want = "not an available exact commit"
			case "side source":
				tree := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", head+"^{tree}"))
				r.Sources[0].SHA = strings.TrimSpace(runEngineGit(t, fixture.canonical, "commit-tree", tree, "-p", head, "-m", "test: independent source descendant"))
				want = "is not an ancestor of failed candidate"
			}
			err := requireImmutableHistoricalWorktreeMergeSources(context.Background(), fixture.canonical, r)
			if want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("native immutable proof=%v want %s", err, want)
			}
			if after := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")); after != head {
				t.Fatal("read-only identity proof advanced native HEAD")
			}
		})
	}
}

func TestPublishedOwnerRootMergesUseNativeGitAndAbortConflicts(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"empty duplicate contained", "missing HEAD", "missing root", "clean merge", "conflict abort"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			fixture := newExplicitRootEngineFixture(t)
			head := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
			dir := fixture.canonical
			roots := []WorktreeMergeValidationFailureSealRoot{{Kind: "empty"}, {Kind: "contained", SHA: head}, {Kind: "duplicate", SHA: head}}
			want := ""
			switch stage {
			case "missing HEAD":
				dir = t.TempDir()
				roots = []WorktreeMergeValidationFailureSealRoot{{Kind: "native", SHA: head}}
				want = ""
			case "missing root":
				roots = []WorktreeMergeValidationFailureSealRoot{{Kind: "missing", SHA: strings.Repeat("f", 40)}}
			case "clean merge", "conflict abort":
				runEngineGit(t, dir, "checkout", "-b", "owner-root")
				writeEngineFile(t, filepath.Join(dir, "owner-root.txt"), "root contents\n")
				runEngineGit(t, dir, "add", "owner-root.txt")
				runEngineGit(t, dir, "commit", "-m", "test: owned merge root")
				rootSHA := strings.TrimSpace(runEngineGit(t, dir, "rev-parse", "HEAD"))
				runEngineGit(t, dir, "checkout", "main")
				if stage == "conflict abort" {
					writeEngineFile(t, filepath.Join(dir, "owner-root.txt"), "different head contents\n")
					runEngineGit(t, dir, "add", "owner-root.txt")
					runEngineGit(t, dir, "commit", "-m", "test: independent conflicting target")
					head = strings.TrimSpace(runEngineGit(t, dir, "rev-parse", "HEAD"))
					want = "merge required native root " + rootSHA
				}
				roots = []WorktreeMergeValidationFailureSealRoot{{Kind: "native", SHA: rootSHA}, {Kind: "duplicate", SHA: rootSHA}}
			}
			err := mergePublishedForwardRepairRootsWithRunner(context.Background(), defaultRunner, dir, roots, 5*time.Second, 0)
			if stage == "empty duplicate contained" || stage == "clean merge" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || want != "" && !strings.Contains(err.Error(), want) {
				t.Fatalf("native merge refusal=%v want %s", err, want)
			}
			if stage == "conflict abort" {
				if after := strings.TrimSpace(runEngineGit(t, dir, "rev-parse", "HEAD")); after != head {
					t.Fatal("failed merge changed native HEAD")
				}
				if status := strings.TrimSpace(runEngineGit(t, dir, "status", "--porcelain=v1")); status != "" {
					t.Fatalf("native conflict was not aborted: %s", status)
				}
				if _, statErr := os.Stat(filepath.Join(dir, ".git", "MERGE_HEAD")); !os.IsNotExist(statErr) {
					t.Fatalf("merge state retained: %v", statErr)
				}
			}
			if stage == "clean merge" {
				if content, readErr := os.ReadFile(filepath.Join(dir, "owner-root.txt")); readErr != nil || string(content) != "root contents\n" {
					t.Fatalf("genuine merged bytes=%q %v", content, readErr)
				}
			}
		})
	}
}

func TestPublishedOwnerHostedIdentityComesFromActualCandidateOrigin(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"local fixture", "local without historical identity", "github renamed identity", "different host", "malformed origin", "missing origin"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			fixture := newExplicitRootEngineFixture(t)
			r := WorktreeMergeReceipt{Repository: fixture.repository.Slug, Candidate: WorktreeMergeCandidate{Worktree: fixture.canonical}}
			want := r.Repository
			wantErr := ""
			switch stage {
			case "local without historical identity":
				r.Repository = ""
				wantErr = "candidate origin is not a github.com repository"
			case "github renamed identity":
				runEngineGit(t, fixture.canonical, "remote", "set-url", "origin", "git@github.com:renamed/actual-repository.git")
				want = "renamed/actual-repository"
			case "different host":
				runEngineGit(t, fixture.canonical, "remote", "set-url", "origin", "git@gitlab.com:team/repository.git")
				wantErr = "candidate origin is not a github.com repository"
			case "malformed origin":
				runEngineGit(t, fixture.canonical, "remote", "set-url", "origin", "https://github.com/")
				wantErr = "parse candidate origin"
			case "missing origin":
				runEngineGit(t, fixture.canonical, "remote", "remove", "origin")
				wantErr = "read candidate origin"
			}
			got, err := hostedRepositoryForCandidate(context.Background(), defaultRunner, r)
			if wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), wantErr) || got != "" {
					t.Fatalf("origin refusal=%q %v", got, err)
				}
			} else if err != nil || got != want {
				t.Fatalf("actual origin identity=%q %v want %q", got, err, want)
			}
		})
	}
}

func TestPublishedOwnerTemporalReplayRefusesActualEvidenceChanges(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"before creation", "after construction"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			for _, stage := range []string{"receipt removal", "receipt digest", "immutable claim", "supersession removal", "malformed correction", "dirty current source", "remote target"} {
				t.Run(stage, func(t *testing.T) {
					t.Parallel()
					fixture, receipt, supersession, options := publishedForwardRepairFixtureWithFixture(t, newExplicitRootEngineFixture(t))
					before, err := os.ReadFile(receipt.ReceiptPath)
					if err != nil {
						t.Fatal(err)
					}
					originalClaim, err := validateMergeAcknowledgementCandidate(context.Background(), fixture.githubDir, receipt, receipt.Candidate)
					if err != nil {
						t.Fatal(err)
					}
					claimBefore, err := os.ReadFile(originalClaim.ClaimPath)
					if err != nil {
						t.Fatal(err)
					}
					ackBefore, err := os.ReadFile(supersession.AcknowledgementPath)
					if err != nil {
						t.Fatal(err)
					}
					observed := false
					want := ""
					change := func() {
						observed = true
						switch stage {
						case "receipt removal":
							if err := os.Remove(receipt.ReceiptPath); err != nil {
								t.Fatal(err)
							}
							want = "failed receipt changed"
						case "receipt digest":
							if err := os.WriteFile(receipt.ReceiptPath, append(append([]byte(nil), before...), '\n'), 0o600); err != nil {
								t.Fatal(err)
							}
							want = "failed receipt SHA256 changed"
						case "immutable claim":
							if err := os.WriteFile(originalClaim.ClaimPath, append(append([]byte(nil), claimBefore...), '\n'), 0o600); err != nil {
								t.Fatal(err)
							}
							want = "immutable failed candidate claim changed"
						case "supersession removal":
							if err := os.Remove(supersession.AcknowledgementPath); err != nil {
								t.Fatal(err)
							}
							want = ""
						case "malformed correction":
							if err := os.WriteFile(selfSupersessionCorrectionPath(receipt.ReceiptPath), []byte("{broken"), 0o600); err != nil {
								t.Fatal(err)
							}
							want = "self-supersession correction changed"
						case "dirty current source":
							writeEngineFile(t, filepath.Join(options.Sources[1], "temporal-dirty.txt"), "owned native dirty source\n")
							want = "revalidate current repair source identity"
						case "remote target":
							writeEngineFile(t, filepath.Join(fixture.canonical, "temporal-target.txt"), "owned native target advancement\n")
							runEngineGit(t, fixture.canonical, "add", "temporal-target.txt")
							runEngineGit(t, fixture.canonical, "commit", "-m", "test: advance target at exact temporal boundary")
							runEngineGit(t, fixture.canonical, "push", "origin", "main")
							want = "remote target drifted"
						}
					}
					var beforeCreate, beforeFinal func()
					if boundary == "before creation" {
						beforeCreate = change
					} else {
						beforeFinal = change
					}
					result, err := preparePublishedValidationFailureForwardRepair(context.Background(), options, defaultRunner, worktreeMergeReceiptSHA256, os.ReadFile, beforeCreate, beforeFinal)
					if !observed || err == nil || want != "" && !strings.Contains(err.Error(), want) {
						t.Fatalf("temporal %s/%s refusal=%+v %v observed=%v", boundary, stage, result, err, observed)
					}
					if stage == "supersession removal" && !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("removed native acknowledgement identity lost: %v", err)
					}
					assertNoPublishedForwardRepairCandidate(t, fixture, receipt, options)
					if stage != "receipt removal" && stage != "receipt digest" {
						if actual, readErr := os.ReadFile(receipt.ReceiptPath); readErr != nil || string(actual) != string(before) {
							t.Fatalf("historical receipt changed beyond selected mutation: %v", readErr)
						}
					}
					if stage != "immutable claim" {
						if actual, readErr := os.ReadFile(originalClaim.ClaimPath); readErr != nil || string(actual) != string(claimBefore) {
							t.Fatalf("historical claim changed beyond selected mutation: %v", readErr)
						}
					}
					if stage != "supersession removal" {
						if actual, readErr := os.ReadFile(supersession.AcknowledgementPath); readErr != nil || string(actual) != string(ackBefore) {
							t.Fatalf("historical acknowledgement changed beyond selected mutation: %v", readErr)
						}
					}
				})
			}
		})
	}
}
