//go:build e2e

package orchestrate

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestE2ERefreshedPublicationRebatchPreservesOriginalAndRejectsChangedProof(t *testing.T) {
	f := newEngineFixture(t)
	source := createMergeSource(t, f, "refreshed-source", "feature/refreshed", "source.txt", "source\n")
	opts := WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"}
	r, err := PrepareWorktreeMerge(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	predecessor := r.Candidate.SHA
	writeEngineFile(t, filepath.Join(source.WorktreeDir, "repair.txt"), "repair\n")
	runEngineGit(t, source.WorktreeDir, "add", "repair.txt")
	runEngineGit(t, source.WorktreeDir, "commit", "-m", "fix: repair")
	runEngineGit(t, r.Candidate.Worktree, "merge", "--no-edit", source.Branch)
	r.Candidate.SHA = strings.TrimSpace(runEngineGit(t, r.Candidate.Worktree, "rev-parse", "HEAD"))
	r.Phase, r.Status, r.PullRequest, r.PublishedCandidateSHA = WorktreeMergePhasePrepare, WorktreeMergeConflict, "7", predecessor
	r.SourceRefreshes = []WorktreeMergeSourceRefresh{{RecordedAt: r.CreatedAt, Sources: append([]WorktreeMergeSource(nil), r.Sources...)}}
	r.TargetRefreshes = []WorktreeMergeTargetRefresh{{RecordedAt: r.CreatedAt, PreviousTargetSHA: r.TargetSHA, NewTargetSHA: r.TargetSHA, PreviousCandidateSHA: predecessor, NewCandidateSHA: r.Candidate.SHA}}
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, r.Candidate.Worktree, "push", "origin", r.Candidate.SHA+":refs/heads/"+r.Candidate.Branch)
	current := r.Sources[0]
	current.SHA = strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	bin := t.TempDir()
	script := `#!/bin/sh
set -eu
case "$*" in
 'api repos/acme/app/pulls/7 --include'|'api repos/acme/app/pulls/7')
 state=open; if [ -f "$WB_TEST_CLOSED" ]; then state=closed; fi
 printf '{"number":7,"state":"%s","head":{"ref":"%s","sha":"%s","repo":{"full_name":"acme/app"}},"base":{"ref":"main","repo":{"full_name":"acme/app"}}}\n' "$state" "$WB_TEST_PR_BRANCH" "$WB_TEST_PR_SHA" ;;
 'api --method PATCH repos/acme/app/pulls/7 -f state=closed') touch "$WB_TEST_CLOSED"; printf '{}\n' ;;
 *) echo "unexpected gh: $*" >&2; exit 2 ;;
esac
`
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("WB_TEST_PR_BRANCH", r.Candidate.Branch)
	t.Setenv("WB_TEST_PR_SHA", r.Candidate.SHA)
	closed := filepath.Join(t.TempDir(), "closed")
	t.Setenv("WB_TEST_CLOSED", closed)
	original, err := os.ReadFile(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"no opt in", "wrong target", "no advance", "wrong source custody", "dirty source", "source head drift", "missing source ancestry", "wrong PR", "wrong live head", "lost published ancestor", "lost historical root", "lost target refresh root", "candidate claim base", "source claim base", "tampered operation", "landed"} {
		t.Run(mode, func(t *testing.T) {
			candidate := r
			next := current
			expected := r.TargetSHA
			switch mode {
			case "no opt in":
				expected = ""
			case "wrong target":
				expected = strings.Repeat("a", 40)
			case "no advance":
				next = r.Sources[0]
			case "wrong source custody":
				next.Task = "other"
			case "source head drift":
				writeEngineFile(t, filepath.Join(source.WorktreeDir, "next-repair.txt"), "next repair\n")
				runEngineGit(t, source.WorktreeDir, "add", "next-repair.txt")
				runEngineGit(t, source.WorktreeDir, "commit", "-m", "fix: next repair")
			case "dirty source":
				writeEngineFile(t, filepath.Join(source.WorktreeDir, "dirty.txt"), "dirty\n")
				t.Cleanup(func() {
					if err := os.Remove(filepath.Join(source.WorktreeDir, "dirty.txt")); err != nil {
						t.Error(err)
					}
				})
			case "missing source ancestry":
				next.SHA = strings.Repeat("b", 40)
			case "wrong PR":
				candidate.PullRequest = "8"
			case "wrong live head":
				t.Setenv("WB_TEST_PR_SHA", predecessor)
			case "lost published ancestor":
				candidate.PublishedCandidateSHA = strings.Repeat("c", 40)
			case "lost historical root":
				candidate.SourceRefreshes = []WorktreeMergeSourceRefresh{{RecordedAt: r.CreatedAt, Sources: []WorktreeMergeSource{{Task: next.Task, Worktree: next.Worktree, Branch: next.Branch, SHA: strings.Repeat("d", 40)}}}}
			case "lost target refresh root":
				candidate.TargetRefreshes = []WorktreeMergeTargetRefresh{{PreviousTargetSHA: r.TargetSHA, NewTargetSHA: r.TargetSHA, PreviousCandidateSHA: strings.Repeat("e", 40), NewCandidateSHA: r.Candidate.SHA}}
			case "candidate claim base", "source claim base":
				dir := r.Candidate.Worktree
				if mode == "source claim base" {
					dir = source.WorktreeDir
				}
				view, err := worktrees.LoadWorkLogView(t.Context(), worktrees.LoadWorkLogOptions{ProjectsRoot: f.githubDir, Worktree: dir})
				if err != nil {
					t.Fatal(err)
				}
				path := view.Claim.ClaimPath
				b, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.WriteFile(path, b, 0600); err != nil {
						t.Error(err)
					}
				})
				var claim map[string]any
				if err := json.Unmarshal(b, &claim); err != nil {
					t.Fatal(err)
				}
				claim["base_sha"] = strings.Repeat("f", 40)
				changed, err := json.Marshal(claim)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, changed, 0600); err != nil {
					t.Fatal(err)
				}
			case "tampered operation":
				candidate.ID = "wrong"
			case "landed":
				candidate.LandingSHA = candidate.Candidate.SHA
			}
			if err := persistWorktreeMergeReceipt(candidate); err != nil {
				t.Fatal(err)
			}
			if _, err := validatePreparedWorktreeMergeRebatch(t.Context(), f.githubDir, r.ReceiptPath, r.Repository, r.Target, []WorktreeMergeSource{next}, expected); err == nil {
				t.Fatal("accepted changed proof")
			}
			if err := os.WriteFile(r.ReceiptPath, original, 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
	current.SHA = strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	if err := proveRefreshedPublicationRebatch(t.Context(), f.githubDir, r, []WorktreeMergeSource{current}, strings.Repeat("f", 40)); err == nil || !strings.Contains(err.Error(), "lost captured root") {
		t.Fatalf("unrelated immutable claim base accepted: %v", err)
	}
	draft, err := validatePreparedWorktreeMergeRebatch(context.Background(), f.githubDir, r.ReceiptPath, r.Repository, r.Target, []WorktreeMergeSource{current}, r.TargetSHA)
	if err != nil {
		t.Fatal(err)
	}
	if draft.RefreshedPublicationTargetSHA != r.TargetSHA {
		t.Fatalf("missing target fence: %+v", draft)
	}
	if _, err := os.Stat(rebatchPath(r.ReceiptPath)); !os.IsNotExist(err) {
		t.Fatalf("proof wrote ack: %v", err)
	}
	opts.RebatchReceipt, opts.RebatchPublishedTargetSHA = r.ReceiptPath, r.TargetSHA

	t.Run("audit refuses incomplete and changed proof", func(t *testing.T) {
		for _, mode := range []string{"missing receipt", "missing target fence", "target drift", "target fence without receipt"} {
			t.Run(mode, func(t *testing.T) {
				probe := opts
				probe.RebatchAuditOnly = true
				switch mode {
				case "missing receipt":
					probe.RebatchReceipt = ""
				case "missing target fence":
					probe.RebatchPublishedTargetSHA = ""
				case "target drift":
					probe.RebatchPublishedTargetSHA = strings.Repeat("a", 40)
				case "target fence without receipt":
					probe.RebatchAuditOnly = false
					probe.RebatchReceipt = ""
				}
				if _, err := PrepareWorktreeMerge(t.Context(), probe); err == nil {
					t.Fatal("invalid audit accepted")
				}
			})
		}
	})

	t.Run("audit refuses an occupied operation lock", func(t *testing.T) {
		lock, err := AcquireOperationLock(f.githubDir, r.Lane, true)
		if err != nil {
			t.Fatal(err)
		}
		probe := opts
		probe.RebatchAuditOnly = true
		_, auditErr := PrepareWorktreeMerge(t.Context(), probe)
		if err := lock.Release(); err != nil {
			t.Fatal(err)
		}
		if auditErr == nil {
			t.Fatal("audit ignored operation exclusivity")
		}
	})
	opts.RebatchAuditOnly = true
	audited, err := PrepareWorktreeMerge(t.Context(), opts)
	if err != nil || audited.Status != "rebatch_audited" || audited.RebatchAudit == nil {
		t.Fatalf("audit=%+v err=%v", audited, err)
	}
	if _, err := os.Stat(closed); !os.IsNotExist(err) {
		t.Fatalf("audit retired PR: %v", err)
	}
	opts.RebatchAuditOnly = false
	replacement, err := PrepareWorktreeMerge(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(r.ReceiptPath)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatalf("original changed: %v", err)
	}
	ack, err := readPreparedWorktreeMergeRebatch(rebatchPath(r.ReceiptPath), r)
	if err != nil {
		t.Fatal(err)
	}
	if ack.ClosedPullRequest != "7" || ack.Sources[0] != current || replacement.RebatchOf != r.ReceiptPath {
		t.Fatalf("incomplete proof: %+v", ack)
	}

	ackBytes, err := os.ReadFile(rebatchPath(r.ReceiptPath))
	if err != nil {
		t.Fatal(err)
	}
	replacementBytes, err := os.ReadFile(replacement.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"acknowledgement PR", "replacement PR"} {
		if mode == "acknowledgement PR" {
			changed := ack
			changed.ClosedPullRequest = "other-pr"
			body, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(rebatchPath(r.ReceiptPath), body, 0600); err != nil {
				t.Fatal(err)
			}
		} else {
			changed := replacement
			changed.SupersededPullRequest = "other-pr"
			if err := persistWorktreeMergeReceipt(changed); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := readPreparedWorktreeMergeRebatch(rebatchPath(r.ReceiptPath), r); err == nil {
			t.Fatalf("tampered %s accepted", mode)
		}
		if err := os.WriteFile(rebatchPath(r.ReceiptPath), ackBytes, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(replacement.ReceiptPath, replacementBytes, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, root := range []string{predecessor, r.Candidate.SHA, r.Sources[0].SHA, current.SHA, r.TargetSHA} {
		retained, err := isMergeAncestor(t.Context(), replacement.Candidate.Worktree, root, replacement.Candidate.SHA)
		if err != nil || !retained {
			t.Fatalf("lost root %s: %v", root, err)
		}
	}
	if _, err := os.Stat(closed); err != nil {
		t.Fatal("superseded PR not retired", err)
	}
	t.Run("advanced source omits captured candidate", func(t *testing.T) {
		if err := os.Remove(closed); err != nil {
			t.Fatal(err)
		}
		writeEngineFile(t, filepath.Join(r.Candidate.Worktree, "candidate-only.txt"), "candidate work\n")
		runEngineGit(t, r.Candidate.Worktree, "add", "candidate-only.txt")
		runEngineGit(t, r.Candidate.Worktree, "commit", "-m", "test: retain candidate work")
		candidate := r
		candidate.Candidate.SHA = strings.TrimSpace(runEngineGit(t, r.Candidate.Worktree, "rev-parse", "HEAD"))
		runEngineGit(t, r.Candidate.Worktree, "push", "origin", candidate.Candidate.SHA+":refs/heads/"+r.Candidate.Branch)
		t.Setenv("WB_TEST_PR_SHA", candidate.Candidate.SHA)
		if err := proveRefreshedPublicationRebatch(t.Context(), f.githubDir, candidate, []WorktreeMergeSource{current}, r.TargetSHA); err == nil || !strings.Contains(err.Error(), "advanced source lost captured root") {
			t.Fatalf("lost candidate work accepted: %v", err)
		}
	})

}
