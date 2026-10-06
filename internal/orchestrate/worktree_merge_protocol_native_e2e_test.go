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
	"strings"
	"testing"
)

func TestE2ECollisionProtocolNativeEvidenceAndExactRefusals(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"dry run", "apply replay", "receipt digest", "claim digest", "published branch", "target rewind", "remote observation", "fetch observation", "root observation", "uncontained root", "existing malformed"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f, o := protocolCollisionFixture(t)
			r := f.receipt
			before, err := os.ReadFile(r.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			path := receiptCollisionAcknowledgementPath(r.ReceiptPath)
			sentinel := errors.New("private collision native observation")
			observed := false
			var run runner.Runner = runner.New()
			switch mode {
			case "receipt digest":
				o.ExpectedReceiptSHA256 = "wrong"
			case "claim digest":
				o.ExpectedImmutableClaimSHA256 = "wrong"
			case "published branch":
				runEngineGit(t, r.Candidate.Worktree, "push", "origin", r.Candidate.SHA+":refs/heads/"+r.Candidate.Branch)
			case "target rewind":
				o.ExpectedTargetSHA = r.Candidate.SHA
			case "uncontained root":
				r.SourceRefreshes[0].Sources[0].SHA = strings.TrimSpace(runEngineGit(t, r.Candidate.Worktree, "commit-tree", r.TargetSHA+"^{tree}", "-p", r.TargetSHA, "-m", "unrelated private historical root"))
				o.ExpectedHistoricalRefreshSourceSHA = r.SourceRefreshes[0].Sources[0].SHA
				if err := persistWorktreeMergeReceipt(r); err != nil {
					t.Fatal(err)
				}
				before, err = os.ReadFile(r.ReceiptPath)
				if err != nil {
					t.Fatal(err)
				}
				o.ExpectedReceiptSHA256, err = worktreeMergeReceiptSHA256(r.ReceiptPath)
				if err != nil {
					t.Fatal(err)
				}
			case "existing malformed":
				if err := os.WriteFile(path, []byte("not JSON\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "remote observation", "fetch observation", "root observation":
				run = prepareOwnerObservedRunner{Runner: runner.New(), before: func(_ context.Context, dir, name string, args []string) error {
					if dir != r.Candidate.Worktree || name != "git" {
						return nil
					}
					argv := strings.Join(args, " ")
					reject := mode == "remote observation" && argv == "ls-remote --heads origin refs/heads/"+r.Candidate.Branch || mode == "fetch observation" && argv == "fetch --no-tags origin +refs/heads/main:refs/remotes/origin/main" || mode == "root observation" && argv == "merge-base "+r.SourceRefreshes[0].Sources[0].SHA+" "+r.Candidate.SHA
					if reject && !observed {
						observed = true
						return sentinel
					}
					return nil
				}}
			}
			if mode == "apply replay" {
				o.Apply = true
				o.Actor = "private reviewer"
				o.Reason = "exact native collision evidence"
			}
			a, err := acknowledgeWorktreeMergeReceiptCollisionWithRunner(t.Context(), run, readWorktreeMergeReceipt, worktreeMergeReceiptSHA256, o, nil)
			if mode == "dry run" || mode == "apply replay" {
				if err != nil || a.ReceiptSHA256 != o.ExpectedReceiptSHA256 || a.ImmutableClaimSHA256 != o.ExpectedImmutableClaimSHA256 {
					t.Fatalf("native collision %s=%+v, %v", mode, a, err)
				}
				if mode == "dry run" {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatalf("dry run wrote acknowledgement: %v", err)
					}
				} else {
					again, err := AcknowledgeWorktreeMergeReceiptCollision(t.Context(), o)
					if err != nil || again.ID != a.ID {
						t.Fatalf("native immutable collision replay=%+v, %v", again, err)
					}
				}
			} else {
				if err == nil {
					t.Fatalf("native collision accepted %s", mode)
				}
				if strings.HasSuffix(mode, "observation") && (!observed || !errors.Is(err, sentinel)) {
					t.Fatalf("exact native %s error identity=%v", mode, err)
				}
				if mode == "uncontained root" && !strings.Contains(err.Error(), "does not contain required root") {
					t.Fatalf("actual disconnected native root diagnostic=%v", err)
				}
				if mode != "existing malformed" {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatalf("refusal wrote acknowledgement: %v", err)
					}
				}
			}
			protocolUnchangedBytes(t, r.ReceiptPath, before)
		})
	}
}

