package orchestrate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

type legacyLandedFixture struct {
	engineFixture
	receipt                            WorktreeMergeReceipt
	ctx                                context.Context
	validated, observed, final         string
	cleanup                            []string
	prState, prHead, prBase, prLanding string
	readFault                          error
}

func newLegacyLandedFixture(t *testing.T) *legacyLandedFixture {
	t.Helper()
	f := &legacyLandedFixture{engineFixture: newExplicitRootEngineFixture(t), prState: "MERGED", prBase: "main"}
	f.ctx = githubobserver.WithReader(t.Context(), githubobserver.Reader{
		Execute: func(_ context.Context, _ string, args ...string) githubobserver.CommandResponse {
			if len(args) > 1 && args[0] == "api" && args[1] == "--paginate" {
				return githubobserver.CommandResponse{Stdout: f.pullJSON()}
			}
			return githubobserver.CommandResponse{Err: fmt.Errorf("unexpected GitHub execute %q", args)}
		},
		Read: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if f.readFault != nil {
				return nil, f.readFault
			}
			if len(args) > 1 && args[0] == "pr" && args[1] == "view" {
				return []byte(fmt.Sprintf(`{"state":%q,"mergedAt":"2026-01-01T00:00:00Z","headRefOid":%q,"baseRefName":%q,"mergeCommit":{"oid":%q}}`, f.prState, f.prHead, f.prBase, f.prLanding)), nil
			}
			if len(args) > 1 && args[0] == "api" && args[1] == "--paginate" {
				return f.pullJSON(), nil
			}
			if len(args) > 1 && args[0] == "pr" && args[1] == "list" {
				return []byte("[]"), nil
			}
			return nil, fmt.Errorf("unexpected GitHub read %q", args)
		},
		Get: func(_ context.Context, req githubobserver.GetRequest) (githubobserver.Response, error) {
			if f.readFault != nil {
				return githubobserver.Response{}, f.readFault
			}
			var body string
			switch {
			case strings.Contains(req.Endpoint, "/commits/") && strings.Contains(req.Endpoint, "/pulls"):
				return githubobserver.Response{Body: f.pullJSON()}, nil
			case strings.Contains(req.Endpoint, "/branches/main"):
				body = `{"protected":false,"protection":{}}`
			case strings.Contains(req.Endpoint, "/rules/branches/") || strings.Contains(req.Endpoint, "/actions/runs"):
				body = "[]"
			case strings.Contains(req.Endpoint, "/git/ref/heads/"):
				head := strings.TrimSpace(runEngineGit(t, f.repository.CloneURL, "rev-parse", "refs/heads/main"))
				body = fmt.Sprintf(`{"object":{"sha":%q}}`, head)
			case strings.Contains(req.Endpoint, "/compare/"):
				base := strings.TrimSpace(runEngineGit(t, f.repository.CloneURL, "merge-base", req.Target, req.Head))
				status := "diverged"
				if req.Target == req.Head {
					status = "identical"
				} else if base == req.Target {
					status = "ahead"
				} else if base == req.Head {
					status = "behind"
				}
				body = fmt.Sprintf(`{"status":%q,"base_commit":{"sha":%q},"merge_base_commit":{"sha":%q}}`, status, req.Target, base)
			default:
				return githubobserver.Response{}, fmt.Errorf("unexpected GitHub endpoint %s", req.Endpoint)
			}
			return githubobserver.Response{Body: []byte(body)}, nil
		},
	})
	a := createMergeSource(t, f.engineFixture, "legacy-a", "feature/legacy-a", "a.txt", "a\n")
	runEngineGit(t, a.WorktreeDir, "push", "origin", a.Branch)
	b := createMergeSourceOnBase(t, f.engineFixture, "legacy-b", "feature/legacy-b", a.Branch, "b.txt", "b\n")
	r, err := PrepareWorktreeMerge(f.ctx, WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{a.WorktreeDir, b.WorktreeDir}, Target: "main", AgentRuntime: "test", Model: "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	f.validated = r.Candidate.SHA
	r.Status = WorktreeMergeValidationFailed
	r.Validation.Status = "failed"
	r.Validation.Repository, r.Validation.Path, r.Validation.Revision, r.Validation.WorkspaceClean = r.Repository, r.Candidate.Worktree, r.Candidate.SHA, true
	r.ValidationIdentity = &WorktreeMergeValidationIdentity{CandidateSHA: r.Candidate.SHA, TargetSHA: r.TargetSHA, SourceSHAs: sourceSHAs(r.Sources)}
	r.Candidate.SHA = ""
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	f.receipt = r
	// Only the historical revision is landed. The original checkout remains a
	// clean, claimed descendant with a distinct, deliberately unpublished tip.
	runEngineGit(t, r.Candidate.Worktree, "commit", "--allow-empty", "-m", "fixture: preserve newer candidate")
	f.observed = strings.TrimSpace(runEngineGit(t, r.Candidate.Worktree, "rev-parse", "HEAD"))
	runEngineGit(t, f.canonical, "merge", "--ff-only", f.validated)
	runEngineGit(t, f.canonical, "commit", "--allow-empty", "-m", "fixture: advance source before retirement")
	f.final = strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, f.canonical, "push", "origin", "main")
	f.prHead, f.prLanding = f.final, f.final
	for i, s := range []worktrees.CreateResult{a, b} {
		runEngineGit(t, s.WorktreeDir, "merge", "--ff-only", f.final)
		clean, err := worktrees.Cleanup(f.ctx, worktrees.CleanupOptions{ProjectsRoot: f.githubDir, Task: r.Sources[i].Task, Apply: true, DeleteRemote: true})
		if err != nil || len(clean.Results) != 1 || !clean.Results[0].Applied {
			t.Fatalf("cleanup %+v: %v", clean, err)
		}
		f.cleanup = append(f.cleanup, clean.ReportPath)
	}
	return f
}

