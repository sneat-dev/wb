//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func branchPolicyNativeRepository(t *testing.T) (*canonicalRepository, string, string) {
	t.Helper()
	path := t.TempDir()
	gitTest(t, path, "init", "--initial-branch=main")
	seedFreshFixtureGitConfig(t, path, false)
	policy := filepath.Join(path, ".wb", "worktrees.yaml")
	if err := os.Mkdir(filepath.Dir(policy), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policy, []byte("version: 1\nworktrees:\n  branch_prefix: repository/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, path, "add", "--", ".wb/worktrees.yaml")
	gitTest(t, path, "commit", "-m", "policy fixture")
	revision := gitTestOutput(t, path, "rev-parse", "HEAD")
	canonical, err := openCanonicalRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(canonical.close)
	return canonical, revision, policy
}

func TestE2EBranchPolicyNativeBlobDisappearance(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ query, diagnostic string }{
		{"cat-file", "inspect repository worktrees policy blob size at "},
		{"show", "read repository worktrees policy blob at "},
	} {
		t.Run(tc.query, func(t *testing.T) {
			t.Parallel()
			canonical, revision, policy := branchPolicyNativeRepository(t)
			expected, err := os.ReadFile(policy)
			if err != nil {
				t.Fatal(err)
			}
			blob := gitTestOutput(t, canonical.path, "rev-parse", revision+":.wb/worktrees.yaml")
			object := filepath.Join(canonical.path, ".git", "objects", blob[:2], blob[2:])
			removed := false
			ctx := withCanonicalGitInterceptor(context.Background(), func(_ context.Context, args []string, runSecure func() ([]byte, error)) ([]byte, error) {
				if args[0] == tc.query {
					if removed {
						t.Fatal("policy query retried after blob disappearance")
					}
					if err := os.Remove(object); err != nil {
						t.Fatal(err)
					}
					removed = true
				}
				return runSecure()
			})
			contents, found, err := repositoryBranchConfigAt(ctx, canonical, revision)
			if !removed || found || contents != nil || err == nil || !strings.HasPrefix(err.Error(), tc.diagnostic+revision+": ") {
				t.Fatalf("native blob disappearance=(%q,%t,%v), removed=%t", contents, found, err, removed)
			}
			if err := canonical.validate(); err != nil {
				t.Fatalf("held canonical authority lost: %v", err)
			}
			actual, err := os.ReadFile(policy)
			if err != nil || string(actual) != string(expected) {
				t.Fatalf("checkout policy changed=%q,%v", actual, err)
			}
			if _, err := os.Stat(object); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("removed object=%v", err)
			}
		})
	}
}

func TestE2EBranchPolicyNativeHostAdmission(t *testing.T) {
	t.Parallel()
	canonical, _, _ := branchPolicyNativeRepository(t)
	gitTest(t, canonical.path, "config", "remote.origin.url", "https://")
	if got := canonicalStoreHost(context.Background(), canonical.path); got != "" {
		t.Fatalf("malformed native remote host=%q", got)
	}
	localOrigin := t.TempDir()
	gitTest(t, canonical.path, "config", "remote.origin.url", localOrigin)
	if got := canonicalStoreHost(context.Background(), canonical.path); got != "" {
		t.Fatalf("native local remote acquired a forge host=%q", got)
	}
	gitTest(t, canonical.path, "config", "remote.origin.url", "https://github.com/acme/app.git")
	if got := canonicalStoreHost(context.Background(), canonical.path); got != "github.com" {
		t.Fatalf("valid native remote host=%q", got)
	}
}

func TestE2EBranchPolicyNativeMissingHome(t *testing.T) {
	t.Parallel()
	if os.Getenv("WB_BRANCH_POLICY_HOME_CHILD") == "1" {
		_, nativeErr := os.UserHomeDir()
		if nativeErr == nil {
			t.Fatal("child still has a native home directory")
		}
		if path, err := defaultWorktreesConfigPath(); path != "" || err == nil || err.Error() != nativeErr.Error() {
			t.Fatalf("default path refusal=(%q,%v), want %v", path, err, nativeErr)
		}
		if config, found, path, err := configuredUserWorktreesConfig(); config.Version != 0 || found || path != "" || err == nil || err.Error() != nativeErr.Error() {
			t.Fatalf("user policy refusal=(%+v,%t,%q,%v)", config, found, path, err)
		}
		if prefix, err := configuredBranchPrefix(context.Background(), nil, "invalid"); prefix != "" || err == nil || err.Error() != nativeErr.Error() {
			t.Fatalf("branch policy error ordering=(%q,%v)", prefix, err)
		}
		canonical, revision, _ := branchPolicyNativeRepository(t)
		if placement, err := configuredWorktreePlacement(context.Background(), t.TempDir(), canonical, revision); placement.Root != "" || err == nil || err.Error() != nativeErr.Error() {
			t.Fatalf("placement home refusal=(%+v,%v)", placement, err)
		}
		if path, err := resolveSharedWorktreesRoot("~/store"); path != "" || err == nil || err.Error() != "resolve user home: "+nativeErr.Error() {
			t.Fatalf("shared store home refusal=(%q,%v)", path, err)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestE2EBranchPolicyNativeMissingHome$")
	command.Env = append(omitEnv(os.Environ(), []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "WB_BRANCH_POLICY_HOME_CHILD"}), "WB_BRANCH_POLICY_HOME_CHILD=1")
	if testing.CoverMode() != "" {
		if directory := wtLifeCovCoverDir(); directory != "" {
			command.Args = append(command.Args, "-test.gocoverdir="+directory)
			command.Env = append(command.Env, "GOCOVERDIR="+directory)
		}
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("native missing-home child=%v\n%s", err, output)
	}
}
