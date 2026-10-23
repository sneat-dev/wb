//go:build e2e

package orchestrate

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestE2EProtocolBoundaryCollisionLockedShapeAndNativeCustody(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"shape", "custody"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f, o := protocolCollisionFixture(t)
			if mode == "shape" {
				o.ExpectedCurrentSourceSHA = f.receipt.TargetSHA
			} else {
				writeEngineFile(t, filepath.Join(f.receipt.Candidate.Worktree, "private-dirt.txt"), "native dirt\n")
			}
			before := protocolBoundaryBytes(t, f.receipt.ReceiptPath)
			_, err := AcknowledgeWorktreeMergeReceiptCollision(t.Context(), o)
			expected := "receipt collision sources or candidate do not match explicit expected identity"
			if mode == "custody" {
				expected = "validate collision candidate: candidate is not clean"
			}
			protocolBoundaryRefusal(t, err, expected)
			protocolUnchangedBytes(t, f.receipt.ReceiptPath, before)
			if _, err := os.Lstat(receiptCollisionAcknowledgementPath(f.receipt.ReceiptPath)); !os.IsNotExist(err) {
				t.Fatalf("refusal published acknowledgement: %v", err)
			}
		})
	}
}

func TestE2EProtocolBoundarySupersessionPolicyAndCandidateRefusals(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"replacement", "actor", "lane", "modern shape", "legacy conflict shape", "candidate", "different existing", "final publication"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f, r, rep := protocolSupersessionFixture(t)
			o := WorktreeMergeValidationFailureSupersessionOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, ReplacementWorktree: rep.WorktreeDir, Actor: "private actor", Reason: "native boundary"}
			expected := ""
			switch mode {
			case "replacement":
				o.ReplacementWorktree = ""
				expected = "replacement worktree is required"
			case "actor":
				o.Apply = true
				o.Actor = ""
				expected = "--actor and --reason are required"
			case "lane":
				r.Lane = ""
				expected = "has no lane identity"
			case "modern shape":
				r.Status = WorktreeMergePrepared
				expected = "want prepare validation_failed or unpublished conflict"
			case "legacy conflict shape":
				r.Status = WorktreeMergeConflict
				r.Candidate.SHA = ""
				r.PublishedCandidateSHA = r.TargetSHA
				expected = "legacy unpublished conflict"
			case "candidate":
				writeEngineFile(t, filepath.Join(r.Candidate.Worktree, "native-dirt.txt"), "native dirt\n")
				expected = "validate failed candidate: candidate is not clean"
			case "different existing":
				a, err := SupersedeValidationFailedWorktreeMerge(t.Context(), o)
				if err != nil {
					t.Fatal(err)
				}
				a.CurrentTargetSHA = r.TargetSHA
				a.ID = validationFailureSupersessionID(a)
				if err := persistValidationFailureSupersession(a.AcknowledgementPath, a); err != nil {
					t.Fatal(err)
				}
				expected = "binds different target or replacement evidence"
			case "final publication":
				if runtime.GOOS == "windows" {
					t.Skip("Windows chmod does not implement the Unix directory write-permission contract")
				}
				o.Apply = true

			}
			if mode == "lane" || mode == "modern shape" || mode == "legacy conflict shape" {
				if err := persistWorktreeMergeReceipt(r); err != nil {
					t.Fatal(err)
				}
			}
			before := protocolBoundaryBytes(t, r.ReceiptPath)
			hash := worktreeMergeReceiptSHA256
			consumed := false
			if mode == "final publication" {
				hash = func(path string) (string, error) {
					h, e := worktreeMergeReceiptSHA256(path)
					if e == nil && path == r.ReceiptPath {
						protocolAssertLaneHeld(t, f.githubDir, r.Lane)
						consumed = true
						// The actual writer uses replacing Rename, so a dangling
						// destination symlink is not a refusal. Deny only private
						// parent writes after native proof and honest receipt hash.
						parent := filepath.Dir(validationFailureSupersessionPath(path))
						info, err := os.Stat(parent)
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() {
							if err := os.Chmod(parent, info.Mode().Perm()); err != nil {
								t.Error(err)
							}
						})
						if err := os.Chmod(parent, 0500); err != nil {
							t.Fatal(err)
						}
					}
					return h, e
				}
			}
			_, err := supersedeValidationFailedWorktreeMergeWithRunner(t.Context(), defaultRunner, readWorktreeMergeReceipt, hash, o)
			if mode == "final publication" {
				if !errors.Is(err, os.ErrPermission) || !consumed {
					t.Fatalf("native destination refusal consumed=%v err=%v", consumed, err)
				}
				if _, statErr := os.Lstat(validationFailureSupersessionPath(r.ReceiptPath)); !os.IsNotExist(statErr) {
					t.Fatalf("failed actual writer published a supersession: %v", statErr)
				}
				temporary, globErr := filepath.Glob(filepath.Join(filepath.Dir(r.ReceiptPath), ".validation-failed-supersession-*.tmp"))
				if globErr != nil || len(temporary) != 0 {
					t.Fatalf("failed actual writer left staging files: %v %v", temporary, globErr)
				}
			} else {
				protocolBoundaryRefusal(t, err, expected)
			}
			protocolUnchangedBytes(t, r.ReceiptPath, before)
		})
	}
}

