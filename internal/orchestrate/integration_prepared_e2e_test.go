//go:build e2e

package orchestrate

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/landinglane"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// External origin/hosted PR reads are controlled; all custody, clean-tree,
// immutable receipt, source and first-parent Git checks use real private fixtures.
type preparedHostedRunner struct {
	runner.Runner
	origin          string
	failArgs        string
	dir             string
	replies         map[string]string
	originCalls     *int
	originFailureAt int
}

func (r preparedHostedRunner) Run(ctx context.Context, dir, name string, args ...string) (runner.Result, error) {
	if name == "git" && (r.dir == "" || dir == r.dir) && strings.Join(args, " ") == r.failArgs {
		return runner.Result{ExitCode: 2}, fmt.Errorf("controlled git read failed")
	}
	if name == "git" && (r.dir == "" || dir == r.dir) {
		if value, ok := r.replies[strings.Join(args, " ")]; ok {
			return runner.Result{Stdout: value}, nil
		}
	}
	if name == "git" && strings.Join(args, " ") == "remote get-url origin" {
		if r.originCalls != nil {
			(*r.originCalls)++
			if *r.originCalls == r.originFailureAt {
				return runner.Result{}, fmt.Errorf("controlled second origin read refused")
			}
		}
		return runner.Result{Stdout: r.origin + "\n"}, nil
	}
	return r.Runner.Run(ctx, dir, name, args...)
}
func (r preparedHostedRunner) RunOpts(ctx context.Context, dir string, _ runner.RunOptions, name string, args ...string) (runner.Result, error) {
	return r.Run(ctx, dir, name, args...)
}

type preparedMovingPR struct {
	*fakePromoteGitHub
	observations int
}

