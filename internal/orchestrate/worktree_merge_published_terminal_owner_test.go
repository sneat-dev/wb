package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// These records prove policy/error ordering, not native publication or custody.
func terminalOwnerPolicyReceipt(t *testing.T) (string, WorktreeMergeReceipt) {
	t.Helper()
	root := t.TempDir()
	home, err := wbhome.Root(root)
	if err != nil {
		t.Fatal(err)
	}
	r := WorktreeMergeReceipt{SchemaVersion: WorktreeMergeSchemaVersion, ID: "terminal-policy", ReceiptPath: filepath.Join(home, "reports", "worktree-merge", "terminal.json"), Lane: worktreeMergeLaneID("acme/app", "main"), Repository: "acme/app", Target: "main", TargetSHA: strings.Repeat("a", 40), Phase: WorktreeMergePhaseLand, Status: WorktreeMergeConflict, PullRequest: "https://example.test/acme/app/pull/17", PublishedCandidateSHA: strings.Repeat("b", 40), Candidate: WorktreeMergeCandidate{Task: "candidate", Worktree: filepath.Join(root, "candidate"), Branch: "candidate", SHA: strings.Repeat("b", 40)}, Sources: []WorktreeMergeSource{{Task: "source", Worktree: filepath.Join(root, "source"), Branch: "source", SHA: strings.Repeat("c", 40)}}}
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	return root, r
}

func TestPublishedTerminalValidatorsPreserveDistinctEligibility(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"retired", "stranded"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			for _, mode := range []string{"valid", "path", "id", "lane empty", "lane mismatch", "phase", "status", "landing", "pull request", "repository", "target", "target SHA", "candidate SHA", "published SHA", "candidate task", "candidate path", "candidate branch", "sources", "source task", "source path", "source branch", "source SHA"} {
				t.Run(mode, func(t *testing.T) {
					t.Parallel()
					_, r := terminalOwnerPolicyReceipt(t)
					want := ""
					switch mode {
					case "path":
						r.ReceiptPath += "other"
						want = "inconsistent immutable receipt identity"
					case "id":
						r.ID = ""
						want = "inconsistent immutable receipt identity"
					case "lane empty":
						r.Lane = ""
						want = "inconsistent immutable receipt identity"
					case "lane mismatch":
						r.Lane = "other"
						want = "inconsistent immutable receipt identity"
					case "phase":
						r.Phase = "other"
						want = "phase"
						if kind == "stranded" {
							want = "want recoverable land receipt"
						}
					case "status":
						r.Status = WorktreeMergeComplete
						want = "want conflict"
						if kind == "stranded" {
							want = "want recoverable land receipt"
						}
					case "landing":
						r.LandingSHA = "landed"
						want = "already recorded a landing SHA"
					case "pull request":
						r.PullRequest = ""
						want = "no published pull request"
					case "repository":
						r.Repository = ""
						r.Lane = worktreeMergeLaneID(r.Repository, r.Target)
						want = "lacks complete immutable repository"
					case "target":
						r.Target = ""
						r.Lane = worktreeMergeLaneID(r.Repository, r.Target)
						want = "lacks complete immutable repository"
					case "target SHA":
						r.TargetSHA = ""
						want = "lacks complete immutable repository"
					case "candidate SHA":
						r.Candidate.SHA = ""
						if kind == "stranded" {
							want = "lacks complete immutable repository"
						}
					case "published SHA":
						r.PublishedCandidateSHA = ""
						if kind == "stranded" {
							want = "does not match its exact preserved candidate"
						}
					case "candidate task":
						r.Candidate.Task = ""
						if kind == "retired" {
							want = "lacks complete immutable candidate identity"
						}
					case "candidate path":
						r.Candidate.Worktree = ""
						if kind == "retired" {
							want = "lacks complete immutable candidate identity"
						}
					case "candidate branch":
						r.Candidate.Branch = ""
						if kind == "retired" {
							want = "lacks complete immutable candidate identity"
						}
					case "sources":
						r.Sources = nil
						if kind == "retired" {
							want = "no receipted sources"
						}
					case "source task":
						r.Sources[0].Task = ""
						if kind == "retired" {
							want = "incomplete immutable source identity"
						}
					case "source path":
						r.Sources[0].Worktree = ""
						if kind == "retired" {
							want = "incomplete immutable source identity"
						}
					case "source branch":
						r.Sources[0].Branch = ""
						if kind == "retired" {
							want = "incomplete immutable source identity"
						}
					case "source SHA":
						r.Sources[0].SHA = ""
						if kind == "retired" {
							want = "incomplete immutable source identity"
						}
					}
					path := r.ReceiptPath
					if mode == "path" {
						path = strings.TrimSuffix(path, "other")
					}
					var err error
					if kind == "retired" {
						err = validateRetiredPublicationReceipt(r, path)
					} else {
						err = validateStrandedLandingReceipt(r, path)
					}
					if want == "" {
						if err != nil {
							t.Fatalf("actual %s acceptance: %v", mode, err)
						}
					} else if err == nil || !strings.Contains(err.Error(), want) {
						t.Fatalf("actual %s refusal=%v want %q", mode, err, want)
					}
				})
			}
			if kind == "retired" {
				_, r := terminalOwnerPolicyReceipt(t)
				r.PublishedCandidateSHA = ""
				r.Candidate.SHA = ""
				if err := validateRetiredPublicationReceipt(r, r.ReceiptPath); err == nil || !strings.Contains(err.Error(), "no published or preserved candidate") {
					t.Fatalf("empty SHAs: %v", err)
				}
			}
		})
	}
}

