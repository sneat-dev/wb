//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

//nolint:paralleltest // the Git fixture and WB home state are process-wide.
func TestE2EClaimPublicationRetryRejectsUntrustedAuthority(t *testing.T) {
	for _, testCase := range []struct {
		name           string
		breakAuthority func(*testing.T, WorkLogPublicationOutcome, CreateResult)
	}{
		{name: "malformed private claim", breakAuthority: func(t *testing.T, outcome WorkLogPublicationOutcome, _ CreateResult) {
			if err := os.WriteFile(outcome.ClaimPath, []byte("{broken"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "failed Git corroboration", breakAuthority: func(t *testing.T, _ WorkLogPublicationOutcome, result CreateResult) {
			if _, err := git(context.Background(), result.WorktreeDir, "branch", "-m", "unexpected-branch"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unreadable live Git checkout", breakAuthority: func(t *testing.T, _ WorkLogPublicationOutcome, result CreateResult) {
			if err := os.Rename(filepath.Join(result.WorktreeDir, ".git"), filepath.Join(result.WorktreeDir, ".git-held")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		//nolint:paralleltest // the Git fixture sets process-wide WB home and environment.
		t.Run(testCase.name, func(t *testing.T) {
			gitFixture := newGitFixture(t)
			evidence := observeLocalGit(context.Background(), gitFixture.canonical)
			result := CreateResult{Repository: "acme/app", WorktreeDir: gitFixture.canonical, Branch: evidence.Branch, Base: "main", BaseSHA: evidence.Head}
			options, err := (WorkLogOptions{EffortID: "retry-refusal", RunID: "run", Model: "unknown"}).WithOriginalPromptFromStdin([]byte("original prompt\n"))
			if err != nil {
				t.Fatal(err)
			}
			interrupted := errors.New("interrupted")
			outcome, err := recordWorkLogWithHooks(gitFixture.home, "retry-refusal", result, options, workLogPublicationHooks{afterClaim: func() error { return interrupted }})
			if !errors.Is(err, interrupted) {
				t.Fatalf("interruption err=%v", err)
			}
			testCase.breakAuthority(t, outcome, result)
			if _, err := EnsureWorkLogClaim(gitFixture.home, "retry-refusal", result, options); err == nil {
				t.Fatal("untrusted authority accepted")
			}
			if _, err := os.Stat(filepath.Join(result.WorktreeDir, workLogProjectionDirectory, workLogProjectionName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected authority published projection: %v", err)
			}
		})
	}
}
