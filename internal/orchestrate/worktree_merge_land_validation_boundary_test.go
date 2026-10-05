package orchestrate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/runner"
)

// These faults change only the private candidate. Every positive Git and
// custody observation is made by the production native owners.
func TestLandValidationNativeCustodyRefusalsDoNotPublish(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"interrupted dirty", "failed dirty", "failed HEAD error", "failed HEAD drift", "advanced journal", "malformed advance record"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f, r := landOwnerNativeFixture(t)
			r.Status = WorktreeMergeValidationFailed
			o := WorktreeMergeLandOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Timeout: 5 * time.Second}
			want := ""
			sentinel := errors.New("private failed-candidate HEAD observation")
			var observed *landOwnerFaultRunner
			switch mode {
			case "interrupted dirty", "failed dirty":
				if mode == "interrupted dirty" {
					r.Status = WorktreeMergePreparing
				}
				writeEngineFile(t, filepath.Join(r.Candidate.Worktree, "uncommitted-boundary.txt"), "actual uncommitted candidate\n")
				want = "dirty"
			case "failed HEAD error":
				observed = &landOwnerFaultRunner{Runner: runner.New(), failure: sentinel, refuse: func(dir, name string, args []string) bool {
					return dir == r.Candidate.Worktree && name == "git" && strings.Join(args, " ") == "rev-parse --verify HEAD^{commit}"
				}}
				o.run = observed
			case "failed HEAD drift":
				writeEngineFile(t, filepath.Join(r.Candidate.Worktree, "head-drift.txt"), "native descendant\n")
				runEngineGit(t, r.Candidate.Worktree, "add", "head-drift.txt")
				runEngineGit(t, r.Candidate.Worktree, "commit", "-m", "private candidate drift before failed validation resume")
				want = "candidate head drifted"
			case "advanced journal":
				r.Status = WorktreeMergeConflict
				writeEngineFile(t, filepath.Join(r.Candidate.Worktree, "advance.txt"), "native advance\n")
				runEngineGit(t, r.Candidate.Worktree, "add", "advance.txt")
				runEngineGit(t, r.Candidate.Worktree, "commit", "-m", "private conflict descendant")
				prompts := filepath.Join(r.Candidate.Worktree, ".wb", "local", "prompts")
				if err := os.RemoveAll(prompts); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(prompts, []byte("private native ENOTDIR\n"), 0600); err != nil {
					t.Fatal(err)
				}
				want = "Work Log"
			case "malformed advance record":
				r.Status = WorktreeMergePrepared
				if err := os.WriteFile(conflictCandidateAdvancePath(r.ReceiptPath), []byte("not JSON\n"), 0600); err != nil {
					t.Fatal(err)
				}
				want = "decode"
			}
			if err := persistWorktreeMergeReceipt(r); err != nil {
				t.Fatal(err)
			}
			got, err := LandWorktreeMerge(t.Context(), o)
			if err == nil || got.Status != WorktreeMergeConflict || got.Candidate != r.Candidate {
				t.Fatalf("native %s refusal=%+v, %v", mode, got, err)
			}
			if observed != nil {
				if !observed.refused || !errors.Is(err, sentinel) {
					t.Fatalf("exact negative observation lost: %v", err)
				}
			} else if !strings.Contains(err.Error(), want) {
				t.Fatalf("native %s diagnostic=%v, want %q", mode, err, want)
			}
			stored, readErr := readWorktreeMergeReceipt(r.ReceiptPath)
			if readErr != nil || stored.Failure != err.Error() || stored.PublishedCandidateSHA != "" {
				t.Fatalf("durable primary refusal=%+v, %v", stored, readErr)
			}
			if remote := strings.TrimSpace(runEngineGit(t, f.repository.CloneURL, "rev-parse", "refs/heads/main")); remote != r.TargetSHA {
				t.Fatalf("refusal changed actual target: %s", remote)
			}
			lock, lockErr := AcquireOperationLock(f.githubDir, r.Lane, true)
			if lockErr != nil {
				t.Fatalf("refusal retained lock: %v", lockErr)
			}
			if err := lock.Release(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

//nolint:paralleltest // Real policy provider fixtures change PATH/XDG_STATE_HOME; each row still owns its mutable Git and receipt roots.
func TestLandValidationNativePolicyAndCompletionBoundaries(t *testing.T) {
	for _, stage := range []string{"recovered", "interrupted", "failed", "advanced", "preserved", "land phase", "stale deferral", "rebased"} {
		for _, failure := range []bool{true, false} {
			name := stage + " completion"
			if failure {
				name = stage + " policy refusal"
			}
			//nolint:paralleltest // Each row installs an actual subprocess policy provider with process-wide environment.
			t.Run(name, func(t *testing.T) {
				f, r := landOwnerNativeFixture(t)
				o := WorktreeMergeLandOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Route: WorktreeMergeRouteDirect, AllowUnfenced: true, ValidateLocally: true, Timeout: 5 * time.Second, PrepareTimeout: time.Minute}
				if stage == "stale deferral" {
					installWorktreeMergeDeferralGH(t, `{"protected":true,"protection":{"required_pull_request_reviews":{},"required_status_checks":{}}}`, `{"strict":true,"contexts":["CI"],"checks":[]}`, `[]`)
					plan, planErr := resolveWorktreeMergeValidationPlan(t.Context(), r.Repository, r.Target, WorktreeMergeRoutePullRequest, false, false)
					if planErr != nil || !plan.Defer {
						t.Fatalf("actual fenced deferral plan=%+v, %v", plan, planErr)
					}
					if err := applyOrDeferWorktreeMergeValidation(t.Context(), &r, plan, 5*time.Second, 0, 0, 0, nil); err != nil {
						t.Fatal(err)
					}
					if r.ValidationDeferral == nil || r.ValidationDeferral.CandidateSHA != r.Candidate.SHA || r.Validation.Status != quality.StatusSkipped {
						t.Fatalf("actual deferred record=%+v", r.ValidationDeferral)
					}
				}
				installWorktreeMergeDirectGH(t)
				// Committed malformed repository policy is a real native validation
				// refusal, not a substituted successful validator or runner result.
				if failure {
					writeEngineFile(t, filepath.Join(r.Candidate.Worktree, ".wb", "quality.yaml"), "version: [invalid\n")
					runEngineGit(t, r.Candidate.Worktree, "add", ".wb/quality.yaml")
					runEngineGit(t, r.Candidate.Worktree, "commit", "-m", "private malformed quality policy")
					if stage != "advanced" {
						r.Candidate.SHA = strings.TrimSpace(runEngineGit(t, r.Candidate.Worktree, "rev-parse", "HEAD"))
					}
				}
				expectedPhase := ""
				expectedPrefix := ""
				switch stage {
				case "recovered":
					r.Status = WorktreeMergeConflict
					r.Candidate.SHA = ""
					expectedPhase = "recover_candidate"
					expectedPrefix = "recovered candidate validation failed"
				case "interrupted":
					r.Status = WorktreeMergePreparing
					expectedPhase = "validate_candidate"
					expectedPrefix = "interrupted candidate validation failed"
				case "failed":
					r.Status = WorktreeMergeValidationFailed
					expectedPhase = "revalidate_candidate"
					expectedPrefix = "resumed validation_failed candidate re-validation failed"
				case "advanced":
					r.Status = WorktreeMergeConflict
					if !failure {
						writeEngineFile(t, filepath.Join(r.Candidate.Worktree, "advance.txt"), "native conflict advance\n")
						runEngineGit(t, r.Candidate.Worktree, "add", "advance.txt")
						runEngineGit(t, r.Candidate.Worktree, "commit", "-m", "private conflict candidate advance")
					}
					// This recovery validates after the actual native advance record.
					expectedPrefix = "advanced conflict candidate validation failed"
				case "preserved":
					o.Route = WorktreeMergeRoutePullRequest
					o.StopBeforeMerge = true
					expectedPhase = "validate_preserved_candidate"
					expectedPrefix = "preserved candidate validation failed"
					r.ValidationIdentity = nil
				case "land phase":
					r.Phase = WorktreeMergePhaseLand
					r.Status = WorktreeMergePrepared
					r.ValidationIdentity = nil
					r.Validation.Revision = ""
					expectedPhase = "revalidate_candidate"
					expectedPrefix = "resumed land-phase candidate re-validation failed"
				case "stale deferral":
					expectedPhase = "revalidate_candidate"
					expectedPrefix = "stale deferral must be re-validated locally"
				case "rebased":
					writeEngineFile(t, filepath.Join(f.canonical, "new-target.txt"), "actual additive target advance\n")
					runEngineGit(t, f.canonical, "add", "new-target.txt")
					runEngineGit(t, f.canonical, "commit", "-m", "private target advance before rebase validation")
					runEngineGit(t, f.canonical, "push", "origin", "main")
					expectedPhase = "validate_rebased_candidate"
					expectedPrefix = "candidate validation failed after incorporating target drift"
				}
				if err := persistWorktreeMergeReceipt(r); err != nil {
					t.Fatal(err)
				}
				nativeTarget := strings.TrimSpace(runEngineGit(t, f.repository.CloneURL, "rev-parse", "refs/heads/main"))
				before, err := os.ReadFile(r.ReceiptPath)
				if err != nil {
					t.Fatal(err)
				}
				entered := false
				completed := false
				o.Progress = func(e progress.Event) {
					if expectedPhase != "" && e.Phase == expectedPhase {
						if e.State == progress.Started {
							entered = true
						}
						if e.State == progress.Completed {
							completed = true
						}
					}
					if stage == "advanced" && e.Phase == "recover_candidate" {
						if e.State == progress.Started {
							entered = true
						}
						if e.State == progress.Completed {
							completed = true
						}
					}
				}
				lastDurable := before
				sentinel := errors.New("private completed-validation write checkpoint")
				refused := false
				save := func(next WorktreeMergeReceipt) error {
					// Stop after actual validation, before any external publication.
					ready := next.Status == WorktreeMergePrepared && next.Validation.Revision == next.Candidate.SHA && next.ValidationDeferral == nil
					if !failure && ready && (stage == "recovered" || stage == "interrupted" || stage == "advanced" || stage == "land phase" && entered || stage == "stale deferral" && entered || next.Phase == WorktreeMergePhaseLand) {
						refused = true
						return sentinel
					}
					if err := persistWorktreeMergeReceipt(next); err != nil {
						return err
					}
					var readErr error
					lastDurable, readErr = os.ReadFile(next.ReceiptPath)
					if readErr != nil {
						t.Fatalf("read actual delegated checkpoint: %v", readErr)
					}
					return nil
				}
				got, err := landWorktreeMerge(t.Context(), o, save)
				if failure {
					if err == nil || !strings.Contains(err.Error(), expectedPrefix) || !strings.Contains(err.Error(), "load candidate quality policy") || got.Status != WorktreeMergeValidationFailed {
						t.Fatalf("native %s validation refusal=%+v, %v", stage, got, err)
					}
					stored, readErr := readWorktreeMergeReceipt(r.ReceiptPath)
					if readErr != nil || stored.Status != got.Status || stored.Failure != err.Error() {
						t.Fatalf("durable native validation failure=%+v, %v", stored, readErr)
					}
				} else {
					if !refused || !errors.Is(err, sentinel) || got.Status != WorktreeMergePrepared || got.Validation.Revision != got.Candidate.SHA || got.ValidationDeferral != nil {
						t.Fatalf("actual %s validation completion=%+v, %v refused=%v", stage, got, err, refused)
					}
					if stage == "failed" && !completed {
						t.Fatal("native failed candidate did not complete re-validation before route persistence")
					}
					if stage == "preserved" && !completed {
						t.Fatal("native preserved candidate did not complete identity-miss validation")
					}
					if stage == "rebased" && got.Rebase == nil {
						t.Fatal("actual rebase was not recorded")
					}
					after, readErr := os.ReadFile(r.ReceiptPath)
					if readErr != nil || string(after) != string(lastDurable) {
						t.Fatalf("refused validation write changed last actual durable checkpoint: %v", readErr)
					}
				}
				if expectedPhase != "" && !entered {
					t.Fatalf("named %s native validation stage was not entered", stage)
				}
				if remote := strings.TrimSpace(runEngineGit(t, f.repository.CloneURL, "rev-parse", "refs/heads/main")); remote != nativeTarget {
					t.Fatalf("validation boundary changed actual target: %s", remote)
				}
				if remote := strings.TrimSpace(runEngineGit(t, f.repository.CloneURL, "for-each-ref", "--format=%(refname)", "refs/heads/"+got.Candidate.Branch)); remote != "" {
					t.Fatalf("validation boundary published candidate: %s", remote)
				}
			})
		}
	}
}