func TestE2EProtocolBoundarySupersessionPostTargetSourceObservations(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"HEAD fault", "tree fault", "tree differs", "journal"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f, r, rep := protocolSupersessionFixture(t)
			source := r.Sources[0]
			advanced := source.SHA
			if mode == "tree fault" || mode == "tree differs" {
				writeEngineFile(t, filepath.Join(source.Worktree, "post-proof-source.txt"), "native forward source\n")
				runEngineGit(t, source.Worktree, "add", "post-proof-source.txt")
				runEngineGit(t, source.Worktree, "commit", "-m", "private postproof source")
				advanced = strings.TrimSpace(runEngineGit(t, source.Worktree, "rev-parse", "HEAD"))
			}
			target := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
			targetObserved, consumed := false, false
			sentinel := errors.New("exact post-target native source observation")
			native := protocolBoundaryNativeRunner{Runner: runner.New(), after: func(dir, name string, args []string, _ runner.Result) {
				if dir == rep.WorktreeDir && name == "git" && strings.Join(args, " ") == "rev-parse --verify "+target+"^{tree}" {
					targetObserved = true
					if mode == "journal" {
						protocolBoundaryPrompts(t, source.Worktree)
						consumed = true
					}
				}
			}}
			run := prepareOwnerObservedRunner{Runner: native, before: func(_ context.Context, dir, name string, args []string) error {
				if targetObserved && !consumed && dir == source.Worktree && name == "git" && (mode == "HEAD fault" && strings.Join(args, " ") == "rev-parse --verify HEAD^{commit}" || mode == "tree fault" && strings.Join(args, " ") == "rev-parse --verify "+advanced+"^{tree}") {
					consumed = true
					return sentinel
				}
				return nil
			}}
			before := protocolBoundaryBytes(t, r.ReceiptPath)
			_, err := supersedeValidationFailedWorktreeMergeWithRunner(t.Context(), run, readWorktreeMergeReceipt, worktreeMergeReceiptSHA256, WorktreeMergeValidationFailureSupersessionOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, ReplacementWorktree: rep.WorktreeDir})
			expected := "read receipted source"
			if mode == "tree fault" {
				expected = "read advanced receipted source tree"
			}
			if mode == "tree differs" {
				expected = "differs from landed target tree"
			}
			if mode == "journal" {
				expected = "load receipted source Work Log"
			}
			protocolBoundaryRefusal(t, err, expected)
			if !targetObserved {
				t.Fatal("native target-tree stage was not reached")
			}
			if strings.HasSuffix(mode, "fault") && (!consumed || !errors.Is(err, sentinel)) {
				t.Fatalf("named fault consumed=%v error=%v", consumed, err)
			}
			if mode == "journal" && !consumed {
				t.Fatal("native post-target journal drift not installed")
			}
			protocolUnchangedBytes(t, r.ReceiptPath, before)
		})
	}
}