func (p *preparedMovingPR) OpenPullRequest(context.Context, string, string, string) (int, string, error) {
	p.observations++
	if p.observations == 1 {
		return 7, "", nil
	}
	return 8, "", nil
}
func TestE2EPreparedAdoptionProvesPublishedDescendantWithoutChangingOriginalReceipt(t *testing.T) {
	// Environment is private to this sequential fixture; no external GitHub/auth calls.
	t.Setenv(worktrees.EnvAgentID, "prepared-owner")
	t.Setenv(worktrees.EnvSessionID, "prepared-owner")
	t.Setenv(worktrees.EnvAgentRuntime, "test")
	t.Setenv(worktrees.EnvAgentModel, "test-model")
	f := newEngineFixture(t)
	home, err := wbhome.EnsureRoot(f.githubDir)
	if err != nil {
		t.Fatal(err)
	}
	owner := landinglane.Owner{WBSessionID: "prepared-owner", PID: os.Getpid(), Runtime: "test", Model: "test-model", Command: "prepared fixture"}
	if _, err := session.Register(filepath.Join(home, session.DirName), session.Record{PID: os.Getpid(), WBSessionID: owner.WBSessionID, Runtime: "test", Model: "test-model", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	source := createMergeSource(t, f, "prepared-source", "feature/prepared-source", "source.txt", "source\n")
	// Historical d825 merged two genuinely diverged histories, not a fast-forward.
	writeEngineFile(t, filepath.Join(f.canonical, "target.txt"), "independent target\n")
	runEngineGit(t, f.canonical, "add", "target.txt")
	runEngineGit(t, f.canonical, "commit", "-m", "target: independent advance")
	runEngineGit(t, f.canonical, "push", "origin", "main")
	receipt, err := PrepareWorktreeMerge(t.Context(), WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Lane: LaneGuardRequest{Owner: owner}, AgentID: "prepared-owner", AgentRuntime: "test", Model: "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	actor := currentSessionIdentity()
	if receipt.LaneOwner == nil {
		t.Log("library prepare has no CLI lane-owner attachment")
	}
	receipt.ValidationIdentity = nil
	receipt.Validation = quality.VerificationReport{Repository: receipt.Repository, Revision: receipt.Candidate.SHA, WorkspaceClean: true, Status: quality.StatusSkipped, Results: []quality.VerificationEntry{{Status: quality.StatusSkipped, Detail: "private /home/fixture/prompt token-like-private-material"}}}
	receipt.ValidationDeferral = &WorktreeMergeValidationDeferral{Route: WorktreeMergeRoutePullRequest, CandidateSHA: receipt.Candidate.SHA, Reason: "private /home/fixture/prompt token-like-private-material", RecordedAt: time.Now().UTC()}
	// Real historical shape has no resolved receipt.route, no publication or validation identity.
	receipt.Route = WorktreeMergeRouteDecision{}
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "correction.txt"), "reviewed descendant\n")
	runEngineGit(t, receipt.Candidate.Worktree, "add", "correction.txt")
	runEngineGit(t, receipt.Candidate.Worktree, "commit", "-m", "fix: reviewed descendant")
	head := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", "HEAD:refs/heads/"+receipt.Candidate.Branch)
	bin := t.TempDir()
	script := `#!/bin/sh
set -eu
case "$*" in
 'api repos/acme/app/pulls/7 --include'|'api repos/acme/app/pulls/7'|'api repos/acme/app/pulls/8 --include'|'api repos/acme/app/pulls/8') number="${2##*/}";printf '{"number":%s,"state":"%s","head":{"ref":"%s","sha":"%s","repo":{"full_name":"acme/app"}},"base":{"ref":"main","repo":{"full_name":"acme/app"}}}\n' "$number" "$WB_TEST_PREPARED_PR_STATE" "$WB_TEST_PREPARED_BRANCH" "$WB_TEST_PREPARED_HEAD";;
 'api repos/acme/app/branches/main --include'|'api repos/acme/app/branches/main') printf '%s\n' '{"protected":true,"protection":{"required_status_checks":{},"required_pull_request_reviews":{}}}';;
 'api repos/acme/app/branches/main/protection/required_status_checks --include'|'api repos/acme/app/branches/main/protection/required_status_checks') printf '%s\n' "$WB_TEST_PREPARED_POLICY";;
 'api repos/acme/app/rules/branches/main?per_page=100 --include'|'api repos/acme/app/rules/branches/main?per_page=100') printf '%s\n' '[]';;
 *) echo "unexpected controlled provider read" >&2;exit 2;;
esac
`
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("WB_TEST_PREPARED_PR_STATE", "open")
	t.Setenv("WB_TEST_PREPARED_BRANCH", receipt.Candidate.Branch)
	t.Setenv("WB_TEST_PREPARED_HEAD", head)
	t.Setenv("WB_TEST_PREPARED_POLICY", `{"strict":true,"contexts":["CI"],"checks":[]}`)
	gh := &fakePromoteGitHub{fakeIntegrationGitHub: &fakeIntegrationGitHub{comments: map[int64]CommitComment{}}, number: 7}
	archive := &legacyArchiveFixture{fakeIntegrationGitHub: gh.fakeIntegrationGitHub}
	o := LegacyAdoptOptions{PromoteOptions: PromoteOptions{Repository: receipt.Repository, Integration: receipt.Candidate.Branch, To: "main", Worktree: receipt.Candidate.Worktree, GitHub: gh, run: preparedHostedRunner{Runner: runner.New(), origin: "https://github.com/acme/app.git"}}, ProjectsRoot: f.githubDir, Receipt: receipt.ReceiptPath, Prepared: true, actor: actor, archive: archive}
	native := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "show", "-s", "--format=%P%x00%B", receipt.Candidate.SHA))
	parents, message, _ := strings.Cut(native, "\x00")
	t.Logf("native prepared merge parentMatch=%t laneTrailer=%t", parents == receipt.TargetSHA+" "+receipt.Sources[0].SHA, carriesLaneTrailer(SourceCommit{Body: message}))
	dry, err := AdoptLegacyIntegration(t.Context(), o)
	if err != nil || dry.Applied || dry.Record.Prepared == nil || dry.Record.Prepared.PublishedHead != head {
		t.Fatalf("dry=%+v error=%v", dry, err)
	}
	if strings.Contains(legacyJSON(dry.Record), "token-like-private-material") || strings.Contains(legacyJSON(dry.Record), "/home/fixture") {
		t.Fatal("private receipt prose leaked into portable record")
	}
	o.Apply = true
	adopted, err := AdoptLegacyIntegration(t.Context(), o)
	if err != nil || !adopted.Applied || archive.creates != 1 {
		t.Fatal(adopted, err)
	}
	retry := o
	retry.actor = ReviewerIdentity{}
	again, err := AdoptLegacyIntegration(t.Context(), retry)
	if err != nil || again.Binding.URL != adopted.Binding.URL || archive.creates != 1 {
		t.Fatal(again, err)
	}
	if current, err := os.ReadFile(receipt.ReceiptPath); err != nil || !bytes.Equal(current, original) {
		t.Fatal("original receipt changed", err)
	}
	if strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD")) != head {
		t.Fatal("adoption rewrote source history")
	}
	for _, fault := range []string{"source read", "branch read", "branch drift", "missing ancestor", "parent read", "parent mismatch", "origin read", "second origin read", "foreign origin", "PR read", "missing PR"} {
		t.Run(fault, func(t *testing.T) {
			bad := o
			calls := 0
			port := preparedHostedRunner{Runner: runner.New(), origin: "https://github.com/acme/app.git", dir: receipt.Candidate.Worktree}
			switch fault {
			case "source read":
				port.dir = source.WorktreeDir
				port.failArgs = "branch --show-current"
			case "branch read":
				port.failArgs = "branch --show-current"
			case "branch drift":
				port.replies = map[string]string{"branch --show-current": "foreign"}
			case "missing ancestor":
				port.replies = map[string]string{"rev-list --first-parent " + head: head}
			case "parent read":
				port.failArgs = "show -s --format=%P%x00%B " + receipt.Candidate.SHA
			case "parent mismatch":
				port.replies = map[string]string{"show -s --format=%P%x00%B " + receipt.Candidate.SHA: head + "\x00unrelated"}
			case "origin read":
				port.failArgs = "remote get-url origin"
			case "second origin read":
				port.originCalls = &calls
				port.originFailureAt = 2
			case "foreign origin":
				port.origin = "https://github.com/foreign/app.git"
			case "PR read":
				bad.GitHub = &fakePromoteGitHub{fakeIntegrationGitHub: &fakeIntegrationGitHub{failAt: 1}, number: 7}
			case "missing PR":
				bad.GitHub = &fakePromoteGitHub{fakeIntegrationGitHub: &fakeIntegrationGitHub{}, number: 0}
			}
			bad.run = port
			if _, err := AdoptLegacyIntegration(t.Context(), bad); err == nil {
				t.Fatal("accepted", fault)
			}
		})
	}
	for _, nth := range []int{1, 2, 3, 4} {
		t.Run(fmt.Sprintf("immutable snapshot read %d", nth), func(t *testing.T) {
			reads := 0
			reader := func(path string) (WorktreeMergeReceipt, string, error) {
				reads++
				if reads == nth {
					return WorktreeMergeReceipt{}, "", os.ErrPermission
				}
				return readWorktreeMergeReceiptSnapshot(path)
			}
			if _, err := adoptPreparedIntegrationWithSnapshot(t.Context(), o, reader); err == nil {
				t.Fatal("read failure accepted")
			}
		})
	}
	for _, existing := range []bool{true, false} {
		t.Run(fmt.Sprintf("receipt changes before append existing=%t", existing), func(t *testing.T) {
			bad := o
			if !existing {
				bad.archive = &legacyArchiveFixture{fakeIntegrationGitHub: &fakeIntegrationGitHub{comments: map[int64]CommitComment{}}}
			}
			reads := 0
			reader := func(path string) (WorktreeMergeReceipt, string, error) {
				r, d, e := readWorktreeMergeReceiptSnapshot(path)
				reads++
				if reads == 4 {
					d = strings.Repeat("0", 64)
				}
				return r, d, e
			}
			if _, err := adoptPreparedIntegrationWithSnapshot(t.Context(), bad, reader); err == nil || !strings.Contains(err.Error(), "receipt changed") {
				t.Fatal("changed snapshot accepted", err)
			}
		})
	}
	t.Run("foreign current claim owner", func(t *testing.T) {
		t.Setenv(worktrees.EnvAgentID, "replacement-owner")
		if _, err := AdoptLegacyIntegration(t.Context(), o); err == nil {
			t.Fatal("replacement claim identity accepted")
		}
	})
	t.Run("unwritable operation lock", func(t *testing.T) {
		directory := filepath.Join(home, "worktrees", receipt.Lane)
		backup := directory + ".retained"
		if err := os.Rename(directory, backup); err != nil {
			t.Fatal(err)
		}
		defer os.Rename(backup, directory)
		if err := os.WriteFile(directory, []byte("not a lock directory"), 0600); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(directory)
		if _, err := AdoptLegacyIntegration(t.Context(), o); err == nil {
			t.Fatal("invalid lock path accepted")
		}
	})
	t.Run("conflicting prepared archive", func(t *testing.T) {
		conflicting := dry.Record
		copyPrepared := *conflicting.Prepared
		copyPrepared.PublishedHead = receipt.Candidate.SHA
		conflicting.Prepared = &copyPrepared
		body := preparedRecordPrefix + legacyJSON(conflicting)
		binding := LegacyBinding{URL: adopted.Binding.URL, Digest: legacyHash([]byte(body))}
		bad := o
		bad.archive = &legacyArchiveFixture{fakeIntegrationGitHub: &fakeIntegrationGitHub{comments: map[int64]CommitComment{90: {CommitID: receipt.Candidate.SHA, Body: body}}}, records: []LegacyBinding{binding}}
		if _, err := AdoptLegacyIntegration(t.Context(), bad); err == nil || !strings.Contains(err.Error(), "conflicting prepared") {
			t.Fatal("conflicting archive accepted", err)
		}
	})
	t.Run("PR replacement during archival read", func(t *testing.T) {
		bad := o
		bad.GitHub = &preparedMovingPR{fakePromoteGitHub: &fakePromoteGitHub{fakeIntegrationGitHub: &fakeIntegrationGitHub{}, number: 7}}
		if _, err := AdoptLegacyIntegration(t.Context(), bad); err == nil || !strings.Contains(err.Error(), "publication changed") {
			t.Fatal("replacement PR adopted", err)
		}
	})
	// Current authorization/publication facts must be authoritative on every retry.
	for _, fault := range []string{"foreign owner", "dirty", "nonancestor", "wrong PR head", "closed PR", "no strict policy", "unknown remote", "git error", "tampered receipt"} {
		t.Run(fault, func(t *testing.T) {
			bad := o
			switch fault {
			case "foreign owner":
				bad.actor.Session = "foreign"
			case "dirty":
				writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "dirty.txt"), "dirty\n")
				defer os.Remove(filepath.Join(receipt.Candidate.Worktree, "dirty.txt"))
			case "nonancestor":
				bad.PromoteOptions.run = preparedHostedRunner{Runner: runner.New(), origin: "https://github.com/acme/app.git", failArgs: "rev-list --first-parent " + head}
			case "wrong PR head":
				t.Setenv("WB_TEST_PREPARED_HEAD", receipt.TargetSHA)
			case "closed PR":
				t.Setenv("WB_TEST_PREPARED_PR_STATE", "closed")
			case "no strict policy":
				t.Setenv("WB_TEST_PREPARED_POLICY", `{"strict":false,"contexts":["CI"],"checks":[]}`)
			case "unknown remote":
				bad.PromoteOptions.run = preparedHostedRunner{Runner: runner.New(), origin: "https://foreign.example/acme/app"}
			case "git error":
				bad.PromoteOptions.run = preparedHostedRunner{Runner: runner.New(), origin: "https://github.com/acme/app.git", failArgs: "rev-parse HEAD"}
			case "tampered receipt":
				if err := os.WriteFile(receipt.ReceiptPath, []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
				defer os.WriteFile(receipt.ReceiptPath, original, 0600)
			}
			if _, err := AdoptLegacyIntegration(t.Context(), bad); err == nil {
				t.Fatal("accepted", fault)
			}
			if archive.creates != 1 {
				t.Fatal("refusal appended evidence", fault)
			}
		})
	}
}