func (f *legacyLandedFixture) pullJSON() []byte {
	return []byte(fmt.Sprintf(`[{"number":45,"html_url":"https://github.com/acme/app/pull/45","state":"closed","merged_at":"2026-01-01T00:00:00Z","merge_commit_sha":%q,"head":{"ref":"feature/legacy-a","sha":%q},"base":{"ref":"main","sha":%q}}]`, f.final, f.final, f.receipt.TargetSHA))
}

func (f *legacyLandedFixture) options() WorktreeMergeLandedFailureAcknowledgementOptions {
	return WorktreeMergeLandedFailureAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: f.receipt.ReceiptPath, Actor: "reviewer", Reason: "historical validation landed; newer candidate preserved"}
}

func TestLegacyLandedValidationPreservesAdvancedCheckoutAndReleasesOnlyHistoricalLane(t *testing.T) {
	t.Parallel()
	f := newLegacyLandedFixture(t)
	original, err := os.ReadFile(f.receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	view, err := worktrees.LoadWorkLogView(f.ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: f.githubDir, Worktree: f.receipt.Candidate.Worktree})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := os.ReadFile(view.Claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	o := f.options()
	ack, err := AcknowledgeLandedMergeFailure(f.ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if ack.CandidateSHA != f.validated || ack.LegacyValidation.ObservedCandidateSHA != f.observed || ack.CurrentTargetSHA != f.final {
		t.Fatalf("conflated landed/preserved identities: %+v", ack)
	}
	for i, p := range ack.LegacyValidation.RetiredSources {
		if p.FinalSHA != f.final || p.Source.SHA != f.receipt.Sources[i].SHA || p.FinalSHA == p.Source.SHA {
			t.Fatalf("retirement lost historical source: %+v", p)
		}
	}
	for _, p := range []string{ack.AcknowledgementPath, legacyValidationFailureIdentityPath(f.receipt.ReceiptPath)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("dry run wrote sidecar %s: %v", p, err)
		}
	}
	o.Apply = true
	ack, err = AcknowledgeLandedMergeFailure(f.ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	again, err := AcknowledgeLandedMergeFailure(f.ctx, o)
	if err != nil || again.ID != ack.ID {
		t.Fatalf("idempotency: %+v %v", again, err)
	}
	for path, want := range map[string][]byte{f.receipt.ReceiptPath: original, view.Claim.ClaimPath: claim} {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("immutable record changed %s: %v", path, err)
		}
	}
	if got := strings.TrimSpace(runEngineGit(t, f.receipt.Candidate.Worktree, "rev-parse", "HEAD")); got != f.observed {
		t.Fatal("newer checkout changed")
	}
	if got := strings.TrimSpace(runEngineGit(t, f.receipt.Candidate.Worktree, "status", "--porcelain")); got != "" {
		t.Fatal("newer checkout dirtied")
	}
	if ok, err := hasLandedFailureAcknowledgement(f.receipt); err != nil || !ok {
		t.Fatalf("scanner rejected real sidecars: %t %v", ok, err)
	}
	next := createMergeSource(t, f.engineFixture, "next-after-legacy", "feature/next-after-legacy", "next.txt", "next\n")
	if _, err := PrepareWorktreeMerge(f.ctx, WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{next.WorktreeDir}, Target: "main", AgentRuntime: "test", Model: "test-model"}); err != nil {
		t.Fatalf("global lane remained blocked: %v", err)
	}
}