func TestE2EProtocolBoundaryCorrectionImmutableHistoryAndConcurrentEvidence(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"shape", "candidate", "history", "required root", "existing different", "concurrent different"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f, r, _, historical, o := protocolCorrectionFixture(t)
			o.Actor, o.Reason = "private reviewer", "native boundary"
			expected := ""
			switch mode {
			case "shape":
				r.Status = WorktreeMergePrepared
				expected = "validation_failed"
			case "candidate":
				writeEngineFile(t, filepath.Join(r.Candidate.Worktree, "native-dirt.txt"), "native dirt\n")
				expected = "validate failed candidate: candidate is not clean"
			case "history":
				r.SourceRefreshes = []WorktreeMergeSourceRefresh{{Sources: []WorktreeMergeSource{{Task: "history", Worktree: r.Sources[0].Worktree, Branch: r.Sources[0].Branch, SHA: strings.Repeat("a", 40)}}, RecordedAt: time.Now().UTC()}}
				expected = "validate immutable historical source evidence"
			case "required root":
				writeEngineFile(t, filepath.Join(f.canonical, "new-unmerged-target.txt"), "native unmerged target\n")
				runEngineGit(t, f.canonical, "add", "new-unmerged-target.txt")
				runEngineGit(t, f.canonical, "commit", "-m", "native unmerged correction target")
				runEngineGit(t, f.canonical, "push", "origin", "main")
				expected = "does not contain required immutable root"
			case "existing different", "concurrent different":
				intended, err := CorrectValidationFailedSelfSupersession(t.Context(), o)
				if err != nil {
					t.Fatal(err)
				}
				intended.Actor = "different private reviewer"
				intended.ID = selfSupersessionCorrectionID(intended)
				if mode == "existing different" {
					if err := persistSelfSupersessionCorrectionInjected(intended.CorrectionPath, intended, nil); err != nil {
						t.Fatal(err)
					}
				}
				expected = "binds different immutable evidence"
			}
			if mode == "shape" || mode == "history" {
				if err := persistWorktreeMergeReceipt(r); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "history" {
				var hashErr error
				historical.ReceiptSHA256, hashErr = worktreeMergeReceiptSHA256(r.ReceiptPath)
				if hashErr != nil {
					t.Fatal(hashErr)
				}
				historical.ID = validationFailureSupersessionID(historical)
				if err := os.Remove(historical.AcknowledgementPath); err != nil {
					t.Fatal(err)
				}
				if err := persistValidationFailureSupersession(historical.AcknowledgementPath, historical); err != nil {
					t.Fatal(err)
				}
				o.ExpectedSupersessionSHA256, hashErr = worktreeMergeReceiptSHA256(historical.AcknowledgementPath)
				if hashErr != nil {
					t.Fatal(hashErr)
				}
			}
			before := protocolBoundaryBytes(t, r.ReceiptPath)
			var inj *filewrite.Injector
			consumed := false
			if mode == "concurrent different" {
				intended, err := CorrectValidationFailedSelfSupersession(t.Context(), o)
				if err != nil {
					t.Fatal(err)
				}
				intended.Actor = "different private reviewer"
				intended.ID = selfSupersessionCorrectionID(intended)
				o.Apply = true
				inj = &filewrite.Injector{Step: filewrite.StepLink, Hook: func() {
					consumed = true
					if err := persistSelfSupersessionCorrectionInjected(intended.CorrectionPath, intended, nil); err != nil {
						t.Fatal(err)
					}
				}}
			}
			_, err := correctValidationFailedSelfSupersessionInjected(t.Context(), o, inj)
			protocolBoundaryRefusal(t, err, expected)
			if mode == "concurrent different" && !consumed {
				t.Fatal("actual atomic-link boundary not reached")
			}
			protocolUnchangedBytes(t, r.ReceiptPath, before)
		})
	}
}