//nolint:paralleltest // Existing native acknowledgement fixtures use the true default projects-root and subprocess provider environment.
func TestLandValidationAppendOnlyAcknowledgementsRefuseReplay(t *testing.T) {
	//nolint:paralleltest // Native landed fixture pins process-wide root/provider inputs.
	t.Run("landed failure", func(t *testing.T) {
		f, _, r, _ := landedTerminalCleanupFixture(t)
		r.Validation.Status = quality.StatusFailed
		if err := persistWorktreeMergeReceipt(r); err != nil {
			t.Fatal(err)
		}
		ack, err := AcknowledgeLandedMergeFailure(t.Context(), WorktreeMergeLandedFailureAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Apply: true, Actor: "private reviewer", Reason: "actual exact candidate already landed despite failed local validation"})
		if err != nil {
			t.Fatal(err)
		}
		landValidationAssertAuditReplay(t, f, r, ack.AcknowledgementPath, "acknowledged as a historical landed failure")
	})
	//nolint:paralleltest // Genuine supersession fixture uses default-root selection and actual private replacement Git.
	t.Run("validation supersession", func(t *testing.T) {
		f, r, replacement := supersessionFixture(t)
		ack, err := SupersedeValidationFailedWorktreeMerge(t.Context(), WorktreeMergeValidationFailureSupersessionOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir, Apply: true, Actor: "private reviewer", Reason: "actual preserved replacement"})
		if err != nil {
			t.Fatal(err)
		}
		landValidationAssertAuditReplay(t, f, r, ack.AcknowledgementPath, "superseded by an audited replacement")
	})
}