func TestPublishedTerminalInitialProofPreservesExactQueriesAndRefusals(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("selected initial provider read")
	for _, kind := range []string{"retired", "stranded"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			for _, mode := range []string{"read error", "malformed", "open", "merged metadata missing", "wrong head branch", "wrong base", "missing head", "merged", "merge commit", "merge time", "closed optional identity"} {
				t.Run(mode, func(t *testing.T) {
					t.Parallel()
					_, r := terminalOwnerPolicyReceipt(t)
					view := map[string]any{"state": "CLOSED", "headRefName": r.Candidate.Branch, "headRefOid": r.Candidate.SHA, "baseRefName": r.Target, "mergedAt": "", "mergeCommit": map[string]string{"oid": ""}}
					want := ""
					if kind == "stranded" {
						view["state"] = "OPEN"
						want = "not MERGED"
					}
					switch mode {
					case "read error":
						want = "read pull-request"
					case "malformed":
						want = "decode pull-request"
					case "open":
						view["state"] = "OPEN"
						if kind == "retired" {
							want = "want CLOSED"
						}
					case "merged metadata missing":
						view["state"] = "MERGED"
						if kind == "retired" {
							want = "is MERGED"
						} else {
							want = "without a merge time or server merge commit"
						}
					case "wrong head branch":
						view["headRefName"] = "other"
						if kind == "retired" {
							want = "does not match exact receipted candidate branch"
						}
					case "wrong base":
						view["baseRefName"] = "other"
						if kind == "stranded" {
							want = "not receipted target"
						}
					case "missing head":
						view["headRefOid"] = ""
						if kind == "stranded" {
							want = "returned no head commit"
						}
					case "merged":
						view["state"] = "MERGED"
						if kind == "retired" {
							want = "is MERGED"
						} else {
							want = "without a merge time or server merge commit"
						}
					case "merge commit":
						view["mergeCommit"] = map[string]string{"oid": "recorded"}
						if kind == "retired" {
							want = "is MERGED"
						}
					case "merge time":
						view["mergedAt"] = "recorded"
						if kind == "retired" {
							want = "is MERGED"
						}
					case "closed optional identity":
						view["headRefName"] = ""
						if kind == "retired" {
							view["headRefOid"] = "not-a-strict-oid"
						}
					}
					data, err := json.Marshal(view)
					if err != nil {
						t.Fatal(err)
					}
					calls := 0
					read := func(ctx context.Context, dir string, args ...string) (string, error) {
						calls++
						fields := "state,closedAt,mergedAt,mergeCommit,headRefName,headRefOid"
						if kind == "stranded" {
							fields = "state,mergedAt,mergeCommit,headRefOid,baseRefName"
						}
						if dir != "" || !reflect.DeepEqual(args, []string{"pr", "view", r.PullRequest, "--repo", r.Repository, "--json", fields}) {
							t.Fatalf("initial proof query dir=%q args=%v", dir, args)
						}
						if mode == "read error" {
							return "", sentinel
						}
						if mode == "malformed" {
							return "{broken", nil
						}
						return string(data), nil
					}
					if kind == "retired" {
						_, err = proveRetiredPullRequest(t.Context(), r, read)
					} else {
						_, _, _, _, _, err = proveStrandedPullRequestLandingWithRead(t.Context(), r, read)
					}
					if calls != 1 {
						t.Fatalf("initial calls=%d", calls)
					}
					if want == "" {
						if err != nil {
							t.Fatalf("original lax retired acceptance: %v", err)
						}
					} else if err == nil || !strings.Contains(err.Error(), want) {
						t.Fatalf("proof=%v want %q", err, want)
					}
					if mode == "read error" && !errors.Is(err, sentinel) {
						t.Fatalf("lost initial read identity: %v", err)
					}
				})
			}
		})
	}
}