func TestE2EProtocolBoundaryLegacyShapeAndNativeSourceIdentity(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"validation", "conflict"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f, r, _ := protocolSupersessionFixture(t)
			r.Candidate.SHA = ""
			if kind == "conflict" {
				r.Status = WorktreeMergeConflict
			} else {
				r.Validation.Revision = ""
			}
			var err error
			if kind == "validation" {
				_, _, _, err = resolveValidationFailedSupersessionReceiptWithRunner(t.Context(), defaultRunner, worktreeMergeReceiptSHA256, f.githubDir, r, r.ReceiptPath, "actor", "reason")
			} else {
				r.Lane = ""
				_, _, _, err = resolveLegacyConflictSupersessionReceiptWithRunner(t.Context(), defaultRunner, worktreeMergeReceiptSHA256, f.githubDir, r, r.ReceiptPath, "actor", "reason")
			}
			protocolBoundaryRefusal(t, err, "legacy")
		})
	}
	t.Run("real Guard different recorded branch", func(t *testing.T) {
		t.Parallel()
		f, r, _ := protocolSupersessionFixture(t)
		s := r.Sources[0]
		s.Branch = "feature/different-recorded-branch"
		_, _, err := validateValidationFailedSupersessionSourceWithRunner(t.Context(), defaultRunner, f.githubDir, r, s)
		protocolBoundaryRefusal(t, err, "no longer has its exact linked-worktree identity")
	})
}

func TestE2EProtocolBoundaryLegacyNativeGraphRefusals(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"validation", "conflict"} {
		modes := []string{"target absent", "source absent", "claim base object"}
		if kind == "validation" {
			modes = append(modes, "source journal")
		}
		for _, mode := range modes {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				t.Parallel()
				f, r, _ := protocolSupersessionFixture(t)
				head := r.Candidate.SHA
				r.Candidate.SHA = ""
				if kind == "conflict" {
					r.Status = WorktreeMergeConflict
				}
				expected := ""
				consumed := false
				var run runner.Runner = runner.New()
				switch mode {
				case "target absent":
					r.TargetSHA = strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
					contains, err := isMergeAncestor(t.Context(), r.Candidate.Worktree, r.TargetSHA, head)
					if err != nil || contains {
						t.Fatalf("native noncontainment preflight=%v %v", contains, err)
					}
					expected = "does not contain receipt target"
				case "source absent":
					history := append([]WorktreeMergeSource(nil), r.Sources...)
					s := r.Sources[0]
					writeEngineFile(t, filepath.Join(s.Worktree, "unmerged-source.txt"), "native unmerged source\n")
					runEngineGit(t, s.Worktree, "add", "unmerged-source.txt")
					runEngineGit(t, s.Worktree, "commit", "-m", "private unmerged source")
					r.Sources[0].SHA = strings.TrimSpace(runEngineGit(t, s.Worktree, "rev-parse", "HEAD"))
					r.SourceRefreshes = []WorktreeMergeSourceRefresh{{Sources: history, RecordedAt: time.Now().UTC()}}
					contains, err := isMergeAncestor(t.Context(), r.Candidate.Worktree, r.Sources[0].SHA, head)
					if err != nil || contains {
						t.Fatalf("native source noncontainment=%v %v", contains, err)
					}
					expected = "does not contain receipted source"
				case "source journal":
					protocolBoundaryPrompts(t, r.Sources[0].Worktree)
					expected = "load receipted source Work Log"
				case "claim base object":
					claim, err := validateMergeAcknowledgementCandidate(t.Context(), f.githubDir, r, WorktreeMergeCandidate{Task: r.Candidate.Task, Worktree: r.Candidate.Worktree, Branch: r.Candidate.Branch, SHA: head})
					if err != nil {
						t.Fatal(err)
					}
					if err := requireCandidateContainsImmutableClaimBase(t.Context(), r.Candidate.Worktree, claim.BaseSHA, head); err != nil {
						t.Fatal(err)
					}
					// Replace only the private base object's interpretation with an actual blob.
					// The requested HEAD, custody and every returned Git observation stay native.
					blob := strings.TrimSpace(runEngineGit(t, r.Candidate.Worktree, "hash-object", "-w", filepath.Join(r.Candidate.Worktree, "source.txt")))
					run = prepareOwnerObservedRunner{Runner: runner.New(), before: func(_ context.Context, dir, name string, args []string) error {
						if !consumed && dir == r.Candidate.Worktree && name == "git" && strings.Join(args, " ") == "merge-base "+claim.BaseSHA+" "+head {
							view, err := worktrees.LoadWorkLogView(t.Context(), worktrees.LoadWorkLogOptions{ProjectsRoot: f.githubDir, Worktree: dir})
							if err != nil || !mergeClaimMatchesIdentity(view.Claim, r.Repository, r.Candidate.Task, dir, r.Candidate.Branch) || view.Claim.BaseSHA != claim.BaseSHA {
								t.Fatalf("late native custody was not accepted: %+v %v", view, err)
							}
							runEngineGit(t, dir, "replace", "-f", claim.BaseSHA, blob)
							consumed = true
							t.Cleanup(func() { runEngineGit(t, dir, "replace", "-d", claim.BaseSHA) })
						}
						return nil
					}}
				}
				if err := persistWorktreeMergeReceipt(r); err != nil {
					t.Fatal(err)
				}
				before := protocolBoundaryBytes(t, r.ReceiptPath)
				var err error
				if kind == "validation" {
					_, _, _, err = resolveValidationFailedSupersessionReceiptWithRunner(t.Context(), run, worktreeMergeReceiptSHA256, f.githubDir, r, r.ReceiptPath, "actor", "reason")
				} else {
					_, _, _, err = resolveLegacyConflictSupersessionReceiptWithRunner(t.Context(), run, worktreeMergeReceiptSHA256, f.githubDir, r, r.ReceiptPath, "actor", "reason")
				}
				if mode == "claim base object" {
					if !consumed || err == nil || !strings.Contains(err.Error(), "merge-base") || strings.Contains(err.Error(), "corroborate legacy") {
						t.Fatalf("actual invalid base object consumed=%v err=%v", consumed, err)
					}
				} else {
					protocolBoundaryRefusal(t, err, expected)
				}
				protocolUnchangedBytes(t, r.ReceiptPath, before)
			})
		}
	}
}