func TestE2EProtocolLateNativeReadAndHashFailuresPreserveImmutableBytes(t *testing.T) {
	t.Parallel()
	for _, owner := range []string{"collision", "supersession", "correction"} {
		for _, stage := range []string{"initial read", "held reread", "receipt hash", "claim hash"} {
			if owner == "supersession" && stage == "claim hash" {
				continue
			}
			t.Run(owner+"/"+stage, func(t *testing.T) {
				t.Parallel()
				var root string
				var r WorktreeMergeReceipt
				var collision WorktreeMergeReceiptCollisionAcknowledgementOptions
				var replacement WorktreeMergeValidationFailureSupersessionOptions
				var correction WorktreeMergeSelfSupersessionCorrectionOptions
				switch owner {
				case "collision":
					f, o := protocolCollisionFixture(t)
					root, r, collision = f.engine.githubDir, f.receipt, o
				case "supersession":
					f, receipt, rep := protocolSupersessionFixture(t)
					root, r = f.githubDir, receipt
					replacement = WorktreeMergeValidationFailureSupersessionOptions{ProjectsRoot: root, Receipt: r.ReceiptPath, ReplacementWorktree: rep.WorktreeDir}
				case "correction":
					f, receipt, _, _, o := protocolCorrectionFixture(t)
					root, r, correction = f.githubDir, receipt, o
				}
				before, err := os.ReadFile(r.ReceiptPath)
				if err != nil {
					t.Fatal(err)
				}
				claim, err := validateMergeAcknowledgementCandidate(t.Context(), root, r, r.Candidate)
				if err != nil {
					t.Fatal(err)
				}
				sentinel := errors.New("named private " + stage)
				consumed := false
				read := func(path string) (WorktreeMergeReceipt, error) {
					if path == r.ReceiptPath && stage == "initial read" {
						consumed = true
						return WorktreeMergeReceipt{}, sentinel
					}
					if path == r.ReceiptPath && stage == "held reread" {
						lock, lockErr := AcquireOperationLock(root, r.Lane, true)
						if lockErr == nil {
							if err := lock.Release(); err != nil {
								t.Fatal(err)
							}
						} else {
							protocolAssertLaneHeld(t, root, r.Lane)
							consumed = true
							return WorktreeMergeReceipt{}, sentinel
						}
					}
					return readWorktreeMergeReceipt(path)
				}
				hash := func(path string) (string, error) {
					if stage == "receipt hash" && path == r.ReceiptPath || stage == "claim hash" && path == claim.ClaimPath {
						protocolAssertLaneHeld(t, root, r.Lane)
						consumed = true
						return "", sentinel
					}
					return worktreeMergeReceiptSHA256(path)
				}
				switch owner {
				case "collision":
					_, err = acknowledgeWorktreeMergeReceiptCollisionWithRunner(t.Context(), runner.New(), read, hash, collision, nil)
				case "supersession":
					_, err = supersedeValidationFailedWorktreeMergeWithRunner(t.Context(), runner.New(), read, hash, replacement)
				case "correction":
					_, err = correctValidationFailedSelfSupersessionWithRunner(t.Context(), runner.New(), read, hash, correction, nil)
				}
				if !consumed || !errors.Is(err, sentinel) {
					t.Fatalf("named %s %s observation consumed=%v error=%v", owner, stage, consumed, err)
				}
				protocolUnchangedBytes(t, r.ReceiptPath, before)
				path := validationFailureSupersessionPath(r.ReceiptPath)
				if owner == "collision" {
					path = receiptCollisionAcknowledgementPath(r.ReceiptPath)
				}
				if owner == "correction" {
					path = selfSupersessionCorrectionPath(r.ReceiptPath)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("negative observation published %s: %v", path, err)
				}
			})
		}
	}
}