func TestLegacyLandedValidationRejectsContradictoryValidationIdentity(t *testing.T) {
	t.Parallel()
	f := newLegacyLandedFixture(t)
	for _, fault := range []string{"candidate", "target", "sources", "published", "dirty validation", "phase", "missing revision"} {
		t.Run(fault, func(t *testing.T) {
			t.Parallel()
			r := f.receipt
			identity := *r.ValidationIdentity
			r.ValidationIdentity = &identity
			switch fault {
			case "candidate":
				r.ValidationIdentity.CandidateSHA = "other"
			case "target":
				r.ValidationIdentity.TargetSHA = "other"
			case "sources":
				r.ValidationIdentity.SourceSHAs = []string{"other"}
			case "published":
				r.PullRequest = "45"
			case "dirty validation":
				r.Validation.WorkspaceClean = false
			case "phase":
				r.Phase = WorktreeMergePhaseLand
			case "missing revision":
				r.Validation.Revision = ""
			}
			if _, err := legacyLandedValidationReceipt(r); err == nil {
				t.Fatal("contradictory legacy identity accepted")
			}
		})
	}
	r := f.receipt
	r.ValidationIdentity = nil
	if _, err := legacyLandedValidationReceipt(r); err != nil {
		t.Fatalf("older missing fingerprint is not contradiction: %v", err)
	}
}

