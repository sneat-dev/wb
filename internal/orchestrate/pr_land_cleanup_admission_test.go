package orchestrate

import (
	"errors"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLandingCleanupRefusesUnverifiableLinkEvidence(t *testing.T) {
	t.Parallel()
	for _, fault := range []string{"malformed workspace", "corrupt stream"} {
		t.Run(fault, func(t *testing.T) {
			t.Parallel()
			projects, checkout := t.TempDir(), t.TempDir()
			var evidence string
			if fault == "malformed workspace" {
				evidence = filepath.Join(checkout, "go.work")
			} else {
				store, err := streams.Open(projects)
				if err != nil {
					t.Fatal(err)
				}
				evidence = filepath.Join(store.Root, "broken", "stream.json")
				if err := os.MkdirAll(filepath.Dir(evidence), 0700); err != nil {
					t.Fatal(err)
				}
			}
			original := []byte("{malformed\n")
			if err := os.WriteFile(evidence, original, 0600); err != nil {
				t.Fatal(err)
			}
			refusal := refuseLinkedWorktree(projects, worktrees.ListResult{Task: "candidate", Repository: "acme/app", WorktreeDir: checkout})
			if refusal == nil || refusal.code != "cleanup-unverifiable" || !strings.Contains(refusal.reason, evidence) || !strings.Contains(refusal.command, "inspect") {
				t.Fatalf("unverifiable %s allowed cleanup: %+v", fault, refusal)
			}
			raw, err := os.ReadFile(evidence)
			if err != nil || string(raw) != string(original) {
				t.Fatalf("evidence changed: %q %v", raw, err)
			}
			if info, err := os.Stat(checkout); err != nil || !info.IsDir() {
				t.Fatal("checkout deleted", err)
			}
		})
	}
}

func TestLandingDiagnosticExclusionRequiresReadableGitIdentity(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"candidate read fails", "malformed response", "parent repository", "detached", "different branch", "same source", "different repository", "source read fails", "source empty", "candidate common missing", "source common missing", "invalid source repository"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			candidate, common, otherCommon := t.TempDir(), t.TempDir(), t.TempDir()
			root, branch, candidateCommon := candidate, "candidate", common
			var candidateErr, sourceErr error
			sourceCommon, repository := common, "acme/app"
			switch kind {
			case "candidate read fails":
				candidateErr = errors.New("unreadable candidate")
			case "parent repository":
				root = filepath.Dir(candidate)
			case "detached":
				branch = "HEAD"
			case "different branch":
				branch = "unrelated"
			case "different repository":
				sourceCommon = otherCommon
			case "source read fails":
				sourceErr = errors.New("unreadable source")
			case "source empty":
				sourceCommon = ""
			case "candidate common missing":
				candidateCommon = filepath.Join(candidate, "missing")
			case "source common missing":
				sourceCommon = filepath.Join(candidate, "missing")
			case "invalid source repository":
				repository = "invalid"
			}
			identity := root + "\n" + candidateCommon + "\n" + branch + "\n"
			if kind == "malformed response" {
				identity = "partial\n"
			}
			fake := runnertest.New(t)
			fake.ExpectArgv([]string{"git", "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir", "--abbrev-ref", "HEAD"}, runner.Result{Stdout: identity}, candidateErr)
			if kind == "same source" || kind == "different repository" || strings.HasPrefix(kind, "source ") || kind == "candidate common missing" {
				fake.ExpectArgv([]string{"git", "rev-parse", "--path-format=absolute", "--git-common-dir"}, runner.Result{Stdout: sourceCommon}, sourceErr)
			}
			view := orchCovPullRequestView(t, `{"head":{"ref":"candidate","repo":{"full_name":"`+repository+`"}}}`)
			got := landingDiagnosticIsUnrelated(t.Context(), PullRequestLandOptions{ProjectsRoot: t.TempDir(), run: fake}, view, worktrees.ListDiagnostic{Path: candidate})
			want := kind == "different branch" || kind == "different repository"
			if got != want {
				t.Fatalf("diagnostic exclusion=%t want=%t", got, want)
			}
		})
	}
}

func TestLandingCleanupRefusesUnresolvableLinkStorage(t *testing.T) {
	t.Parallel()
	projects := filepath.Join(t.TempDir(), "cycle")
	if err := os.Symlink(projects, projects); err != nil {
		t.Fatal(err)
	}
	checkout := t.TempDir()
	refusal := refuseLinkedWorktree(projects, worktrees.ListResult{WorktreeDir: checkout})
	if refusal == nil || refusal.code != "cleanup-unverifiable" || !strings.Contains(refusal.reason, "could not be resolved") {
		t.Fatalf("unreadable stream home allowed landing: %+v", refusal)
	}
}

func TestLandingCleanupRequiresAuthoritativeSourceIdentity(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`{"head":{"ref":"candidate"}}`, `{"head":{"ref":"candidate","repo":{"full_name":""}}}`, `{"head":{"repo":{"full_name":"acme/app"}}}`} {
		view := orchCovPullRequestView(t, raw)
		refusal := preflightLandingCleanup(t.Context(), PullRequestLandOptions{Repository: "acme/app", ProjectsRoot: t.TempDir()}, view, "7", true)
		if refusal == nil || refusal.code != "cleanup-unverifiable" || !strings.Contains(refusal.reason, "head repository and branch") {
			t.Fatalf("unknown source accepted: %+v", refusal)
		}
	}
}
