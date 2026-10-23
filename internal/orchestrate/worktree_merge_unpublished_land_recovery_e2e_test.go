//go:build e2e

package orchestrate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

//nolint:paralleltest // Native cleanup uses the process-wide GitHub executable fixture.
func TestE2ERetiredUnpublishedLandFailureCanReleaseItsLane(t *testing.T) {
	installWorktreeMergeDirectGH(t)
	f, r := unpublishedOwnerFixture(t)
	// A separate fixture target lets ordinary WB cleanup retire the candidate
	// without putting it on the receipted target or retiring its source.
	runEngineGit(t, r.Candidate.Worktree, "push", "origin", "HEAD:refs/heads/fixture-archive")
	payload := "[]"
	var lookupError error
	ctx := githubobserver.WithReader(context.Background(), githubobserver.Reader{Read: func(_ context.Context, dir string, args ...string) ([]byte, error) {
		if dir != f.canonical || strings.Join(args, " ") != "pr list --repo acme/app --head "+r.Candidate.Branch+" --state all --json number,url,headRefName --limit 1000" {
			t.Fatalf("unexpected publication lookup: %s %q", dir, args)
		}
		return []byte(payload), lookupError
	}})
	retired, err := worktrees.Cleanup(ctx, worktrees.CleanupOptions{ProjectsRoot: f.githubDir, Task: r.Candidate.Task, Base: "fixture-archive", ExplicitBase: true, Apply: true})
	if err != nil || len(retired.Results) != 1 || !retired.Results[0].Applied {
		t.Fatalf("native candidate retirement: %+v %v", retired, err)
	}
	r.Phase = WorktreeMergePhaseLand
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	home, err := wbhome.Root(f.githubDir)
	if err != nil {
		t.Fatal(err)
	}
	terminals, err := filepath.Glob(filepath.Join(home, "worklogs", r.Candidate.Task, "runs", "*", "terminals", "*.json"))
	if err != nil || len(terminals) != 1 {
		t.Fatalf("native terminal: %q %v", terminals, err)
	}
	outboxes, err := filepath.Glob(filepath.Join(home, "worklogs", r.Candidate.Task, "outbox", "*-sealed.json"))
	if err != nil || len(outboxes) != 1 {
		t.Fatalf("native outbox: %q %v", outboxes, err)
	}
	original, err := os.ReadFile(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	probe := func(t *testing.T) {
		t.Helper()
		o := unpublishedOwnerOptions(f, r)
		o.Apply = false
		if _, err := AcknowledgeUnpublishedValidationFailure(ctx, o); err == nil {
			t.Fatal("unsafe recovery accepted")
		}
		if _, err := os.Stat(unpublishedValidationFailureAcknowledgementPath(r.ReceiptPath)); !os.IsNotExist(err) {
			t.Fatalf("refusal wrote acknowledgement: %v", err)
		}
	}
	for _, mode := range []string{"recorded PR", "recorded publication", "recorded landing", "wrong status", "live checkout", "registered checkout", "local branch", "missing terminal", "wrong terminal head", "incomplete terminal", "mismatched outbox", "remote branch", "PR history", "lookup unavailable", "target contains candidate", "dirty source", "non-descendant source"} {
		//nolint:paralleltest // Each row mutates and restores one isolated native fixture synchronously.
		t.Run(mode, func(t *testing.T) {
			bad := r
			switch mode {
			case "recorded PR":
				bad.PullRequest = "7"
			case "recorded publication":
				bad.PublishedCandidateSHA = r.Candidate.SHA
			case "recorded landing":
				bad.LandingSHA = r.Candidate.SHA
			case "wrong status":
				bad.Status = WorktreeMergePreparing
			case "live checkout":
				if err := os.MkdirAll(r.Candidate.Worktree, 0700); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(r.Candidate.Worktree) })
			case "registered checkout":
				runEngineGit(t, f.canonical, "worktree", "add", "--detach", r.Candidate.Worktree, r.Candidate.SHA)
				if err := os.RemoveAll(r.Candidate.Worktree); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { runEngineGit(t, f.canonical, "worktree", "prune") })
			case "local branch":
				runEngineGit(t, f.canonical, "branch", r.Candidate.Branch, r.Candidate.SHA)
				t.Cleanup(func() { runEngineGit(t, f.canonical, "branch", "-D", r.Candidate.Branch) })
			case "missing terminal", "wrong terminal head", "incomplete terminal", "mismatched outbox":
				path := terminals[0]
				if mode == "mismatched outbox" {
					path = outboxes[0]
				}
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.WriteFile(path, before, 0600); err != nil {
						t.Error(err)
					}
				})
				if mode == "missing terminal" {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				} else {
					var value map[string]any
					if err := json.Unmarshal(before, &value); err != nil {
						t.Fatal(err)
					}
					if mode == "incomplete terminal" {
						value["sealed_at"] = "0001-01-01T00:00:00Z"
					} else {
						value["final_commit"] = r.TargetSHA
					}
					data, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, data, 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "remote branch":
				runEngineGit(t, f.canonical, "push", "origin", r.Candidate.SHA+":refs/heads/"+r.Candidate.Branch)
				t.Cleanup(func() { runEngineGit(t, f.canonical, "push", "origin", ":refs/heads/"+r.Candidate.Branch) })
			case "PR history":
				payload = `[{"number":7,"headRefName":"` + r.Candidate.Branch + `"}]`
				t.Cleanup(func() { payload = "[]" })
			case "lookup unavailable":
				lookupError = errors.New("publication observer unavailable")
				t.Cleanup(func() { lookupError = nil })
			case "target contains candidate":
				runEngineGit(t, f.canonical, "push", "origin", r.Candidate.SHA+":refs/heads/main")
				t.Cleanup(func() { runEngineGit(t, f.repository.CloneURL, "update-ref", "refs/heads/main", r.TargetSHA) })
			case "dirty source":
				path := filepath.Join(r.Sources[0].Worktree, "dirty.txt")
				if err := os.WriteFile(path, []byte("preserved"), 0600); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Remove(path) })
			case "non-descendant source":
				runEngineGit(t, r.Sources[0].Worktree, "reset", "--hard", r.TargetSHA)
				t.Cleanup(func() { runEngineGit(t, r.Sources[0].Worktree, "reset", "--hard", r.Sources[0].SHA) })
			}
			if err := persistWorktreeMergeReceipt(bad); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.WriteFile(r.ReceiptPath, original, 0600); err != nil {
					t.Error(err)
				}
			})
			probe(t)
		})
	}
	for _, mode := range []string{"canonical", "registry read", "branch read"} {
		//nolint:paralleltest // Negative adapters share the restored native retirement proof.
		t.Run(mode, func(t *testing.T) {
			sentinel := errors.New("selected " + mode)
			canonical := worktrees.CanonicalRepositoryPath
			if mode == "canonical" {
				canonical = func(string, string) (string, error) { return "", sentinel }
			}
			run := retirementOwnerRunner{Runner: defaultRunner, before: func(_ context.Context, _ string, name string, args []string) error {
				if name == "git" && len(args) > 0 && (mode == "registry read" && args[0] == "worktree" || mode == "branch read" && args[0] == "for-each-ref") {
					return sentinel
				}
				return nil
			}}
			if _, err := requireRetiredUnpublishedLandCandidate(ctx, f.githubDir, r, run, canonical); !errors.Is(err, sentinel) {
				t.Fatalf("lost observation refusal: %v", err)
			}
		})
	}
	before := retirementOwnerReceiptBytes(t, r)
	dry := unpublishedOwnerOptions(f, r)
	dry.Apply = false
	if ack, err := AcknowledgeUnpublishedValidationFailure(ctx, dry); err != nil || ack.ID == "" {
		t.Fatalf("dry recovery: %+v %v", ack, err)
	}
	ack, err := AcknowledgeUnpublishedValidationFailure(ctx, unpublishedOwnerOptions(f, r))
	if err != nil || ack.ID == "" || len(ack.PreservedSources) != 1 {
		t.Fatalf("retired land acknowledgement: %+v %v", ack, err)
	}
	before()
	if ok, err := hasUnpublishedValidationFailureAcknowledgement(r); err != nil || !ok {
		t.Fatalf("acknowledgement receipt: %v %v", ok, err)
	}
	current, err := os.ReadFile(r.ReceiptPath)
	if err != nil || !bytes.Equal(current, original) {
		t.Fatalf("historical receipt changed: %v", err)
	}
}