func TestLegacyLandedValidationReadRequiresBothUntamperedSidecars(t *testing.T) {
	t.Parallel()
	f := newLegacyLandedFixture(t)
	o := f.options()
	o.Apply = true
	ack, err := AcknowledgeLandedMergeFailure(f.ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	identityPath := legacyValidationFailureIdentityPath(f.receipt.ReceiptPath)
	identity, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"missing identity", "corrupt identity", "wrong identity digest", "wrong observed checkout", "wrong candidate worktree", "wrong candidate branch", "wrong source", "missing source proof", "mixed evidence", "wrong actor", "legacy on ordinary receipt"} {
		//nolint:paralleltest // Rows mutate and restore one isolated pair of actual sidecar files.
		t.Run(fault, func(t *testing.T) {
			defer func() {
				if err := os.WriteFile(identityPath, identity, 0600); err != nil {
					t.Error(err)
				}
			}()
			a := ack
			e := *ack.LegacyValidation
			e.RetiredSources = append([]WorktreeMergeRetiredDescendantSourceProof(nil), e.RetiredSources...)
			a.LegacyValidation = &e
			r := f.receipt
			switch fault {
			case "missing identity":
				if err := os.Remove(identityPath); err != nil {
					t.Fatal(err)
				}
			case "corrupt identity":
				if err := os.WriteFile(identityPath, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "wrong identity digest":
				if err := os.WriteFile(identityPath, bytes.Replace(identity, []byte(ack.ReceiptSHA256), []byte("wrong"), 1), 0600); err != nil {
					t.Fatal(err)
				}
			case "wrong observed checkout":
				a.LegacyValidation.ObservedCandidateSHA = "tampered"
			case "wrong candidate worktree":
				a.CandidateWorktree += ".foreign"
				a.ID = landedFailureAcknowledgementID(a)
			case "wrong candidate branch":
				a.CandidateBranch = "feature/foreign"
				a.ID = landedFailureAcknowledgementID(a)
			case "wrong source":
				a.LegacyValidation.RetiredSources[0].Source.SHA = "wrong"
				a.ID = landedFailureAcknowledgementID(a)
			case "missing source proof":
				a.LegacyValidation.RetiredSources = nil
				a.ID = landedFailureAcknowledgementID(a)
			case "mixed evidence":
				a.ObservedSources = a.Sources
				a.ID = landedFailureAcknowledgementID(a)
			case "wrong actor":
				a.Actor = "other"
				a.ID = landedFailureAcknowledgementID(a)
			case "legacy on ordinary receipt":
				r.Candidate.SHA = f.validated
			}
			if err := persistLandedFailureAcknowledgement(a.AcknowledgementPath, a); err != nil {
				t.Fatal(err)
			}
			if _, err := readLandedFailureAcknowledgement(a.AcknowledgementPath, r); err == nil || errors.Is(err, os.ErrNotExist) {
				t.Fatalf("scanner failure was suppressed: %v", err)
			} else if (fault == "wrong candidate worktree" || fault == "wrong candidate branch") && !strings.Contains(err.Error(), "mismatched correlated evidence") {
				t.Fatalf("candidate custody was not rejected by the identity guard: %v", err)
			}
		})
	}
}

func TestLegacyLandedValidationNativeCustodyAndRetirementRefusals(t *testing.T) {
	t.Parallel()
	for _, fault := range []string{"dirty candidate", "missing candidate", "foreign branch", "foreign claim", "non-descendant candidate", "missing cleanup", "malformed cleanup", "wrong cleanup head", "missing terminal", "source path resurrected", "source branch resurrected", "foreign PR head", "foreign PR base", "unmerged PR", "wrong PR landing", "unavailable PR", "target lacks historical candidate", "missing actor"} {
		t.Run(fault, func(t *testing.T) {
			t.Parallel()
			f := newLegacyLandedFixture(t)
			r := f.receipt
			o := f.options()
			o.Apply = true
			switch fault {
			case "dirty candidate":
				writeEngineFile(t, filepath.Join(r.Candidate.Worktree, "dirty.txt"), "uncommitted\n")
			case "missing candidate":
				if err := os.Rename(r.Candidate.Worktree, r.Candidate.Worktree+".preserved"); err != nil {
					t.Fatal(err)
				}
			case "foreign branch":
				r.Candidate.Branch = "feature/foreign"
			case "foreign claim":
				view, err := worktrees.LoadWorkLogView(f.ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: f.githubDir, Worktree: r.Candidate.Worktree})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(view.Claim.ClaimPath, view.Claim.ClaimPath+".preserved"); err != nil {
					t.Fatal(err)
				}
			case "non-descendant candidate":
				runEngineGit(t, r.Candidate.Worktree, "reset", "--hard", r.TargetSHA)
			case "missing cleanup":
				if err := os.Remove(f.cleanup[0]); err != nil {
					t.Fatal(err)
				}
			case "malformed cleanup":
				if err := os.WriteFile(f.cleanup[0], []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "wrong cleanup head":
				raw, err := os.ReadFile(f.cleanup[0])
				if err != nil {
					t.Fatal(err)
				}
				var doc map[string]any
				if err := json.Unmarshal(raw, &doc); err != nil {
					t.Fatal(err)
				}
				doc["results"].([]any)[0].(map[string]any)["head_sha"] = r.Sources[0].SHA
				raw, err = json.Marshal(doc)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(f.cleanup[0], raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "missing terminal":
				files, err := filepath.Glob(filepath.Join(f.githubDir, ".wb", "worklogs", r.Sources[0].Task, "runs", "*", "terminals", "*.json"))
				if err != nil || len(files) != 1 {
					t.Fatalf("terminal fixture %v %v", files, err)
				}
				if err := os.Remove(files[0]); err != nil {
					t.Fatal(err)
				}
			case "source path resurrected":
				if err := os.MkdirAll(r.Sources[0].Worktree, 0700); err != nil {
					t.Fatal(err)
				}
			case "source branch resurrected":
				runEngineGit(t, f.canonical, "update-ref", "refs/heads/"+r.Sources[0].Branch, r.Sources[0].SHA)
			case "foreign PR head":
				f.prHead = r.TargetSHA
			case "foreign PR base":
				f.prBase = "other"
			case "unmerged PR":
				f.prState = "OPEN"
			case "wrong PR landing":
				f.prLanding = r.TargetSHA
			case "unavailable PR":
				f.readFault = errors.New("synthetic provider unavailable")
			case "target lacks historical candidate":
				runEngineGit(t, f.canonical, "push", "--force", "origin", r.TargetSHA+":refs/heads/main")
			case "missing actor":
				o.Actor = ""
			}
			if err := persistWorktreeMergeReceipt(r); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(r.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := AcknowledgeLandedMergeFailure(f.ctx, o); err == nil {
				t.Fatalf("unsafe %s accepted", fault)
			}
			after, err := os.ReadFile(r.ReceiptPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("refusal modified historical receipt: %v", err)
			}
			for _, path := range []string{landedFailureAcknowledgementPath(r.ReceiptPath), legacyValidationFailureIdentityPath(r.ReceiptPath)} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("refusal published sidecar %s: %v", path, err)
				}
			}
		})
	}
}

type legacyLandedRunner struct {
	runner.Runner
	before func(string, []string) error
}

func (r legacyLandedRunner) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if err := r.before(dir, args); err != nil {
		return runner.Result{ExitCode: 1}, err
	}
	return r.Runner.RunOpts(ctx, dir, opts, name, args...)
}