func TestE2EProtocolNativeAppendOnlyPublicationAndConcurrentEvidence(t *testing.T) {
	t.Parallel()
	for _, owner := range []string{"collision", "correction"} {
		for _, mode := range []string{"permission", "concurrent identical", "concurrent different", "concurrent malformed"} {
			t.Run(owner+"/"+mode, func(t *testing.T) {
				t.Parallel()
				var receipt WorktreeMergeReceipt
				var path string
				var intendedCollision WorktreeMergeReceiptCollisionAcknowledgement
				var intendedCorrection WorktreeMergeSelfSupersessionCorrection
				var collision WorktreeMergeReceiptCollisionAcknowledgementOptions
				var correction WorktreeMergeSelfSupersessionCorrectionOptions
				var err error
				if owner == "collision" {
					f, o := protocolCollisionFixture(t)
					receipt, collision = f.receipt, o
					collision.Actor, collision.Reason = "private reviewer", "append-only collision"
					intendedCollision, err = AcknowledgeWorktreeMergeReceiptCollision(t.Context(), collision)
					path = receiptCollisionAcknowledgementPath(receipt.ReceiptPath)
				} else {
					_, r, _, _, o := protocolCorrectionFixture(t)
					receipt, correction = r, o
					correction.Actor, correction.Reason = "private reviewer", "append-only correction"
					intendedCorrection, err = CorrectValidationFailedSelfSupersession(t.Context(), correction)
					path = selfSupersessionCorrectionPath(receipt.ReceiptPath)
				}
				if err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadFile(receipt.ReceiptPath)
				if err != nil {
					t.Fatal(err)
				}
				consumed := false
				inj := &filewrite.Injector{Step: filewrite.StepLink}
				if mode == "permission" {
					inj.Err = os.ErrPermission
				} else {
					inj.Hook = func() {
						consumed = true
						if mode == "concurrent malformed" {
							if err := os.WriteFile(path, []byte("not JSON"), 0600); err != nil {
								t.Fatal(err)
							}
							return
						}
						if owner == "collision" {
							if mode == "concurrent different" {
								intendedCollision.Actor = "different private reviewer"
								intendedCollision.ID = receiptCollisionAcknowledgementID(intendedCollision)
							}
							if err := persistReceiptCollisionAcknowledgementInjected(path, intendedCollision, nil); err != nil {
								t.Fatal(err)
							}
						} else {
							if mode == "concurrent different" {
								intendedCorrection.CurrentTargetSHA = receipt.Candidate.SHA
								intendedCorrection.ID = selfSupersessionCorrectionID(intendedCorrection)
							}
							if err := persistSelfSupersessionCorrectionInjected(path, intendedCorrection, nil); err != nil {
								t.Fatal(err)
							}
						}
					}
				}
				if owner == "collision" {
					collision.Apply = true
					_, err = acknowledgeWorktreeMergeReceiptCollisionInjected(t.Context(), collision, inj)
				} else {
					correction.Apply = true
					_, err = correctValidationFailedSelfSupersessionInjected(t.Context(), correction, inj)
				}
				if mode == "concurrent identical" {
					if err != nil || !consumed {
						t.Fatalf("actual identical publication did not converge: %v consumed=%v", err, consumed)
					}
				} else {
					if err == nil {
						t.Fatal("append-only publication ignored refusal")
					}
					if mode == "permission" && !errors.Is(err, os.ErrPermission) {
						t.Fatalf("native writer error identity=%v", err)
					}
					if mode != "permission" && !consumed {
						t.Fatal("concurrent real link boundary not reached")
					}
				}
				protocolUnchangedBytes(t, receipt.ReceiptPath, before)
			})
		}
	}
}