func TestE2EProtocolBoundaryLegacyOwnerAppendOnlyPublication(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"validation", "conflict"} {
		for _, mode := range []string{"identical", "different", "malformed", "native permission"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				t.Parallel()
				if mode == "native permission" && runtime.GOOS == "windows" {
					t.Skip("Windows chmod does not implement the Unix directory write-permission contract")
				}
				f, r, rep := protocolSupersessionFixture(t)
				r.Candidate.SHA = ""
				if kind == "conflict" {
					r.Status = WorktreeMergeConflict
				}
				if err := persistWorktreeMergeReceipt(r); err != nil {
					t.Fatal(err)
				}
				o := WorktreeMergeValidationFailureSupersessionOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, ReplacementWorktree: rep.WorktreeDir, Apply: true, Actor: "private actor", Reason: "native append-only boundary"}
				var vi *WorktreeMergeLegacyValidationFailureIdentity
				var ci *WorktreeMergeLegacyConflictIdentity
				var err error
				path := ""
				if kind == "validation" {
					_, vi, _, err = resolveValidationFailedSupersessionReceiptWithRunner(t.Context(), defaultRunner, worktreeMergeReceiptSHA256, f.githubDir, r, r.ReceiptPath, o.Actor, o.Reason)
					if vi != nil {
						path = vi.AcknowledgementPath
					}
				} else {
					_, ci, _, err = resolveLegacyConflictSupersessionReceiptWithRunner(t.Context(), defaultRunner, worktreeMergeReceiptSHA256, f.githubDir, r, r.ReceiptPath, o.Actor, o.Reason)
					if ci != nil {
						path = ci.AcknowledgementPath
					}
				}
				if err != nil || path == "" {
					t.Fatalf("actual legacy correlation failed path=%q error=%v", path, err)
				}
				target := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
				targetObserved, consumed := false, false
				run := protocolBoundaryNativeRunner{Runner: runner.New(), after: func(dir, name string, args []string, _ runner.Result) {
					if dir == rep.WorktreeDir && name == "git" && strings.Join(args, " ") == "rev-parse --verify "+target+"^{tree}" {
						targetObserved = true
					}
				}}
				hash := func(p string) (string, error) {
					h, e := worktreeMergeReceiptSHA256(p)
					if e == nil && p == r.ReceiptPath && targetObserved && !consumed {
						protocolAssertLaneHeld(t, f.githubDir, r.Lane)
						consumed = true
						switch mode {
						case "malformed":
							if err := os.WriteFile(path, []byte("not JSON"), 0600); err != nil {
								t.Fatal(err)
							}
						case "native permission":
							parent := filepath.Dir(path)
							info, err := os.Stat(parent)
							if err != nil {
								t.Fatal(err)
							}
							if err := os.Chmod(parent, 0500); err != nil {
								t.Fatal(err)
							}
							t.Cleanup(func() {
								if err := os.Chmod(parent, info.Mode().Perm()); err != nil {
									t.Error(err)
								}
							})
						default:
							if kind == "validation" {
								if mode == "different" {
									vi.Actor = "other private actor"
									vi.ID = legacyValidationFailureIdentityID(*vi)
								}
								if err := persistLegacyValidationFailureIdentity(path, *vi); err != nil {
									t.Fatal(err)
								}
							} else {
								if mode == "different" {
									ci.Actor = "other private actor"
									ci.ID = legacyConflictIdentityID(*ci)
								}
								if err := persistLegacyConflictIdentity(path, *ci); err != nil {
									t.Fatal(err)
								}
							}
						}
					}
					return h, e
				}
				before := protocolBoundaryBytes(t, r.ReceiptPath)
				a, err := supersedeValidationFailedWorktreeMergeWithRunner(t.Context(), run, readWorktreeMergeReceipt, hash, o)
				if !consumed {
					t.Fatal("actual final post-proof receipt hash was not reached")
				}
				switch mode {
				case "identical":
					if err != nil || a.ID == "" {
						t.Fatalf("actual identity collision did not converge: %+v %v", a, err)
					}
				case "different":
					protocolBoundaryRefusal(t, err, "binds different immutable evidence")
				case "malformed":
					protocolBoundaryRefusal(t, err, "decode legacy")
				default:
					if !errors.Is(err, os.ErrPermission) {
						t.Fatalf("actual private directory permission refusal=%v", err)
					}
				}
				protocolUnchangedBytes(t, r.ReceiptPath, before)
			})
		}
	}
}