func TestLegacyLandedValidationRechecksMutableProofBeforePublishing(t *testing.T) {
	t.Parallel()
	for _, fault := range []string{"target advanced", "candidate advanced", "source resurrected", "second proof unavailable"} {
		t.Run(fault, func(t *testing.T) {
			t.Parallel()
			f := newLegacyLandedFixture(t)
			o := f.options()
			o.Apply = true
			fetches := 0
			sentinel := errors.New("second exact proof unavailable")
			run := legacyLandedRunner{Runner: defaultRunner, before: func(dir string, args []string) error {
				if dir == f.canonical && len(args) > 0 && args[0] == "fetch" {
					fetches++
					if fetches == 2 {
						switch fault {
						case "target advanced":
							runEngineGit(t, f.canonical, "commit", "--allow-empty", "-m", "fixture: advance target during proof")
							runEngineGit(t, f.canonical, "push", "origin", "main")
						case "candidate advanced":
							runEngineGit(t, f.receipt.Candidate.Worktree, "commit", "--allow-empty", "-m", "fixture: change custody during proof")
						case "source resurrected":
							if err := os.MkdirAll(f.receipt.Sources[0].Worktree, 0700); err != nil {
								t.Fatal(err)
							}
						case "second proof unavailable":
							return sentinel
						}
					}
				}
				return nil
			}}
			_, err := acknowledgeLandedMergeFailure(f.ctx, o, run, readWorktreeMergeReceipt, persistLandedFailureAcknowledgement)
			if err == nil {
				t.Fatal("changed mutable proof accepted")
			}
			if fault == "second proof unavailable" && !errors.Is(err, sentinel) {
				t.Fatalf("lost error identity: %v", err)
			}
			for _, path := range []string{landedFailureAcknowledgementPath(f.receipt.ReceiptPath), legacyValidationFailureIdentityPath(f.receipt.ReceiptPath)} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("changed proof wrote sidecar %s: %v", path, err)
				}
			}
		})
	}
}