func landValidationAssertAuditReplay(t *testing.T, f engineFixture, r WorktreeMergeReceipt, path, want string) {
	t.Helper()
	before, err := os.ReadFile(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := LandWorktreeMerge(t.Context(), WorktreeMergeLandOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath})
	if err == nil || !strings.Contains(err.Error(), want) || got.Candidate != r.Candidate {
		t.Fatalf("actual append-only replay refusal=%+v, %v", got, err)
	}
	after, readErr := os.ReadFile(r.ReceiptPath)
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("replay rewrote receipt: %v", readErr)
	}
	retained, readErr := os.ReadFile(path)
	if readErr != nil || string(retained) != string(audit) {
		t.Fatalf("replay rewrote immutable audit: %v", readErr)
	}
}

//nolint:paralleltest // Authentic missing-cleanup producer fixture pins the default root and subprocess provider; no live machine state is touched.
func TestLandValidationMissingCleanupRequiresEveryAssetAbsent(t *testing.T) {
	f, _, r, claims := landedTerminalCleanupFixture(t)
	intent := WorktreeMergeLandOptions{Cleanup: true, OnFailure: "stop"}
	retainWorktreeMergeLandIntent(&r, &intent)
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	externallyTerminalizeMergeCleanup(t, f, &r)
	if err := os.Remove(terminalWorkLogPath(claims[r.Sources[0].Task])); err != nil {
		t.Fatal(err)
	}
	ack, err := AcknowledgeMissingWorktreeMergeCleanup(t.Context(), WorktreeMergeMissingCleanupAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Apply: true, Actor: "private reviewer", Reason: "native assets actually gone without one terminal record"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateMissingCleanupAcknowledgement(t.Context(), f.githubDir, r, ack.AcknowledgementPath, 0, 0); err != nil {
		t.Fatalf("actual immutable acknowledgement must be valid before paths reappear: %v", err)
	}
	before, err := os.ReadFile(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := os.ReadFile(ack.AcknowledgementPath)
	if err != nil {
		t.Fatal(err)
	}
	// Both actual paths reappear. No successful custody or Git admin records
	// are invented: the native Lstat existence check alone must refuse cleanup.
	for _, path := range []string{r.Candidate.Worktree, r.Sources[0].Worktree} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	got, err := LandWorktreeMerge(t.Context(), WorktreeMergeLandOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Cleanup: true})
	if err == nil || !strings.Contains(err.Error(), "did not prove every cleanup asset terminal") || got.Status != WorktreeMergeLanded {
		t.Fatalf("native all-live asset recovery=%+v, %v", got, err)
	}
	after, readErr := os.ReadFile(r.ReceiptPath)
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("live asset refusal rewrote receipt: %v", readErr)
	}
	retained, readErr := os.ReadFile(ack.AcknowledgementPath)
	if readErr != nil || string(retained) != string(audit) {
		t.Fatalf("live asset refusal rewrote audit: %v", readErr)
	}
}