func TestE2ESupersessionProtocolNativeReplacementEvidenceAndRefusals(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"dry run", "apply replay", "same candidate", "invalid replacement", "dirty source", "missing source", "source claim", "source journal", "source HEAD error", "source clean error", "source base error", "source ancestry error", "target fetch error", "target tree error", "required root error", "existing malformed"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f, r, rep := protocolSupersessionFixture(t)
			o := WorktreeMergeValidationFailureSupersessionOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, ReplacementWorktree: rep.WorktreeDir}
			before, err := os.ReadFile(r.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			path := validationFailureSupersessionPath(r.ReceiptPath)
			sentinel := errors.New("exact native supersession observation")
			consumed := false
			var run runner.Runner = runner.New()
			source := r.Sources[0]
			switch mode {
			case "same candidate":
				o.ReplacementWorktree = r.Candidate.Worktree
			case "invalid replacement":
				o.ReplacementWorktree = f.canonical
			case "dirty source":
				writeEngineFile(t, filepath.Join(source.Worktree, "dirty.txt"), "private dirt\n")
			case "missing source":
				runEngineGit(t, f.canonical, "worktree", "remove", "--force", source.Worktree)
			case "source claim":
				r.Sources[0].Task = "wrong-task"
				if err := persistWorktreeMergeReceipt(r); err != nil {
					t.Fatal(err)
				}
				before, err = os.ReadFile(r.ReceiptPath)
				if err != nil {
					t.Fatal(err)
				}
			case "source journal":
				prompts := filepath.Join(source.Worktree, ".wb", "local", "prompts")
				if err := os.RemoveAll(prompts); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(prompts, []byte("native ENOTDIR\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "existing malformed":
				if err := os.WriteFile(path, []byte("not JSON"), 0600); err != nil {
					t.Fatal(err)
				}
			default:
				if strings.HasSuffix(mode, "error") {
					view, err := worktrees.LoadWorkLogView(t.Context(), worktrees.LoadWorkLogOptions{ProjectsRoot: f.githubDir, Worktree: source.Worktree})
					if err != nil {
						t.Fatal(err)
					}
					run = prepareOwnerObservedRunner{Runner: runner.New(), before: func(_ context.Context, dir, name string, args []string) error {
						if name != "git" || consumed {
							return nil
						}
						argv := strings.Join(args, " ")
						reject := dir == source.Worktree && (mode == "source HEAD error" && argv == "rev-parse --verify HEAD^{commit}" || mode == "source clean error" && argv == "status --porcelain=v1" || mode == "source base error" && argv == "merge-base "+view.Claim.BaseSHA+" "+source.SHA || mode == "source ancestry error" && argv == "merge-base "+source.SHA+" "+source.SHA)
						reject = reject || dir == rep.WorktreeDir && (mode == "target fetch error" && argv == "fetch --no-tags origin +refs/heads/main:refs/remotes/origin/main" || mode == "target tree error" && len(args) == 3 && args[0] == "rev-parse" && args[1] == "--verify" && strings.HasSuffix(args[2], "^{tree}") || mode == "required root error" && argv == "merge-base "+r.TargetSHA+" "+strings.TrimSpace(runEngineGit(t, rep.WorktreeDir, "rev-parse", "HEAD")))
						if reject {
							consumed = true
							return sentinel
						}
						return nil
					}}
				}
			}
			if mode == "apply replay" {
				o.Apply, o.Actor, o.Reason = true, "private reviewer", "native distinct replacement"
			}
			a, err := supersedeValidationFailedWorktreeMergeWithRunner(t.Context(), run, readWorktreeMergeReceipt, worktreeMergeReceiptSHA256, o)
			if mode == "dry run" || mode == "apply replay" {
				if err != nil || a.OriginalCandidate != r.Candidate || a.Replacement.Worktree != rep.WorktreeDir || a.Replacement == r.Candidate {
					t.Fatalf("actual replacement %s=%+v %v", mode, a, err)
				}
				if mode == "apply replay" {
					again, err := SupersedeValidationFailedWorktreeMerge(t.Context(), o)
					if err != nil || again.ID != a.ID {
						t.Fatalf("native replacement replay=%+v %v", again, err)
					}
				} else if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("dry run publication: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("native replacement accepted %s", mode)
				}
				if strings.HasSuffix(mode, "error") && (!consumed || !errors.Is(err, sentinel)) {
					t.Fatalf("named %s error=%v consumed=%v", mode, err, consumed)
				}
				if mode != "existing malformed" {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatalf("refusal publication: %v", err)
					}
				}
			}
			protocolUnchangedBytes(t, r.ReceiptPath, before)
		})
	}
}

func TestE2ECorrectionProtocolNativeDistinctReplacementAndExactRefusals(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"dry run", "apply replay", "supersession digest", "claim digest", "same candidate", "replacement dirt", "supersession missing", "supersession malformed", "supersession shape", "receipt digest", "existing malformed", "fetch error", "root error"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f, r, rep, a, o := protocolCorrectionFixture(t)
			before, err := os.ReadFile(r.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			path := selfSupersessionCorrectionPath(r.ReceiptPath)
			sentinel := errors.New("named private correction observation")
			consumed := false
			var run runner.Runner = runner.New()
			hash := worktreeMergeReceiptSHA256
			switch mode {
			case "supersession digest":
				o.ExpectedSupersessionSHA256 = "wrong"
			case "claim digest":
				o.ExpectedImmutableClaimSHA256 = "wrong"
			case "same candidate":
				o.ReplacementWorktree = r.Candidate.Worktree
			case "replacement dirt":
				writeEngineFile(t, filepath.Join(rep.WorktreeDir, "dirty.txt"), "private dirt\n")
			case "supersession missing":
				if err := os.Remove(a.AcknowledgementPath); err != nil {
					t.Fatal(err)
				}
			case "supersession malformed":
				if err := os.WriteFile(a.AcknowledgementPath, []byte("not JSON"), 0600); err != nil {
					t.Fatal(err)
				}
			case "supersession shape":
				a.ReplacementClaimBaseSHA = strings.TrimSpace(runEngineGit(t, rep.WorktreeDir, "rev-parse", "HEAD"))
				a.ID = validationFailureSupersessionID(a)
				if err := os.Remove(a.AcknowledgementPath); err != nil {
					t.Fatal(err)
				}
				if err := persistValidationFailureSupersession(a.AcknowledgementPath, a); err != nil {
					t.Fatal(err)
				}
			case "receipt digest":
				hash = func(path string) (string, error) {
					if path == r.ReceiptPath {
						protocolAssertLaneHeld(t, f.githubDir, r.Lane)
						consumed = true
						actual, err := os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(path, append(actual, '\n'), 0600); err != nil {
							t.Fatal(err)
						}
						before, err = os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
						return worktreeMergeReceiptSHA256(path)
					}
					return worktreeMergeReceiptSHA256(path)
				}
			case "existing malformed":
				if err := os.WriteFile(path, []byte("not JSON"), 0600); err != nil {
					t.Fatal(err)
				}
			case "fetch error", "root error":
				head := strings.TrimSpace(runEngineGit(t, rep.WorktreeDir, "rev-parse", "HEAD"))
				run = prepareOwnerObservedRunner{Runner: runner.New(), before: func(_ context.Context, dir, name string, args []string) error {
					argv := strings.Join(args, " ")
					if !consumed && name == "git" && dir == rep.WorktreeDir && (mode == "fetch error" && argv == "fetch --no-tags origin +refs/heads/main:refs/remotes/origin/main" || mode == "root error" && argv == "merge-base "+r.TargetSHA+" "+head) {
						consumed = true
						return sentinel
					}
					return nil
				}}
			}
			if mode == "apply replay" {
				o.Apply, o.Actor, o.Reason = true, "private reviewer", "actual distinct replacement"
			}
			c, err := correctValidationFailedSelfSupersessionWithRunner(t.Context(), run, readWorktreeMergeReceipt, hash, o, nil)
			if mode == "dry run" || mode == "apply replay" {
				if err != nil || c.CorrectedReplacement.Worktree != rep.WorktreeDir || c.CorrectedReplacement == r.Candidate {
					t.Fatalf("native correction %s=%+v %v", mode, c, err)
				}
				if mode == "apply replay" {
					again, err := CorrectValidationFailedSelfSupersession(t.Context(), o)
					if err != nil || again.ID != c.ID {
						t.Fatalf("native correction replay=%+v %v", again, err)
					}
					if err := validateSelfSupersessionCorrection(t.Context(), f.githubDir, r, a); err != nil {
						t.Fatalf("native effective correction: %v", err)
					}
				} else if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("dry run publication: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("native correction accepted %s", mode)
				}
				if strings.HasSuffix(mode, "error") && (!consumed || !errors.Is(err, sentinel)) {
					t.Fatalf("named native %s error=%v consumed=%v", mode, err, consumed)
				}
				if mode == "receipt digest" && (!consumed || !strings.Contains(err.Error(), "receipt bytes no longer match")) {
					t.Fatalf("late receipt identity refusal=%v consumed=%v", err, consumed)
				}
				if mode != "existing malformed" {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatalf("refusal publication: %v", err)
					}
				}
			}
			protocolUnchangedBytes(t, r.ReceiptPath, before)
		})
	}
}