func TestLegacyLandedValidationUnavailableExternalEvidenceFailsClosed(t *testing.T) {
	t.Parallel()
	f := newLegacyLandedFixture(t)
	for _, fault := range []string{"canonical", "receipt hash", "cleanup initial hash", "cleanup final hash", "cleanup digest changed", "retired original not ancestor", "PR proof changed", "claim ancestry query", "historical root query", "final custody query", "final target query", "invalid shape"} {
		//nolint:paralleltest // Rows perform fresh Git fetches against the same isolated canonical checkout.
		t.Run(fault, func(t *testing.T) {
			readers := nativeLegacyLandedProofReaders()
			sentinel := errors.New("unavailable " + fault)
			r := f.receipt
			hashCalls, headCalls, fetches := 0, 0, 0
			switch fault {
			case "canonical":
				readers.canonical = func(string, string) (string, error) { return "", sentinel }
			case "receipt hash", "cleanup initial hash", "cleanup final hash", "cleanup digest changed":
				readers.hash = func(path string) (string, error) {
					if path == r.ReceiptPath && fault == "receipt hash" {
						return "", sentinel
					}
					if path == f.cleanup[0] {
						hashCalls++
						if fault == "cleanup initial hash" && hashCalls == 1 || fault == "cleanup final hash" && hashCalls == 2 {
							return "", sentinel
						}
						if fault == "cleanup digest changed" && hashCalls == 2 {
							return "changed", nil
						}
					}
					return worktreeMergeReceiptSHA256(path)
				}
			case "retired original not ancestor":
				readers.terminal = func(string, worktrees.TerminalWorkLogExpectation) (string, error) { return f.observed, nil }
			case "PR proof changed":
				readers.landing = func(ctx context.Context, r WorktreeMergeReceipt) (string, string, string, string, string, error) {
					landing, current, _, kind, tree, err := proveStrandedPullRequestLanding(ctx, r)
					return landing, current, "unreceipted-head", kind, tree, err
				}
			case "invalid shape":
				r.Validation.Revision = ""
			}
			run := legacyLandedRunner{Runner: defaultRunner, before: func(dir string, args []string) error {
				if len(args) > 0 && args[0] == "fetch" {
					fetches++
					if fault == "final target query" && fetches == 2 {
						return sentinel
					}
				}
				if dir == r.Candidate.Worktree && len(args) > 2 && args[0] == "rev-parse" && args[2] == "HEAD^{commit}" {
					headCalls++
					if fault == "final custody query" && headCalls == 3 {
						return sentinel
					}
				}
				if len(args) == 3 && args[0] == "merge-base" {
					if fault == "claim ancestry query" && dir == r.Candidate.Worktree && args[1] == r.TargetSHA && args[2] == f.validated {
						return sentinel
					}
					if fault == "historical root query" && dir == f.canonical && args[1] == r.Sources[0].SHA && args[2] == f.validated {
						return sentinel
					}
				}
				return nil
			}}
			_, err := inspectLegacyLandedValidationWithReaders(f.ctx, f.options(), r, run, readers)
			if err == nil {
				t.Fatalf("untrusted %s accepted", fault)
			}
			if fault != "cleanup digest changed" && fault != "retired original not ancestor" && fault != "PR proof changed" && fault != "invalid shape" && !errors.Is(err, sentinel) {
				t.Fatalf("wrong evidence failed for %s: %v", fault, err)
			}
		})
	}
}