//nolint:paralleltest // The existing subprocess provider binds process-wide PATH/XDG_STATE_HOME; each native candidate and receipt is private.
func TestLandValidationLandPhaseChecksCustodyAfterIntentWrite(t *testing.T) {
	for _, mode := range []string{"cleanliness observation", "HEAD observation", "native HEAD drift", "stale deferral cleanliness"} {
		//nolint:paralleltest // Actual policy provider fixture requires process-wide environment.
		t.Run(mode, func(t *testing.T) {
			f, r := landOwnerNativeFixture(t)
			if mode == "stale deferral cleanliness" {
				installWorktreeMergeDeferralGH(t, `{"protected":true,"protection":{"required_pull_request_reviews":{},"required_status_checks":{}}}`, `{"strict":true,"contexts":["CI"],"checks":[]}`, `[]`)
				plan, planErr := resolveWorktreeMergeValidationPlan(t.Context(), r.Repository, r.Target, WorktreeMergeRoutePullRequest, false, false)
				if planErr != nil || !plan.Defer {
					t.Fatalf("native stale-deferral precondition=%+v, %v", plan, planErr)
				}
				if err := applyOrDeferWorktreeMergeValidation(t.Context(), &r, plan, 5*time.Second, 0, 0, 0, nil); err != nil {
					t.Fatal(err)
				}
			} else {
				r.Phase = WorktreeMergePhaseLand
				r.Validation.Revision = ""
				r.ValidationIdentity = nil
			}
			installWorktreeMergeDirectGH(t)
			if err := persistWorktreeMergeReceipt(r); err != nil {
				t.Fatal(err)
			}
			o := WorktreeMergeLandOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Route: WorktreeMergeRouteDirect, AllowUnfenced: true, ValidateLocally: true, Timeout: 5 * time.Second}
			intentWritten := false
			sentinel := errors.New("private post-intent native observation refusal")
			observed := &landOwnerFaultRunner{Runner: runner.New(), failure: sentinel, refuse: func(dir, name string, args []string) bool {
				if !intentWritten || dir != r.Candidate.Worktree || name != "git" {
					return false
				}
				argv := strings.Join(args, " ")
				return (mode == "cleanliness observation" || mode == "stale deferral cleanliness") && argv == "status --porcelain=v1" || mode == "HEAD observation" && argv == "rev-parse --verify HEAD^{commit}"
			}}
			o.run = observed
			save := func(next WorktreeMergeReceipt) error {
				if err := persistWorktreeMergeReceipt(next); err != nil {
					return err
				}
				if !intentWritten && next.Phase == WorktreeMergePhaseLand && next.Route.Route == WorktreeMergeRouteDirect && (next.Validation.Revision == "" || mode == "stale deferral cleanliness" && next.ValidationDeferral != nil) {
					intentWritten = true
					if mode == "native HEAD drift" {
						writeEngineFile(t, filepath.Join(next.Candidate.Worktree, "post-intent-drift.txt"), "native temporal descendant\n")
						runEngineGit(t, next.Candidate.Worktree, "add", "post-intent-drift.txt")
						runEngineGit(t, next.Candidate.Worktree, "commit", "-m", "private native HEAD drift after land intent write")
						if head := strings.TrimSpace(runEngineGit(t, next.Candidate.Worktree, "rev-parse", "HEAD")); head == next.Candidate.SHA {
							t.Fatal("actual post-intent HEAD failed to advance")
						}
					}
				}
				return nil
			}
			got, err := landWorktreeMerge(t.Context(), o, save)
			if !intentWritten || err == nil || got.Status != WorktreeMergeConflict || got.Candidate != r.Candidate {
				t.Fatalf("native post-intent %s refusal=%+v, %v", mode, got, err)
			}
			if mode == "native HEAD drift" {
				if observed.refused || !strings.Contains(err.Error(), "candidate head drifted") {
					t.Fatalf("actual drift diagnostic=%v", err)
				}
			} else if !observed.refused || !errors.Is(err, sentinel) {
				t.Fatalf("negative exact post-intent observation not reached: %v", err)
			}
			stored, readErr := readWorktreeMergeReceipt(r.ReceiptPath)
			if readErr != nil || stored.Failure != err.Error() || stored.Candidate != r.Candidate || stored.PublishedCandidateSHA != "" {
				t.Fatalf("post-intent durable refusal=%+v, %v", stored, readErr)
			}
			if remote := strings.TrimSpace(runEngineGit(t, f.repository.CloneURL, "rev-parse", "refs/heads/main")); remote != r.TargetSHA {
				t.Fatalf("post-intent refusal changed target: %s", remote)
			}
		})
	}
}