func TestE2EProtocolBoundaryLegacyConflictReauthenticatesRetiredHistory(t *testing.T) {
	t.Parallel()
	f, r, _ := protocolSupersessionFixture(t)
	r.Status = WorktreeMergeConflict
	r.Candidate.SHA = ""
	r.PullRequest = "https://github.com/private/protocol/pull/1"
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	h, err := worktreeMergeReceiptSHA256(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	path := retiredPublicationAcknowledgementPath(r.ReceiptPath)
	// Decoder-level historical eligibility: this record authenticates an omitted
	// candidate SHA. It is not evidence that the public retirement workflow would
	// authorize writing an empty-SHA publication record.
	a := WorktreeMergeRetiredPublicationAcknowledgement{SchemaVersion: worktreeMergeRetiredPublicationAcknowledgementSchemaVersion, Status: "retired_publication_acknowledged", ReceiptPath: r.ReceiptPath, AcknowledgementPath: path, ReceiptID: r.ID, ReceiptSHA256: h, ReceiptPhase: r.Phase, ReceiptStatus: r.Status, Lane: r.Lane, Repository: r.Repository, Target: r.Target, ReceiptTargetSHA: r.TargetSHA, CurrentTargetSHA: r.TargetSHA, CandidateTask: r.Candidate.Task, CandidateWorktree: r.Candidate.Worktree, CandidateBranch: r.Candidate.Branch, CandidateSHA: "", PullRequest: r.PullRequest, PullRequestState: "CLOSED", Sources: append([]WorktreeMergeSource(nil), r.Sources...), Actor: "historical operator", Reason: "controlled historical decoder eligibility", RecordedAt: time.Now().UTC()}
	a.ID = retiredPublicationAcknowledgementID(a)
	if err := persistRetiredPublicationAcknowledgement(path, a); err != nil {
		t.Fatal(err)
	}
	if err := validateLegacyConflictReceiptShape(r, r.ReceiptPath); err != nil {
		t.Fatalf("actual first history authentication failed: %v", err)
	}
	before := protocolBoundaryBytes(t, r.ReceiptPath)
	ackBefore := protocolBoundaryBytes(t, path)
	effective, identity, needs, err := resolveLegacyConflictSupersessionReceiptWithRunner(t.Context(), defaultRunner, worktreeMergeReceiptSHA256, f.githubDir, r, r.ReceiptPath, "actor", "reason")
	protocolBoundaryRefusal(t, err, "corroborate legacy conflict receipt: retired-publication acknowledgement")
	if effective.ID != "" || identity != nil || needs {
		t.Fatalf("reauthentication refusal returned eligibility: %+v %+v %v", effective, identity, needs)
	}
	protocolUnchangedBytes(t, r.ReceiptPath, before)
	protocolUnchangedBytes(t, path, ackBefore)
}

//nolint:paralleltest // Actual terminal Cleanup uses the existing process-wide PATH/provider fixture; independent rows retain its real private proof.
func TestE2EProtocolBoundaryCleanedCorrectionLateNativeResolutionAndHistory(t *testing.T) {
	f, r, rep, a, o := protocolCorrectionFixture(t)
	o.Apply, o.Actor, o.Reason = true, "private reviewer", "native cleaned boundary"
	c, err := CorrectValidationFailedSelfSupersession(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, f.canonical, "update-ref", "refs/heads/main", c.CorrectedReplacement.SHA)
	runEngineGit(t, f.canonical, "push", "origin", "main")
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_REMOTE", f.repository.CloneURL)
	out, err := worktrees.Cleanup(t.Context(), worktrees.CleanupOptions{ProjectsRoot: f.githubDir, Tasks: []string{c.CorrectedReplacement.Task, r.Sources[0].Task}, Base: r.Target, Apply: true})
	if err != nil || len(out.Results) != 2 || !out.Results[0].Applied || !out.Results[1].Applied || out.ReportPath == "" {
		t.Fatalf("native terminal cleanup=%+v %v", out, err)
	}
	if _, err := os.Stat(rep.WorktreeDir); !os.IsNotExist(err) {
		t.Fatalf("replacement was not really cleaned: %v", err)
	}
	before := protocolBoundaryBytes(t, r.ReceiptPath)
	for _, mode := range []string{"canonical native resolution", "historical source", "recorded target absent"} {
		//nolint:paralleltest // Process-wide environment changes in TestE2EProtocolBoundaryCleanedCorrectionLateNativeResolutionAndHistory, installWorktreeMergeDirectGH; these rows share their parent environment and remain sequential.
		t.Run(mode, func(t *testing.T) {
			copyReceipt := r
			canonical := worktrees.CanonicalRepositoryPath
			consumed := false
			expected := ""
			switch mode {
			case "canonical native resolution":
				if runtime.GOOS == "windows" {
					t.Skip("native self-symlink fault requires Unix symlink creation privileges")
				}
				expected = "resolve cleaned corrected self-supersession canonical repository"
				canonical = func(root, repository string) (string, error) {
					if root != f.githubDir || repository != r.Repository {
						t.Fatalf("late native query changed identity: %q %q", root, repository)
					}
					consumed = true
					held := root + ".private-canonical-held"
					if err := os.Rename(root, held); err != nil {
						t.Fatal(err)
					}
					restored := false
					restore := func() {
						if restored {
							return
						}
						if err := os.Remove(root); err != nil && !os.IsNotExist(err) {
							t.Error(err)
						}
						if err := os.Rename(held, root); err != nil {
							t.Error(err)
						} else {
							restored = true
						}
					}
					t.Cleanup(restore)
					if err := os.Symlink(root, root); err != nil {
						restore()
						t.Fatal(err)
					}
					path, queryErr := worktrees.CanonicalRepositoryPath(root, repository)
					restore()
					if queryErr == nil {
						t.Fatalf("actual self-symlink query unexpectedly succeeded: %q", path)
					}
					return path, queryErr
				}
			case "historical source":
				alternative := strings.TrimSpace(runEngineGit(t, f.canonical, "commit-tree", r.TargetSHA+"^{tree}", "-p", r.TargetSHA, "-m", "private unretained historical source"))
				old := r.Sources[0]
				old.SHA = alternative
				copyReceipt.SourceRefreshes = []WorktreeMergeSourceRefresh{{Sources: []WorktreeMergeSource{old}, RecordedAt: time.Now().UTC()}}
				expected = "validate cleaned corrected self-supersession historical source"
			case "recorded target absent":
				runEngineGit(t, f.canonical, "push", "--force", "origin", r.TargetSHA+":refs/heads/main")
				t.Cleanup(func() {
					runEngineGit(t, f.canonical, "push", "--force", "origin", c.CorrectedReplacement.SHA+":refs/heads/main")
				})
				contains, e := isMergeAncestor(t.Context(), f.canonical, c.CurrentTargetSHA, r.TargetSHA)
				if e != nil || contains {
					t.Fatalf("actual older target noncontainment=%v %v", contains, e)
				}
				expected = "is not a descendant of recorded target"
			}
			err := validateCleanedSelfSupersessionReplacementWithRunner(t.Context(), defaultRunner, canonical, f.githubDir, copyReceipt, a, c)
			protocolBoundaryRefusal(t, err, expected)
			if mode == "canonical native resolution" && !consumed {
				t.Fatal("actual terminal proof did not reach late canonical query")
			}
			protocolUnchangedBytes(t, r.ReceiptPath, before)
		})
	}
}

func TestE2EProtocolBoundaryImmutableClaimBaseNativeWrapperAndRunner(t *testing.T) {
	t.Parallel()
	f := newExplicitRootEngineFixture(t)
	base := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
	candidate := strings.TrimSpace(runEngineGit(t, f.canonical, "commit-tree", base+"^{tree}", "-p", base, "-m", "native private candidate descendant"))
	sibling := strings.TrimSpace(runEngineGit(t, f.canonical, "commit-tree", base+"^{tree}", "-p", base, "-m", "native private unrelated sibling"))
	t.Run("native positive wrapper", func(t *testing.T) {
		t.Parallel()
		if err := requireCandidateContainsImmutableClaimBase(t.Context(), f.canonical, base, candidate); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("native false wrapper", func(t *testing.T) {
		t.Parallel()
		err := requireCandidateContainsImmutableClaimBase(t.Context(), f.canonical, sibling, candidate)
		protocolBoundaryRefusal(t, err, "candidate "+candidate+" does not contain immutable claim base "+sibling)
	})
	t.Run("exact negative runner", func(t *testing.T) {
		t.Parallel()
		sentinel := errors.New("exact late immutable claim base query")
		consumed := false
		run := prepareOwnerObservedRunner{Runner: runner.New(), before: func(_ context.Context, dir, name string, args []string) error {
			if dir == f.canonical && name == "git" && strings.Join(args, " ") == "merge-base "+base+" "+candidate {
				consumed = true
				return sentinel
			}
			return nil
		}}
		err := requireCandidateContainsImmutableClaimBaseWithRunner(t.Context(), run, f.canonical, base, candidate)
		if !consumed || !errors.Is(err, sentinel) {
			t.Fatalf("exact negative query consumed=%v error=%v", consumed, err)
		}
	})
}