func TestE2ELegacyProtocolNativeCorrelationPreservesReceiptAndIdentity(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"validation", "conflict"} {
		for _, mode := range []string{"native dry run", "native apply replay", "HEAD error", "HEAD mismatch", "custody refusal", "target ancestry error", "source ancestry error", "fetch error", "landing ancestry error", "already landed", "hash error", "malformed identity", "different identity"} {
			if kind == "conflict" && mode == "HEAD mismatch" {
				continue
			}
			t.Run(kind+"/"+mode, func(t *testing.T) {
				t.Parallel()
				f, r, rep := protocolSupersessionFixture(t)
				candidateSHA := r.Candidate.SHA
				r.Candidate.SHA = ""
				if kind == "conflict" {
					r.Status = WorktreeMergeConflict
				}
				if mode == "HEAD mismatch" {
					r.Validation.Revision = r.TargetSHA
				}
				if err := persistWorktreeMergeReceipt(r); err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadFile(r.ReceiptPath)
				if err != nil {
					t.Fatal(err)
				}
				sentinel := errors.New("exact legacy native observation")
				consumed := false
				var run runner.Runner = runner.New()
				hash := worktreeMergeReceiptSHA256
				identityPath := legacyValidationFailureIdentityPath(r.ReceiptPath)
				if kind == "conflict" {
					identityPath = legacyConflictIdentityPath(r.ReceiptPath)
				}
				currentTarget := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
				switch mode {
				case "custody refusal":
					writeEngineFile(t, filepath.Join(r.Candidate.Worktree, "dirt.txt"), "private custody refusal\n")
				case "already landed":
					runEngineGit(t, f.canonical, "merge", "--no-edit", candidateSHA)
					runEngineGit(t, f.canonical, "push", "origin", "main")
				case "hash error":
					hash = func(path string) (string, error) {
						if path == r.ReceiptPath {
							consumed = true
							return "", sentinel
						}
						return worktreeMergeReceiptSHA256(path)
					}
				case "malformed identity":
					if err := os.WriteFile(identityPath, []byte("not JSON"), 0600); err != nil {
						t.Fatal(err)
					}
				default:
					if strings.HasSuffix(mode, "error") {
						argv := ""
						switch mode {
						case "HEAD error":
							argv = "rev-parse --verify HEAD^{commit}"
						case "target ancestry error":
							argv = "merge-base " + r.TargetSHA + " " + candidateSHA
						case "source ancestry error":
							argv = "merge-base " + r.Sources[0].SHA + " " + candidateSHA
						case "fetch error":
							argv = "fetch --no-tags origin +refs/heads/main:refs/remotes/origin/main"
						case "landing ancestry error":
							argv = "merge-base " + candidateSHA + " " + currentTarget
						}
						run = prepareOwnerObservedRunner{Runner: runner.New(), before: func(_ context.Context, dir, name string, args []string) error {
							if !consumed && dir == r.Candidate.Worktree && name == "git" && strings.Join(args, " ") == argv {
								consumed = true
								return sentinel
							}
							return nil
						}}
					}
				}
				if mode == "different identity" {
					if kind == "validation" {
						_, id, needs, err := resolveValidationFailedSupersessionReceiptWithRunner(t.Context(), defaultRunner, worktreeMergeReceiptSHA256, f.githubDir, r, r.ReceiptPath, "private reviewer", "native legacy evidence")
						if err != nil || !needs || id == nil {
							t.Fatalf("native identity setup=%+v %v", id, err)
						}
						id.CurrentTargetSHA = r.TargetSHA
						id.ID = legacyValidationFailureIdentityID(*id)
						if err := persistLegacyValidationFailureIdentity(identityPath, *id); err != nil {
							t.Fatal(err)
						}
					} else {
						_, id, needs, err := resolveLegacyConflictSupersessionReceiptWithRunner(t.Context(), defaultRunner, worktreeMergeReceiptSHA256, f.githubDir, r, r.ReceiptPath, "private reviewer", "native legacy evidence")
						if err != nil || !needs || id == nil {
							t.Fatalf("native identity setup=%+v %v", id, err)
						}
						id.CurrentTargetSHA = r.TargetSHA
						id.ID = legacyConflictIdentityID(*id)
						if err := persistLegacyConflictIdentity(identityPath, *id); err != nil {
							t.Fatal(err)
						}
					}
				}
				var effective WorktreeMergeReceipt
				var needs bool
				if kind == "validation" {
					effective, _, needs, err = resolveValidationFailedSupersessionReceiptWithRunner(t.Context(), run, hash, f.githubDir, r, r.ReceiptPath, "private reviewer", "native legacy evidence")
				} else {
					effective, _, needs, err = resolveLegacyConflictSupersessionReceiptWithRunner(t.Context(), run, hash, f.githubDir, r, r.ReceiptPath, "private reviewer", "native legacy evidence")
				}
				if strings.HasPrefix(mode, "native") {
					if err != nil || !needs || effective.Candidate.SHA != candidateSHA {
						t.Fatalf("native legacy correlation=%+v needs=%v error=%v", effective, needs, err)
					}
					if mode == "native apply replay" {
						o := WorktreeMergeValidationFailureSupersessionOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, ReplacementWorktree: rep.WorktreeDir, Apply: true, Actor: "private reviewer", Reason: "native legacy evidence"}
						a, err := SupersedeValidationFailedWorktreeMerge(t.Context(), o)
						if err != nil || a.OriginalCandidate.SHA != candidateSHA {
							t.Fatalf("actual legacy owner apply=%+v %v", a, err)
						}
						if _, err := os.Stat(identityPath); err != nil {
							t.Fatalf("native identity not published: %v", err)
						}
						again, err := SupersedeValidationFailedWorktreeMerge(t.Context(), o)
						if err != nil || again.ID != a.ID {
							t.Fatalf("native legacy owner replay=%+v %v", again, err)
						}
					} else if _, err := os.Stat(identityPath); !os.IsNotExist(err) {
						t.Fatalf("native dry run wrote identity: %v", err)
					}
				} else {
					if err == nil {
						t.Fatalf("legacy %s accepted %s", kind, mode)
					}
					if strings.HasSuffix(mode, "error") && (!consumed || !errors.Is(err, sentinel)) {
						t.Fatalf("named legacy %s error=%v consumed=%v", mode, err, consumed)
					}
				}
				protocolUnchangedBytes(t, r.ReceiptPath, before)
			})
		}
	}
}