//nolint:paralleltest // Native route provider setup changes process-wide PATH/XDG_STATE_HOME; the corrupted attestation is explicitly negative receipt input.
func TestLandValidationPreservedCandidateRejectsCorruptImportedMainIdentity(t *testing.T) {
	f, r := landOwnerNativeFixture(t)
	installWorktreeMergeDirectGH(t)
	// The underlying prepared candidate and its real validation are untouched.
	// Only untrusted persisted attestation metadata is malformed: this must
	// never grant imported-main validation or authorize publication.
	r.ImportedMainDeadcode = &WorktreeMergeImportedMainDeadcode{CandidateSHA: r.TargetSHA, TargetSHA: r.TargetSHA}
	if r.Candidate.SHA == r.TargetSHA {
		t.Fatal("actual prepared source must advance the native target")
	}
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	got, err := LandWorktreeMerge(t.Context(), WorktreeMergeLandOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Route: WorktreeMergeRoutePullRequest, StopBeforeMerge: true, ValidateLocally: true, Timeout: 5 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "recheck prepared validation identity") || !strings.Contains(err.Error(), "does not bind the exact candidate, target and imported main revision") || got.Status != WorktreeMergeConflict {
		t.Fatalf("corrupted imported identity refusal=%+v, %v", got, err)
	}
	stored, readErr := readWorktreeMergeReceipt(r.ReceiptPath)
	if readErr != nil || stored.Failure != err.Error() || stored.PublishedCandidateSHA != "" {
		t.Fatalf("durable corrupt-identity refusal=%+v, %v", stored, readErr)
	}
	if remote := strings.TrimSpace(runEngineGit(t, f.repository.CloneURL, "rev-parse", "refs/heads/main")); remote != r.TargetSHA {
		t.Fatalf("corrupt identity changed target: %s", remote)
	}
}