func TestPublishedTerminalOwnersRefuseBeforeAnyProviderObservation(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"retired", "stranded"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			for _, mode := range []string{"empty input", "read", "shape", "actor", "lock", "retired reread", "retired changed shape", "retired missing candidate", "retired non-directory candidate", "provider"} {
				if kind == "stranded" && strings.HasPrefix(mode, "retired ") {
					continue
				}
				t.Run(mode, func(t *testing.T) {
					t.Parallel()
					root, r := terminalOwnerPolicyReceipt(t)
					before, err := os.ReadFile(r.ReceiptPath)
					if err != nil {
						t.Fatal(err)
					}
					sentinel := errors.New("selected receipt observation")
					reads, providers := 0, 0
					apply := true
					input := r.ReceiptPath
					want := ""
					actor := "operator"
					read := func(path string) (WorktreeMergeReceipt, error) {
						reads++
						if mode == "read" || (mode == "retired reread" && reads == 2) {
							return WorktreeMergeReceipt{}, sentinel
						}
						got, err := readWorktreeMergeReceipt(path)
						if mode == "shape" || (mode == "retired changed shape" && reads == 2) {
							got.Lane = ""
						}
						return got, err
					}
					switch mode {
					case "empty input":
						input = ""
						want = "candidate worktree or receipt is required"
					case "read", "retired reread":
						want = sentinel.Error()
					case "shape", "retired changed shape":
						want = "inconsistent immutable receipt identity"
					case "actor":
						actor = ""
						want = "--actor and --reason"
					case "lock":
						home, err := wbhome.Root(root)
						if err != nil {
							t.Fatal(err)
						}
						path := filepath.Join(home, "worktrees")
						if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(path, []byte("blocker"), 0o600); err != nil {
							t.Fatal(err)
						}
					case "retired missing candidate":
						want = "required for read-only git object resolution"
					case "retired non-directory candidate":
						if err := os.WriteFile(r.Candidate.Worktree, []byte("file"), 0o600); err != nil {
							t.Fatal(err)
						}
						want = "required for read-only git object resolution"
					case "provider":
						want = "selected initial provider refusal"
					}
					if kind == "retired" && mode != "retired missing candidate" && mode != "retired non-directory candidate" {
						if err := os.MkdirAll(r.Candidate.Worktree, 0o700); err != nil {
							t.Fatal(err)
						}
					}
					readPR := func(context.Context, string, ...string) (string, error) {
						providers++
						return "", errors.New("selected initial provider refusal")
					}
					if kind == "retired" {
						_, err = acknowledgeRetiredPublication(t.Context(), WorktreeMergeRetiredPublicationAcknowledgementOptions{ProjectsRoot: root, Receipt: input, Apply: apply, Actor: actor, Reason: "review"}, defaultRunner, read, worktreeMergeReceiptSHA256, persistRetiredPublicationAcknowledgement, readPR)
					} else {
						_, err = acknowledgeStrandedPullRequestLanding(t.Context(), WorktreeMergeStrandedLandingAcknowledgementOptions{ProjectsRoot: root, Receipt: input, Apply: apply, Actor: actor, Reason: "review"}, read, worktreeMergeReceiptSHA256, persistStrandedLandingAcknowledgement, readPR)
					}
					if err == nil || (want != "" && !strings.Contains(err.Error(), want)) {
						t.Fatalf("%s error=%v want %q", mode, err, want)
					}
					wantProviders := 0
					if mode == "provider" {
						wantProviders = 1
					}
					if providers != wantProviders {
						t.Fatalf("%s reached provider %d times", mode, providers)
					}
					if mode == "read" || mode == "retired reread" {
						if !errors.Is(err, sentinel) {
							t.Fatalf("lost selected read: %v", err)
						}
					}
					after, readErr := os.ReadFile(r.ReceiptPath)
					if readErr != nil || string(after) != string(before) {
						t.Fatalf("historical receipt changed: %v", readErr)
					}
					if mode != "lock" {
						lock, lockErr := AcquireOperationLock(root, r.Lane, true)
						if lockErr != nil {
							t.Fatalf("operation lock retained: %v", lockErr)
						}
						if err := lock.Release(); err != nil {
							t.Fatal(err)
						}
					}
				})
			}
		})
	}
}