func TestE2ECorrectionProtocolLiveNativeValidationRetainsRecordedRoots(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"native", "malformed correction", "original dirty", "replacement dirty", "replacement missing", "replacement HEAD error", "recorded ancestry error", "fetch error", "target advance", "target ancestry error", "target rewind", "required root error", "changed claim hash"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f, r, rep, a, o := protocolCorrectionFixture(t)
			o.Apply, o.Actor, o.Reason = true, "private reviewer", "native correction validation"
			c, err := CorrectValidationFailedSelfSupersession(t.Context(), o)
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(r.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			correctionBefore, err := os.ReadFile(c.CorrectionPath)
			if err != nil {
				t.Fatal(err)
			}
			sentinel := errors.New("named live correction native observation")
			consumed := false
			var run runner.Runner = runner.New()
			current := c.CurrentTargetSHA
			switch mode {
			case "malformed correction":
				if err := os.WriteFile(c.CorrectionPath, []byte("not JSON"), 0600); err != nil {
					t.Fatal(err)
				}
			case "original dirty":
				writeEngineFile(t, filepath.Join(r.Candidate.Worktree, "dirty.txt"), "native original dirt\n")
			case "replacement dirty":
				writeEngineFile(t, filepath.Join(rep.WorktreeDir, "dirty.txt"), "native replacement dirt\n")
			case "replacement missing":
				runEngineGit(t, f.canonical, "worktree", "remove", "--force", rep.WorktreeDir)
			case "changed claim hash":
				claim, err := validateMergeAcknowledgementCandidate(t.Context(), f.githubDir, r, r.Candidate)
				if err != nil {
					t.Fatal(err)
				}
				bytes, err := os.ReadFile(claim.ClaimPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(claim.ClaimPath, append(bytes, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			case "target advance", "target ancestry error":
				writeEngineFile(t, filepath.Join(f.canonical, "later.txt"), "native target descendant\n")
				runEngineGit(t, f.canonical, "add", "later.txt")
				runEngineGit(t, f.canonical, "commit", "-m", "private later target")
				runEngineGit(t, f.canonical, "push", "origin", "main")
				current = strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
			case "target rewind":
				runEngineGit(t, f.canonical, "push", "--force", "origin", r.TargetSHA+":refs/heads/main")
			}
			if strings.HasSuffix(mode, "error") {
				argv := ""
				switch mode {
				case "replacement HEAD error":
					argv = "rev-parse --verify HEAD^{commit}"
				case "recorded ancestry error":
					argv = "merge-base " + c.CorrectedReplacement.SHA + " " + c.CorrectedReplacement.SHA
				case "fetch error":
					argv = "fetch --no-tags origin +refs/heads/main:refs/remotes/origin/main"
				case "target ancestry error":
					argv = "merge-base " + c.CurrentTargetSHA + " " + current
				case "required root error":
					argv = "merge-base " + r.TargetSHA + " " + c.CorrectedReplacement.SHA
				}
				run = prepareOwnerObservedRunner{Runner: runner.New(), before: func(_ context.Context, dir, name string, args []string) error {
					if !consumed && dir == rep.WorktreeDir && name == "git" && strings.Join(args, " ") == argv {
						consumed = true
						return sentinel
					}
					return nil
				}}
			}
			err = validateSelfSupersessionCorrectionWithRunner(t.Context(), run, worktreeMergeReceiptSHA256, f.githubDir, r, a)
			if mode == "native" || mode == "target advance" {
				if err != nil {
					t.Fatalf("native live correction %s: %v", mode, err)
				}
			} else {
				if err == nil {
					t.Fatalf("live correction accepted %s", mode)
				}
				if strings.HasSuffix(mode, "error") && (!consumed || !errors.Is(err, sentinel)) {
					t.Fatalf("named %s consumed=%v error=%v", mode, consumed, err)
				}
			}
			protocolUnchangedBytes(t, r.ReceiptPath, before)
			if mode != "malformed correction" {
				protocolUnchangedBytes(t, c.CorrectionPath, correctionBefore)
			}
		})
	}
}

//nolint:paralleltest // Actual Cleanup's existing GH fixture changes PATH and provider env; the native terminal proof is shared read-only across serial rows.
func TestE2ECorrectionProtocolCleanedNativeValidationRequiresLandingAndHistory(t *testing.T) {
	f, r, rep, a, o := protocolCorrectionFixture(t)
	o.Apply, o.Actor, o.Reason = true, "private reviewer", "native terminal correction"
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
		t.Fatalf("actual native terminal cleanup=%+v %v", out, err)
	}
	if _, err := os.Stat(rep.WorktreeDir); !os.IsNotExist(err) {
		t.Fatalf("actual cleanup retained replacement: %v", err)
	}
	before, err := os.ReadFile(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"native wrapper", "fetch error", "target ancestry error", "landing error", "required root error", "missing proof", "uncontained landing", "uncontained root"} {
		//nolint:paralleltest // Process-wide environment changes in TestE2ECorrectionProtocolCleanedNativeValidationRequiresLandingAndHistory, installWorktreeMergeDirectGH; these rows share their parent environment and remain sequential.
		t.Run(mode, func(t *testing.T) {
			// These rows retain the actual terminal proof; only negative Git observations or record inputs differ.
			copyReceipt, copyCorrection := r, c
			var run runner.Runner = runner.New()
			sentinel := errors.New("named cleaned correction observation")
			consumed := false
			switch mode {
			case "missing proof":
				copyCorrection.CorrectedReplacement.Task = "no-terminal-proof"
			case "uncontained landing":
				// The fetched target is a real earlier commit, so native merge-base proves cleanup head is not landed.
				runEngineGit(t, f.canonical, "push", "--force", "origin", c.CurrentTargetSHA+":refs/heads/main")
				t.Cleanup(func() {
					runEngineGit(t, f.canonical, "push", "--force", "origin", c.CorrectedReplacement.SHA+":refs/heads/main")
				})
			case "uncontained root":
				copyCorrection.ReplacementClaimBaseSHA = strings.TrimSpace(runEngineGit(t, f.canonical, "commit-tree", r.TargetSHA+"^{tree}", "-p", r.TargetSHA, "-m", "real uncontained correction root"))
			case "target ancestry error":
				copyCorrection.CurrentTargetSHA = r.TargetSHA
			}
			if strings.HasSuffix(mode, "error") {
				argv := ""
				switch mode {
				case "fetch error":
					argv = "fetch --no-tags origin +refs/heads/main:refs/remotes/origin/main"
				case "target ancestry error":
					argv = "merge-base " + copyCorrection.CurrentTargetSHA + " " + c.CorrectedReplacement.SHA
				case "landing error":
					argv = "merge-base " + c.CorrectedReplacement.SHA + " " + c.CorrectedReplacement.SHA
				case "required root error":
					argv = "merge-base " + r.TargetSHA + " " + c.CorrectedReplacement.SHA
				}
				run = prepareOwnerObservedRunner{Runner: runner.New(), before: func(_ context.Context, dir, name string, args []string) error {
					if !consumed && dir == f.canonical && name == "git" && strings.Join(args, " ") == argv {
						consumed = true
						return sentinel
					}
					return nil
				}}
			}
			if mode == "native wrapper" {
				err = validateCleanedSelfSupersessionReplacementWithRunner(t.Context(), defaultRunner, worktrees.CanonicalRepositoryPath, f.githubDir, copyReceipt, a, copyCorrection)
			} else {
				err = validateCleanedSelfSupersessionReplacementWithRunner(t.Context(), run, worktrees.CanonicalRepositoryPath, f.githubDir, copyReceipt, a, copyCorrection)
			}
			if mode == "native wrapper" {
				if err != nil {
					t.Fatalf("actual terminal proof refused: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("cleaned correction accepted %s", mode)
				}
				if strings.HasSuffix(mode, "error") && (!consumed || !errors.Is(err, sentinel)) {
					t.Fatalf("named cleaned %s consumed=%v error=%v", mode, consumed, err)
				}
			}
			protocolUnchangedBytes(t, r.ReceiptPath, before)
		})
	}
}

func TestE2EProtocolActualLaneLocksRefuseConcurrentOwnerWithoutPublication(t *testing.T) {
	t.Parallel()
	for _, owner := range []string{"collision", "supersession", "correction"} {
		t.Run(owner, func(t *testing.T) {
			t.Parallel()
			var root string
			var r WorktreeMergeReceipt
			var collision WorktreeMergeReceiptCollisionAcknowledgementOptions
			var replacement WorktreeMergeValidationFailureSupersessionOptions
			var correction WorktreeMergeSelfSupersessionCorrectionOptions
			switch owner {
			case "collision":
				f, o := protocolCollisionFixture(t)
				root, r, collision = f.engine.githubDir, f.receipt, o
			case "supersession":
				f, receipt, rep := protocolSupersessionFixture(t)
				root, r = f.githubDir, receipt
				replacement = WorktreeMergeValidationFailureSupersessionOptions{ProjectsRoot: root, Receipt: r.ReceiptPath, ReplacementWorktree: rep.WorktreeDir}
			case "correction":
				f, receipt, _, _, o := protocolCorrectionFixture(t)
				root, r, correction = f.githubDir, receipt, o
			}
			before, err := os.ReadFile(r.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			lock, err := AcquireOperationLock(root, r.Lane, true)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := lock.Release(); err != nil {
					t.Error(err)
				}
			})
			switch owner {
			case "collision":
				_, err = AcknowledgeWorktreeMergeReceiptCollision(t.Context(), collision)
			case "supersession":
				_, err = SupersedeValidationFailedWorktreeMerge(t.Context(), replacement)
			case "correction":
				_, err = CorrectValidationFailedSelfSupersession(t.Context(), correction)
			}
			if err == nil || !strings.Contains(err.Error(), "already active") {
				t.Fatalf("actual parallel owner not refused: %v", err)
			}
			protocolUnchangedBytes(t, r.ReceiptPath, before)
		})
	}
}
