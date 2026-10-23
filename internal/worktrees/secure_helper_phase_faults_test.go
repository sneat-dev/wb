//go:build e2e

package worktrees

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestE2ESecureHelperPhaseFailuresPreserveAuthority(t *testing.T) {
	t.Parallel()
	repo := wtLifeCovNewRepo(t)
	operationRoot, stage, stageDescriptor := wtLifeCovStage(t)
	git := wtLifeCovTrustedGit(t)
	stagePath := filepath.Join(stage, "checkout")
	cases := []struct {
		name, helper, diagnostic string
		files                    []*os.File
		args                     []string
	}{
		{"stage cwd", "fault-stage-cwd", "determine held directory: injected held cwd failure", []*os.File{stageDescriptor}, []string{secureStagePathArgument}},
		{"stage canonical cwd", "fault-stage-canonical-cwd", "derive held stage path: injected held cwd failure", []*os.File{stageDescriptor, repo.root, repo.common}, []string{operationRoot, repo.path, git, "phase-fault", repo.head, "0"}},
		{"stage canonical Git chdir", "fault-stage-canonical-common-chdir", "enter inherited Git directory: injected held chdir failure", []*os.File{stageDescriptor, repo.root, repo.common}, []string{operationRoot, repo.path, git, "phase-fault", repo.head, "0"}},
		{"canonical Git chdir", "fault-canonical-common-chdir", "enter inherited Git directory: injected held chdir failure", []*os.File{repo.root, repo.common}, []string{repo.path, git, "rev-parse", "HEAD"}},
		{"cleanup Git chdir", "fault-cleanup-common-chdir", "enter inherited canonical Git directory: injected held chdir failure", []*os.File{repo.root, repo.common}, wtLifeCovCleanupArgs(repo, git, "", "", "", "-1", "rev-parse", "HEAD")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := wtLifeCovRunSecureHelper(t, tc.helper, tc.files, tc.args)
			if result.exitCode != 1 || !strings.Contains(result.stderr, tc.diagnostic) {
				t.Fatalf("%s exit = %d, stderr = %q; want %q", tc.helper, result.exitCode, result.stderr, tc.diagnostic)
			}
			if _, err := os.Lstat(stagePath); !os.IsNotExist(err) {
				t.Fatalf("refused phase created staged checkout: %v", err)
			}
			if got := strings.TrimSpace(wtLifeCovGit(t, repo.path, "rev-parse", "HEAD")); got != repo.head {
				t.Fatalf("refused phase changed canonical HEAD from %s to %s", repo.head, got)
			}
		})
	}
}

//nolint:paralleltest // newSecureRenameHelperFixture configures process-wide Git and WB environment.
func TestE2ESecureRenameHelperPhaseFailuresPreserveHeldCheckout(t *testing.T) {
	for _, tc := range []struct{ name, helper, diagnostic string }{
		{"worktree chdir", "fault-rename-worktree-chdir", "enter inherited worktree: injected held chdir failure"},
		{"descriptor retention", "fault-rename-retain", "retain descriptor paths for Git: injected retention failure"},
		{"post-retention drift", "fault-rename-post-retention-drift", "Git or worktree directory changed before capability installation"},
	} {
		//nolint:paralleltest // each native Git fixture changes process-wide environment.
		t.Run(tc.name, func(t *testing.T) {
			fixture, canonical, parent, worktree, linked := newSecureRenameHelperFixture(t)
			t.Cleanup(canonical.close)
			t.Cleanup(func() { _ = parent.Close(); _ = worktree.Close(); linked.close() })
			expectedCanonicalHead := strings.TrimSpace(wtLifeCovGit(t, canonical.path, "rev-parse", "HEAD"))
			expectedCheckoutHead := strings.TrimSpace(wtLifeCovGit(t, fixture.worktreePath, "rev-parse", "HEAD"))
			replacement := fixture.worktreePath + "-replacement"
			if err := os.Mkdir(replacement, 0o700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(replacement, "protected")
			if err := os.WriteFile(marker, []byte("protected replacement bytes"), 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{canonical.path, fixture.worktreePath, fixture.worktreesRoot, linked.adminName, "/bin/false", "status"}
			result := wtLifeCovRunSecureHelper(t, tc.helper,
				[]*os.File{canonical.root, canonical.common, parent, worktree, linked.gitFile, linked.adminRoot, linked.admin}, args)
			if result.exitCode != 1 || !strings.Contains(result.stderr, tc.diagnostic) {
				t.Fatalf("%s exit = %d, stderr = %q; want %q", tc.helper, result.exitCode, result.stderr, tc.diagnostic)
			}
			actualCheckout := fixture.worktreePath
			if tc.helper == "fault-rename-post-retention-drift" {
				actualCheckout += "-held"
				if _, err := os.Stat(fixture.worktreePath); err != nil {
					t.Fatalf("test replacement checkout is missing: %v", err)
				}
				marker = filepath.Join(fixture.worktreePath, "protected")
				if _, err := os.Lstat(filepath.Join(fixture.worktreePath, ".git")); !os.IsNotExist(err) {
					t.Fatalf("refused phase wrote Git metadata into replacement: %v", err)
				}
			}
			if got := strings.TrimSpace(wtLifeCovGit(t, canonical.path, "rev-parse", "HEAD")); got != expectedCanonicalHead {
				t.Fatalf("refused phase changed canonical HEAD: got %s, want %s", got, expectedCanonicalHead)
			}
			if got := strings.TrimSpace(wtLifeCovGit(t, actualCheckout, "rev-parse", "HEAD")); got != expectedCheckoutHead {
				t.Fatalf("refused phase changed checkout HEAD: got %s, want %s", got, expectedCheckoutHead)
			}
			if content, err := os.ReadFile(marker); err != nil || string(content) != "protected replacement bytes" {
				t.Fatalf("refused phase changed replacement bytes: %q, %v", content, err)
			}
		})
	}
}
