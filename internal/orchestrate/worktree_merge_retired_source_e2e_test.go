//go:build e2e

package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

//nolint:paralleltest // Native cleanup uses the process-wide GitHub executable fixture.
func TestE2EFailedLandAcknowledgesSeparatelySquashLandedRetiredSource(t *testing.T) {
	installWorktreeMergeDirectGH(t)
	f, r := unpublishedOwnerFixture(t)
	source := r.Sources[0]
	runEngineGit(t, r.Candidate.Worktree, "push", "origin", "HEAD:refs/heads/fixture-archive")
	// Produce a real squash: same tree as source, different commit identity.
	runEngineGit(t, f.canonical, "merge", "--squash", source.SHA)
	runEngineGit(t, f.canonical, "commit", "-m", "fixture: separately squash source")
	landing := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, f.canonical, "push", "origin", "main")
	directGH, err := exec.LookPath("gh")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	payload := fmt.Sprintf(`[{"number":450,"html_url":"https://github.com/acme/app/pull/450","state":"closed","merged_at":"2026-10-20T21:17:24Z","merge_commit_sha":%q,"head":{"ref":%q,"sha":%q},"base":{"ref":"main","sha":%q}}]`, landing, source.Branch, source.SHA, r.TargetSHA)
	t.Setenv("WB_TEST_RETIRED_SOURCE_PRS", payload)
	t.Setenv("WB_TEST_RETIRED_DIRECT_GH", directGH)
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nif [ \"$1 $2\" = \"api --paginate\" ]; then printf '%s\\n' \"$WB_TEST_RETIRED_SOURCE_PRS\"; else exec \"$WB_TEST_RETIRED_DIRECT_GH\" \"$@\"; fi\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	mode := ""
	var cleanupReport string
	viewCalls := 0
	runEngineGit(t, f.canonical, "commit", "--allow-empty", "-m", "fixture: later target")
	ctx := githubobserver.WithReader(t.Context(), githubobserver.Reader{
		Read: func(_ context.Context, dir string, args ...string) ([]byte, error) {
			if mode == "missing remote" {
				return nil, errors.New("fixture unavailable")
			}
			if len(args) > 1 && args[0] == "pr" && args[1] == "list" {
				return []byte("[]"), nil
			}
			if len(args) > 1 && args[0] == "pr" && args[1] == "view" {
				viewCalls++
				if mode == "prewrite changed proof" && viewCalls > 1 {
					return nil, errors.New("fixture changed before write")
				}
				head, state, merge := source.SHA, "MERGED", landing
				if mode == "wrong PR head" {
					head = r.TargetSHA
				}
				if mode == "open PR" {
					state = "OPEN"
				}
				if mode == "wrong landing" {
					merge = r.TargetSHA
				}
				return []byte(fmt.Sprintf(`{"state":%q,"mergedAt":"2026-10-20T21:17:24Z","headRefOid":%q,"baseRefName":"main","mergeCommit":{"oid":%q}}`, state, head, merge)), nil
			}
			t.Fatalf("unexpected read %s %q", dir, args)
			return nil, nil
		},
		Get: func(_ context.Context, req githubobserver.GetRequest) (githubobserver.Response, error) {
			if mode == "missing remote" {
				return githubobserver.Response{}, errors.New("fixture unavailable")
			}
			var body string
			switch {
			case strings.HasSuffix(req.Endpoint, "/pulls"):
				body = fmt.Sprintf(`[{"number":450,"html_url":"https://github.com/acme/app/pull/450","state":"closed","merged_at":"2026-10-20T21:17:24Z","merge_commit_sha":%q,"head":{"ref":%q,"sha":%q},"base":{"ref":"main","sha":%q}}]`, landing, source.Branch, source.SHA, r.TargetSHA)
			case strings.Contains(req.Endpoint, "/git/ref/heads/"):
				body = fmt.Sprintf(`{"object":{"sha":%q}}`, landing)
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
				if mode == "landing absent" && req.Target == landing {
					status = "diverged"
				}
				body = fmt.Sprintf(`{"status":%q,"base_commit":{"sha":%q},"merge_base_commit":{"sha":%q}}`, status, req.Target, base)
				if req.Target == r.TargetSHA && req.Head == source.SHA {
					if mode == "missing cleanup digest" {
						if err := os.Remove(cleanupReport); err != nil {
							t.Fatal(err)
						}
					}
					if mode == "target advanced" {
						runEngineGit(t, f.canonical, "push", "origin", "HEAD:refs/heads/main")
					}
				}
			case strings.Contains(req.Endpoint, "/git/commits/"):
				tree := strings.TrimSpace(runEngineGit(t, f.repository.CloneURL, "rev-parse", req.Target+"^{tree}"))
				body = fmt.Sprintf(`{"tree":{"sha":%q}}`, tree)
				if mode == "late source reappearance" {
					if err := os.MkdirAll(source.Worktree, 0700); err != nil {
						t.Fatal(err)
					}
				}
			default:
				t.Fatalf("unexpected endpoint %+v", req)
			}
			return githubobserver.Response{Body: []byte(body)}, nil
		},
	})
	clean, err := worktrees.Cleanup(ctx, worktrees.CleanupOptions{ProjectsRoot: f.githubDir, Task: source.Task, Apply: true})
	if err != nil || len(clean.Results) != 1 || !clean.Results[0].Applied {
		t.Fatalf("native source cleanup: %+v %v", clean, err)
	}
	clean, err = worktrees.Cleanup(ctx, worktrees.CleanupOptions{ProjectsRoot: f.githubDir, Task: r.Candidate.Task, Base: "fixture-archive", ExplicitBase: true, Apply: true})
	if err != nil || len(clean.Results) != 1 || !clean.Results[0].Applied {
		t.Fatalf("native candidate cleanup: %+v %v", clean, err)
	}
	r.Phase = WorktreeMergePhaseLand
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	home, err := wbhome.Root(f.githubDir)
	if err != nil {
		t.Fatal(err)
	}
	terminals, _ := filepath.Glob(filepath.Join(home, "worklogs", source.Task, "runs", "*", "terminals", "*.json"))
	outboxes, _ := filepath.Glob(filepath.Join(home, "worklogs", source.Task, "outbox", "*-sealed.json"))
	if len(terminals) != 1 || len(outboxes) != 1 {
		t.Fatal("missing native immutable proof")
	}
	cleanupProof, err := worktrees.FindTerminalCleanupProof(f.githubDir, r.Repository, r.Target, source.Task, source.Worktree, source.Branch)
	if err != nil {
		t.Fatal(err)
	}
	cleanupReport = cleanupProof.ReportPath
	cleanupBytes, err := os.ReadFile(cleanupReport)
	if err != nil {
		t.Fatal(err)
	}
	// Every failure is an observation fault around the same real retired source.
	for _, failure := range []string{"cleanup missing", "cleanup wrong head", "cleanup wrong landing", "missing cleanup digest", "target advanced", "prewrite changed proof"} {
		//nolint:paralleltest // Rows restore the same native target and private cleanup fixture.
		t.Run(failure, func(t *testing.T) {
			mode = failure
			viewCalls = 0
			defer func() { mode = "" }()
			defer os.WriteFile(cleanupReport, cleanupBytes, 0600)
			switch failure {
			case "cleanup missing":
				if err := os.Remove(cleanupReport); err != nil {
					t.Fatal(err)
				}
			case "cleanup wrong head", "cleanup wrong landing":
				var report map[string]any
				if err := json.Unmarshal(cleanupBytes, &report); err != nil {
					t.Fatal(err)
				}
				results := report["results"].([]any)
				result := results[0].(map[string]any)
				if failure == "cleanup wrong head" {
					result["head_sha"] = r.TargetSHA
				} else {
					result["merged_pull_request"].(map[string]any)["merge_sha"] = r.TargetSHA
				}
				raw, _ := json.Marshal(report)
				if err := os.WriteFile(cleanupReport, raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "target advanced":
				defer runEngineGit(t, f.canonical, "push", "--force", "origin", landing+":refs/heads/main")
			}
			o := unpublishedOwnerOptions(f, r)
			o.Apply = failure == "prewrite changed proof"
			if _, err := AcknowledgeUnpublishedValidationFailure(ctx, o); err == nil {
				t.Fatal("unsafe cleanup/target recovery accepted")
			}
		})
	}
	// Native query failures and reappearing local identities remain refusals.
	for _, argv := range [][]string{{"git", "worktree", "list", "--porcelain", "-z"}, {"git", "for-each-ref", "--format=%(refname)", "refs/heads/" + source.Branch}} {
		query := &absorbedSourceProofRunner{Runner: defaultRunner, t: t, repo: f.canonical, fail: argv, cause: errors.New("fixture query unavailable")}
		if _, err := proveRetiredUnpublishedFailureSource(ctx, f.githubDir, r, source, f.canonical, query); err == nil {
			t.Fatal("unavailable native query accepted")
		}
	}
	runEngineGit(t, f.canonical, "update-ref", "refs/heads/"+source.Branch, source.SHA)
	if err := requireAbsentRetiredSource(ctx, f.canonical, source, defaultRunner); err == nil {
		t.Fatal("reappeared source branch accepted")
	}
	runEngineGit(t, f.canonical, "update-ref", "-d", "refs/heads/"+source.Branch)
	runEngineGit(t, f.canonical, "worktree", "add", "--detach", source.Worktree, source.SHA)
	if err := os.RemoveAll(source.Worktree); err != nil {
		t.Fatal(err)
	}
	if err := requireAbsentRetiredSource(ctx, f.canonical, source, defaultRunner); err == nil {
		t.Fatal("registered missing source accepted")
	}
	runEngineGit(t, f.canonical, "worktree", "prune")
	for _, failure := range []string{"wrong PR head", "open PR", "wrong landing", "landing absent", "missing remote", "source reappears", "late source reappearance", "missing terminal", "missing outbox", "wrong terminal head", "invalid landing terminal", "forged outbox", "forged claim"} {
		//nolint:paralleltest // Rows mutate and restore one isolated native evidence fixture.
		t.Run(failure, func(t *testing.T) {
			mode = failure
			defer func() { mode = "" }()
			switch failure {
			case "late source reappearance":
				defer os.RemoveAll(source.Worktree)
			case "source reappears":
				if err := os.MkdirAll(source.Worktree, 0700); err != nil {
					t.Fatal(err)
				}
				defer os.RemoveAll(source.Worktree)
			case "missing terminal":
				if err := os.Rename(terminals[0], terminals[0]+".saved"); err != nil {
					t.Fatal(err)
				}
				defer os.Rename(terminals[0]+".saved", terminals[0])
			case "missing outbox":
				if err := os.Rename(outboxes[0], outboxes[0]+".saved"); err != nil {
					t.Fatal(err)
				}
				defer os.Rename(outboxes[0]+".saved", outboxes[0])
			case "wrong terminal head", "invalid landing terminal", "forged outbox", "forged claim":
				path := terminals[0]
				if failure == "forged outbox" {
					path = outboxes[0]
				}
				if failure == "forged claim" {
					path = filepath.Join(filepath.Dir(filepath.Dir(terminals[0])), "claims", filepath.Base(terminals[0]))
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				defer os.WriteFile(path, raw, 0600)
				var record map[string]any
				if err := json.Unmarshal(raw, &record); err != nil {
					t.Fatal(err)
				}
				if failure == "wrong terminal head" {
					record["final_commit"] = r.TargetSHA
				}
				if failure == "invalid landing terminal" {
					record["landed"] = nil
				}
				if failure == "forged outbox" {
					record["disposition"] = "removed"
				}
				if failure == "forged claim" {
					record["branch"] = "forged"
				}
				changed, _ := json.Marshal(record)
				if err := os.WriteFile(path, changed, 0600); err != nil {
					t.Fatal(err)
				}
			}
			o := unpublishedOwnerOptions(f, r)
			o.Apply = false
			if _, err := AcknowledgeUnpublishedValidationFailure(ctx, o); err == nil {
				t.Fatal("unsafe retired-source recovery accepted")
			}
		})
	}
	o := unpublishedOwnerOptions(f, r)
	o.Apply = false
	dry, err := AcknowledgeUnpublishedValidationFailure(ctx, o)
	if err != nil || len(dry.LandedSources) != 1 || len(dry.PreservedSources) != 0 || dry.LandedSources[0].Kind != "tree-identical" {
		t.Fatalf("independent squash landing proof: %+v %v", dry, err)
	}
	if err := validateUnpublishedSourceProofCoverage(dry, r); err != nil {
		t.Fatal(err)
	}
	duplicate := dry
	duplicate.PreservedSources = []WorktreeMergeSource{source}
	if err := validateUnpublishedSourceProofCoverage(duplicate, r); err == nil {
		t.Fatal("duplicate proof accepted")
	}
	missing := dry
	missing.LandedSources = nil
	if err := validateUnpublishedSourceProofCoverage(missing, r); err == nil {
		t.Fatal("missing proof accepted")
	}
	before := retirementOwnerReceiptBytes(t, r)
	ack, err := AcknowledgeUnpublishedValidationFailure(ctx, unpublishedOwnerOptions(f, r))
	if err != nil || ack.ID == "" {
		t.Fatalf("apply independent landing acknowledgement: %+v %v", ack, err)
	}
	before()
	if ok, err := hasUnpublishedValidationFailureAcknowledgement(r); err != nil || !ok {
		t.Fatalf("replay: %v %v", ok, err)
	}
	// Replay rejects edited immutable proof fields even when the historical
	// receipt remains byte-identical. Source classifications cannot overlap.
	ackPath := unpublishedValidationFailureAcknowledgementPath(r.ReceiptPath)
	ackBytes, err := os.ReadFile(ackPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"landing", "cleanup digest", "source", "kind"} {
		bad := ack
		bad.LandedSources = append([]WorktreeMergeRetiredSourceProof(nil), ack.LandedSources...)
		switch field {
		case "landing":
			bad.LandedSources[0].LandingSHA = r.TargetSHA
		case "cleanup digest":
			bad.LandedSources[0].CleanupSHA256 = "changed"
		case "source":
			bad.LandedSources[0].Source.SHA = r.TargetSHA
		case "kind":
			bad.LandedSources[0].Kind = "unknown"
		}
		if unpublishedValidationFailureAcknowledgementID(bad) == ack.ID {
			t.Fatalf("unbound immutable field %s", field)
		}
		raw, _ := json.Marshal(bad)
		if err := os.WriteFile(ackPath, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if ok, err := hasUnpublishedValidationFailureAcknowledgement(r); err == nil || ok {
			t.Fatalf("edited %s proof accepted", field)
		}
	}
	if err := os.WriteFile(ackPath, ackBytes, 0600); err != nil {
		t.Fatal(err)
	}
	mixedReceipt := r
	live := source
	live.Task = "still-preserved"
	live.Worktree = source.Worktree + "-live"
	live.Branch = "feature/live"
	mixedReceipt.Sources = append(append([]WorktreeMergeSource(nil), r.Sources...), live)
	mixed := ack
	mixed.PreservedSources = []WorktreeMergeSource{live}
	if err := validateUnpublishedSourceProofCoverage(mixed, mixedReceipt); err != nil {
		t.Fatalf("valid mixed proof: %v", err)
	}
	mixed.PreservedSources = []WorktreeMergeSource{source}
	if err := validateUnpublishedSourceProofCoverage(mixed, mixedReceipt); err == nil {
		t.Fatal("overlapping mixed source proof accepted")
	}
	duplicatePreserved := ack
	duplicatePreserved.LandedSources = nil
	duplicatePreserved.PreservedSources = []WorktreeMergeSource{source, source}
	if err := validateUnpublishedSourceProofCoverage(duplicatePreserved, mixedReceipt); err == nil {
		t.Fatal("duplicate preserved source accepted")
	}
	unknown := ack
	unknown.LandedSources = nil
	foreign := source
	foreign.Task = "foreign"
	unknown.PreservedSources = []WorktreeMergeSource{foreign}
	if err := validateUnpublishedSourceProofCoverage(unknown, r); err == nil {
		t.Fatal("foreign preserved source accepted")
	}
	invalid := ack
	invalid.LandedSources = append([]WorktreeMergeRetiredSourceProof(nil), ack.LandedSources...)
	invalid.LandedSources[0].Kind = "unknown"
	if err := validateUnpublishedSourceProofCoverage(invalid, r); err == nil {
		t.Fatal("unknown landing proof accepted")
	}
}