func TestLegacyLandedValidationOwnerRechecksReceiptAndRecoversInterruptedSidecarWrite(t *testing.T) {
	t.Parallel()
	for _, fault := range []string{"invalid receipt", "locked lane", "initial read failed", "initial receipt changed", "final read failed", "final receipt changed", "identity write failed", "ack write failed", "existing identity different", "existing identity corrupt", "second proof failed"} {
		t.Run(fault, func(t *testing.T) {
			t.Parallel()
			f := newLegacyLandedFixture(t)
			o := f.options()
			o.Apply = true
			r := f.receipt
			sentinel := errors.New("selected " + fault)
			reads, fetches := 0, 0
			read := func(path string) (WorktreeMergeReceipt, error) {
				reads++
				native, err := readWorktreeMergeReceipt(path)
				if err != nil {
					return native, err
				}
				if fault == "initial read failed" && reads == 1 || fault == "final read failed" && reads == 2 {
					return WorktreeMergeReceipt{}, sentinel
				}
				if fault == "initial receipt changed" && reads == 1 || fault == "final receipt changed" && reads == 2 {
					native.Failure = "concurrent immutable receipt change"
				}
				return native, nil
			}
			persist := persistLandedFailureAcknowledgement
			if fault == "ack write failed" {
				persist = func(string, WorktreeMergeLandedFailureAcknowledgement) error { return sentinel }
			}
			persistIdentity := persistLegacyValidationFailureIdentity
			if fault == "identity write failed" {
				persistIdentity = func(string, WorktreeMergeLegacyValidationFailureIdentity) error { return sentinel }
			}
			if fault == "locked lane" {
				lock, err := AcquireOperationLock(f.githubDir, r.Lane, true)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = lock.Release() })
			}
			if fault == "invalid receipt" {
				r.Validation.WorkspaceClean = false
			}
			if fault == "existing identity different" || fault == "existing identity corrupt" {
				state, err := inspectLegacyLandedValidation(f.ctx, o, r, defaultRunner)
				if err != nil {
					t.Fatal(err)
				}
				i := state.identity
				i.RecordedAt = time.Now().UTC()
				i.Actor = "other"
				i.ID = legacyValidationFailureIdentityID(i)
				if fault == "existing identity corrupt" {
					if err := os.WriteFile(i.AcknowledgementPath, []byte("{"), 0600); err != nil {
						t.Fatal(err)
					}
				} else if err := persistLegacyValidationFailureIdentity(i.AcknowledgementPath, i); err != nil {
					t.Fatal(err)
				}
			}
			run := legacyLandedRunner{Runner: defaultRunner, before: func(_ string, args []string) error {
				if len(args) > 0 && args[0] == "fetch" {
					fetches++
					if fault == "second proof failed" && fetches == 3 {
						return sentinel
					}
				}
				return nil
			}}
			_, err := acknowledgeLegacyLandedValidationFailureWithIdentityWriter(f.ctx, o, r, r.ReceiptPath, run, read, persist, persistIdentity)
			if err == nil {
				t.Fatalf("unsafe %s accepted", fault)
			}
			if fault == "ack write failed" {
				// Failure after publishing the identity remains retryable with the
				// original assertion, without replacing that append-only identity.
				if _, err := AcknowledgeLandedMergeFailure(f.ctx, o); err != nil {
					t.Fatalf("retry after partial publish: %v", err)
				}
			} else if _, err := os.Stat(landedFailureAcknowledgementPath(r.ReceiptPath)); !os.IsNotExist(err) {
				t.Fatalf("refusal wrote final acknowledgement: %v", err)
			}
		})
	}
}

func TestLegacyLandedValidationRetainsExactHeadSupportAndLateCustodyFences(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"exact checkout", "checkout changes during source proof", "earlier source reappears", "current source reappears"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f := newLegacyLandedFixture(t)
			readers := nativeLegacyLandedProofReaders()
			if scenario == "exact checkout" {
				runEngineGit(t, f.receipt.Candidate.Worktree, "reset", "--hard", f.validated)
			}
			if scenario == "checkout changes during source proof" || scenario == "current source reappears" {
				readers.landing = func(ctx context.Context, r WorktreeMergeReceipt) (string, string, string, string, string, error) {
					landing, current, head, kind, tree, err := proveStrandedPullRequestLanding(ctx, r)
					if scenario == "checkout changes during source proof" {
						runEngineGit(t, f.receipt.Candidate.Worktree, "commit", "--allow-empty", "-m", "fixture: custody changed during later read")
					} else if err := os.MkdirAll(r.Candidate.Worktree, 0700); err != nil {
						t.Fatal(err)
					}
					return landing, current, head, kind, tree, err
				}
			}
			if scenario == "earlier source reappears" {
				readers.hash = func(path string) (string, error) {
					if path == f.receipt.ReceiptPath {
						if err := os.MkdirAll(f.receipt.Sources[0].Worktree, 0700); err != nil {
							t.Fatal(err)
						}
					}
					return worktreeMergeReceiptSHA256(path)
				}
			}
			state, err := inspectLegacyLandedValidationWithReaders(f.ctx, f.options(), f.receipt, defaultRunner, readers)
			if scenario == "exact checkout" {
				if err != nil || state.evidence.ObservedCandidateSHA != f.validated {
					t.Fatalf("unchanged checkout lost: %+v %v", state, err)
				}
			} else if err == nil {
				t.Fatal("late custody loss accepted")
			}
		})
	}
	ordinary := WorktreeMergeReceipt{Candidate: WorktreeMergeCandidate{SHA: "unchanged"}}
	if got, err := resolveLegacyLandedAcknowledgement(WorktreeMergeLandedFailureAcknowledgement{}, ordinary); err != nil || got.Candidate != ordinary.Candidate {
		t.Fatalf("ordinary reader changed: %+v %v", got, err)
	}
}