//nolint:paralleltest // The real private PR subprocess fixture changes PATH/XDG_STATE_HOME; publication and target objects are actual private Git.
func TestLandValidationPublishedRefreshRefusesBeforeRepublishing(t *testing.T) {
	for _, mode := range []string{"post-refresh cleanliness", "native malformed quality"} {
		//nolint:paralleltest // Each PR provider fixture owns process-wide environment until its row returns.
		t.Run(mode, func(t *testing.T) {
			f, r := landOwnerNativeFixture(t)
			runEngineGit(t, r.Candidate.Worktree, "push", "origin", r.Candidate.SHA+":refs/heads/"+r.Candidate.Branch)
			gh := installWorktreeMergeEngineGH(t, f, r.Candidate.SHA, r.Candidate.Branch)
			r.PullRequest = gh.pr
			r.PublishedCandidateSHA = r.Candidate.SHA
			if err := persistWorktreeMergeReceipt(r); err != nil {
				t.Fatal(err)
			}
			// The malformed policy, when selected, is an actual remote target
			// commit. The real refresh must merge it into its recorded candidate
			// SHA; no post-refresh metadata or positive command result is forged.
			if mode == "native malformed quality" {
				writeEngineFile(t, filepath.Join(f.canonical, ".wb", "quality.yaml"), "version: [invalid\n")
				runEngineGit(t, f.canonical, "add", ".wb/quality.yaml")
			}
			writeEngineFile(t, filepath.Join(f.canonical, "validation-refresh-target.txt"), "actual private target advance\n")
			runEngineGit(t, f.canonical, "add", "validation-refresh-target.txt")
			runEngineGit(t, f.canonical, "commit", "-m", "private target before published validation refresh")
			runEngineGit(t, f.canonical, "push", "origin", "main")
			target := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
			refreshed := false
			sentinel := errors.New("private post-refresh cleanliness observation")
			observed := &landOwnerFaultRunner{Runner: runner.New(), failure: sentinel, refuse: func(dir, name string, args []string) bool {
				return mode == "post-refresh cleanliness" && refreshed && dir == r.Candidate.Worktree && name == "git" && strings.Join(args, " ") == "status --porcelain=v1"
			}}
			got, err := LandWorktreeMerge(t.Context(), WorktreeMergeLandOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Route: WorktreeMergeRoutePullRequest, ValidateLocally: true, Timeout: 5 * time.Second, run: observed, Progress: func(e progress.Event) {
				if e.Phase == "refresh_published_candidate" && e.State == progress.Completed {
					refreshed = true
				}
			}})
			if !refreshed || err == nil || got.Candidate.SHA == r.Candidate.SHA || got.PublishedCandidateSHA != r.PublishedCandidateSHA {
				t.Fatalf("actual refreshed validation boundary=%+v, %v", got, err)
			}
			if mode == "post-refresh cleanliness" {
				if !observed.refused || !errors.Is(err, sentinel) || got.Status != WorktreeMergeConflict {
					t.Fatalf("post-refresh exact negative observation=%+v, %v", got, err)
				}
			} else if observed.refused || got.Status != WorktreeMergeValidationFailed || !strings.Contains(err.Error(), "candidate validation failed after refreshing published candidate to target") || !strings.Contains(err.Error(), "load candidate quality policy") {
				t.Fatalf("actual merged malformed policy refusal=%+v, %v", got, err)
			}
			if head := strings.TrimSpace(runEngineGit(t, r.Candidate.Worktree, "rev-parse", "HEAD")); head != got.Candidate.SHA {
				t.Fatalf("durable refreshed candidate differs from actual local head: %s", head)
			}
			if base := strings.TrimSpace(runEngineGit(t, r.Candidate.Worktree, "merge-base", target, got.Candidate.SHA)); base != target {
				t.Fatalf("actual refreshed candidate does not retain new target: %s", base)
			}
			if remote := strings.TrimSpace(runEngineGit(t, f.repository.CloneURL, "rev-parse", "refs/heads/main")); remote != target {
				t.Fatalf("validation refusal changed actual remote target: %s", remote)
			}
			if remote := strings.TrimSpace(runEngineGit(t, f.repository.CloneURL, "rev-parse", "refs/heads/"+r.Candidate.Branch)); remote != r.PublishedCandidateSHA {
				t.Fatalf("validation refusal republished candidate: %s", remote)
			}
			stored, readErr := readWorktreeMergeReceipt(r.ReceiptPath)
			if readErr != nil || stored.Candidate.SHA != got.Candidate.SHA || stored.TargetSHA != target || stored.PublishedCandidateSHA != r.PublishedCandidateSHA || stored.Failure != err.Error() || stored.Status != got.Status {
				t.Fatalf("durable refreshed refusal=%+v, %v", stored, readErr)
			}
			for _, path := range []string{r.Candidate.Worktree, r.Sources[0].Worktree} {
				if _, statErr := os.Stat(path); statErr != nil {
					t.Fatalf("validation refusal removed native managed asset %s: %v", path, statErr)
				}
			}
		})
	}
}
